# Aggregation performance

Written by `make perf-notes`; do not edit by hand. It records one run of
`TestAggregationPerformance` (`cmd/sprout/perf_test.go`), which every
`make test` repeats and holds to the same budget.

## Dataset

One user with 50,000 transactions spread evenly over three years, written by
`sprout seed`, next to four other users with 25,000 each: 150,000 rows in
`transactions`. Nine in ten are expenses, in the seven default categories.

## Budget

Each read endpoint answers within 100 ms at the database at the 95th
percentile, and no query plan reads the whole `transactions` table.

The times below are what one request's queries took together, measured from
the Go side over 40 runs on a single connection, on the machine that wrote
this file. They are an indication, not a benchmark of the hardware.

| Request | p95 | Slowest |
|---|---|---|
| `GET /home` | 4.69ms | 7.16ms |
| `GET /summary/categories?period=week` | 440µs | 1.17ms |
| `GET /summary/categories?period=month` | 1.38ms | 1.48ms |
| `GET /summary/categories?period=year` | 9.99ms | 10.5ms |
| `GET /summary/categories/{id}?period=week` | 490µs | 1.06ms |
| `GET /summary/categories/{id}?period=month` | 480µs | 930µs |
| `GET /summary/categories/{id}?period=year` | 1.03ms | 1.37ms |
| `GET /summary/stats?period=week` | 980µs | 1.11ms |
| `GET /summary/stats?period=month` | 990µs | 1.33ms |
| `GET /summary/stats?period=year` | 3.71ms | 3.83ms |
| `GET /transactions` | 660µs | 760µs |
| `GET /transactions?cursor=` | 490µs | 730µs |
| `GET /transactions?category_id=&from=&to=` | 480µs | 720µs |
| `GET /transactions?kind=expense` | 530µs | 610µs |
| `GET /transactions?q=merchant 01` | 4.31ms | 4.88ms |

## Plans

`EXPLAIN (ANALYZE, BUFFERS)` of every query each request runs, with the
arguments of its last run.

### `GET /home`

`LedgerNet`

```
Aggregate  (cost=3836.32..3836.33 rows=1 width=8) (actual time=3.881..3.881 rows=1 loops=1)
  Buffers: shared hit=623
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..3458.39 rows=50390 width=15) (actual time=0.014..2.299 rows=50000 loops=1)
        Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
        Heap Fetches: 0
        Buffers: shared hit=623
Planning Time: 0.034 ms
Execution Time: 3.894 ms
```

`SpentByCategory`

```
Sort  (cost=109.33..109.33 rows=2 width=28) (actual time=0.256..0.256 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=15
  ->  GroupAggregate  (cost=109.25..109.32 rows=2 width=28) (actual time=0.213..0.255 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=15
        ->  Sort  (cost=109.25..109.26 rows=5 width=28) (actual time=0.204..0.221 rows=867 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 72kB
              Buffers: shared hit=15
              ->  Hash Join  (cost=9.95..109.19 rows=5 width=28) (actual time=0.015..0.144 rows=867 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=15
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..97.15 rows=949 width=40) (actual time=0.008..0.071 rows=867 loops=1)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=13
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.004..0.005 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.002..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.061 ms
Execution Time: 0.287 ms
```

`SpentBetween`

```
Aggregate  (cost=128.59..128.60 rows=1 width=8) (actual time=0.141..0.141 rows=1 loops=1)
  Buffers: shared hit=18
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..125.50 rows=1233 width=8) (actual time=0.010..0.098 rows=1185 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-06-01'::date) AND (local_date <= '2025-06-30'::date))
        Heap Fetches: 0
        Buffers: shared hit=18
Planning Time: 0.038 ms
Execution Time: 0.149 ms
```

`CountActiveCategories`

```
Aggregate  (cost=8.16..8.17 rows=1 width=8) (actual time=0.008..0.008 rows=1 loops=1)
  Buffers: shared hit=2
  ->  Index Only Scan using categories_user_id_name_key on categories  (cost=0.14..8.16 rows=1 width=0) (actual time=0.006..0.007 rows=7 loops=1)
        Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
        Heap Fetches: 7
        Buffers: shared hit=2
Planning Time: 0.017 ms
Execution Time: 0.016 ms
```

`ListTransactions`

```
Limit  (cost=0.42..2.61 rows=11 width=193) (actual time=0.006..0.009 rows=11 loops=1)
  Buffers: shared hit=14
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..10009.92 rows=50390 width=193) (actual time=0.006..0.008 rows=11 loops=1)
        Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
        Buffers: shared hit=14
Planning Time: 0.025 ms
Execution Time: 0.015 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=88.14..89.47 rows=44 width=20) (actual time=0.019..0.019 rows=1 loops=1)
  Group Key: local_date
  Buffers: shared hit=34
  ->  Sort  (cost=88.14..88.25 rows=45 width=19) (actual time=0.015..0.016 rows=31 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 26kB
        Buffers: shared hit=34
        ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..86.90 rows=45 width=19) (actual time=0.005..0.013 rows=31 loops=1)
              Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (local_date = ANY ('{2025-07-21}'::date[])))
              Buffers: shared hit=34
Planning Time: 0.029 ms
Execution Time: 0.029 ms
```

### `GET /summary/categories?period=week`

`CategorySpending`

```
Sort  (cost=58.69..58.69 rows=1 width=48) (actual time=0.139..0.139 rows=7 loops=1)
  Sort Key: ((COALESCE(sum(t.amount_minor) FILTER (WHERE (t.local_date >= '2025-07-21'::date)), '0'::numeric))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=337
  ->  GroupAggregate  (cost=58.59..58.68 rows=1 width=48) (actual time=0.118..0.137 rows=7 loops=1)
        Group Key: c.id
        Filter: ((c.archived_at IS NULL) OR (count(t.id) FILTER (WHERE (t.local_date >= '2025-07-21'::date)) > 0))
        Buffers: shared hit=337
        ->  Sort  (cost=58.59..58.60 rows=2 width=64) (actual time=0.113..0.119 rows=315 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 49kB
              Buffers: shared hit=337
              ->  Nested Loop Left Join  (cost=4.58..58.58 rows=2 width=64) (actual time=0.006..0.088 rows=315 loops=1)
                    Buffers: shared hit=337
                    ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=52) (actual time=0.002..0.002 rows=7 loops=1)
                          Recheck Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                          Heap Blocks: exact=1
                          Buffers: shared hit=2
                          ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                Buffers: shared hit=1
                    ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions t  (cost=0.42..24.45 rows=9 width=60) (actual time=0.002..0.009 rows=45 loops=7)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (category_id = c.id) AND (local_date >= '2025-07-14'::date) AND (local_date <= '2025-07-27'::date))
                          Filter: (kind = 'expense'::text)
                          Buffers: shared hit=335
Planning Time: 0.060 ms
Execution Time: 0.160 ms
```

### `GET /summary/categories?period=month`

`CategorySpending`

```
Sort  (cost=245.85..245.85 rows=1 width=48) (actual time=0.900..0.901 rows=7 loops=1)
  Sort Key: ((COALESCE(sum(t.amount_minor) FILTER (WHERE (t.local_date >= '2025-07-01'::date)), '0'::numeric))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=2074
  ->  GroupAggregate  (cost=245.59..245.84 rows=1 width=48) (actual time=0.761..0.899 rows=7 loops=1)
        Group Key: c.id
        Filter: ((c.archived_at IS NULL) OR (count(t.id) FILTER (WHERE (t.local_date >= '2025-07-01'::date)) > 0))
        Buffers: shared hit=2074
        ->  Sort  (cost=245.59..245.62 rows=10 width=64) (actual time=0.737..0.787 rows=2052 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 257kB
              Buffers: shared hit=2074
              ->  Nested Loop Left Join  (cost=4.58..245.43 rows=10 width=64) (actual time=0.006..0.547 rows=2052 loops=1)
                    Buffers: shared hit=2074
                    ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=52) (actual time=0.002..0.003 rows=7 loops=1)
                          Recheck Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                          Heap Blocks: exact=1
                          Buffers: shared hit=2
                          ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                Buffers: shared hit=1
                    ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions t  (cost=0.42..117.40 rows=56 width=60) (actual time=0.002..0.056 rows=293 loops=7)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (category_id = c.id) AND (local_date >= '2025-06-01'::date) AND (local_date <= '2025-07-31'::date))
                          Filter: (kind = 'expense'::text)
                          Buffers: shared hit=2072
Planning Time: 0.074 ms
Execution Time: 0.927 ms
```

### `GET /summary/categories?period=year`

`CategorySpending`

```
Sort  (cost=2156.13..2156.14 rows=1 width=48) (actual time=9.502..9.503 rows=7 loops=1)
  Sort Key: ((COALESCE(sum(t.amount_minor) FILTER (WHERE (t.local_date >= '2025-01-01'::date)), '0'::numeric))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=23363
  ->  GroupAggregate  (cost=2153.84..2156.12 rows=1 width=48) (actual time=8.252..9.500 rows=7 loops=1)
        Group Key: c.id
        Filter: ((c.archived_at IS NULL) OR (count(t.id) FILTER (WHERE (t.local_date >= '2025-01-01'::date)) > 0))
        Buffers: shared hit=23363
        ->  Sort  (cost=2153.84..2154.12 rows=112 width=64) (actual time=8.003..8.455 rows=23335 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 2592kB
              Buffers: shared hit=23363
              ->  Nested Loop Left Join  (cost=4.58..2150.03 rows=112 width=64) (actual time=0.015..6.139 rows=23335 loops=1)
                    Buffers: shared hit=23363
                    ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=52) (actual time=0.005..0.007 rows=7 loops=1)
                          Recheck Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                          Heap Blocks: exact=1
                          Buffers: shared hit=2
                          ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.003..0.004 rows=7 loops=1)
                                Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                Buffers: shared hit=1
                    ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions t  (cost=0.42..1064.22 rows=604 width=60) (actual time=0.004..0.648 rows=3334 loops=7)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (category_id = c.id) AND (local_date >= '2024-01-01'::date) AND (local_date <= '2025-12-31'::date))
                          Filter: (kind = 'expense'::text)
                          Buffers: shared hit=23361
Planning Time: 0.118 ms
Execution Time: 9.564 ms
```

### `GET /summary/categories/{id}?period=week`

`GetCategory`

```
Index Scan using categories_pkey on categories  (cost=0.15..8.17 rows=1 width=166) (actual time=0.005..0.005 rows=1 loops=1)
  Index Cond: (id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid)
  Filter: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
  Buffers: shared hit=2
Planning Time: 0.018 ms
Execution Time: 0.009 ms
```

`CategorySpentByDay`

```
GroupAggregate  (cost=0.42..9.46 rows=2 width=16) (actual time=0.011..0.011 rows=1 loops=1)
  Group Key: local_date
  Buffers: shared hit=4
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..9.41 rows=2 width=12) (actual time=0.008..0.009 rows=6 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-21'::date) AND (local_date <= '2025-07-27'::date))
        Filter: (category_id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid)
        Rows Removed by Filter: 22
        Heap Fetches: 0
        Buffers: shared hit=4
Planning Time: 0.045 ms
Execution Time: 0.018 ms
```

### `GET /summary/categories/{id}?period=month`

`GetCategory`

```
Index Scan using categories_pkey on categories  (cost=0.15..8.17 rows=1 width=166) (actual time=0.005..0.005 rows=1 loops=1)
  Index Cond: (id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid)
  Filter: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
  Buffers: shared hit=2
Planning Time: 0.013 ms
Execution Time: 0.009 ms
```

`CategorySpentByDay`

```
GroupAggregate  (cost=0.42..90.03 rows=40 width=16) (actual time=0.009..0.044 rows=21 loops=1)
  Group Key: local_date
  Buffers: shared hit=140
  ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..89.02 rows=41 width=12) (actual time=0.006..0.032 rows=137 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (category_id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
        Filter: (kind = 'expense'::text)
        Buffers: shared hit=140
Planning Time: 0.028 ms
Execution Time: 0.051 ms
```

### `GET /summary/categories/{id}?period=year`

`GetCategory`

```
Index Scan using categories_pkey on categories  (cost=0.15..8.17 rows=1 width=166) (actual time=0.006..0.006 rows=1 loops=1)
  Index Cond: (id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid)
  Filter: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
  Buffers: shared hit=2
Planning Time: 0.015 ms
Execution Time: 0.010 ms
```

`CategorySpentByDay`

```
GroupAggregate  (cost=0.42..707.73 rows=310 width=16) (actual time=0.011..0.370 rows=201 loops=1)
  Group Key: local_date
  Buffers: shared hit=1170
  ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..699.58 rows=364 width=12) (actual time=0.008..0.268 rows=1165 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (category_id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid) AND (local_date >= '2025-01-01'::date) AND (local_date <= '2025-12-31'::date))
        Filter: (kind = 'expense'::text)
        Buffers: shared hit=1170
Planning Time: 0.038 ms
Execution Time: 0.381 ms
```

### `GET /summary/stats?period=week`

`SpentByCategory`

```
Sort  (cost=18.99..18.99 rows=1 width=28) (actual time=0.024..0.024 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=6
  ->  GroupAggregate  (cost=18.95..18.98 rows=1 width=28) (actual time=0.020..0.023 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=6
        ->  Sort  (cost=18.95..18.96 rows=1 width=28) (actual time=0.019..0.020 rows=28 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 26kB
              Buffers: shared hit=6
              ->  Hash Join  (cost=9.95..18.94 rows=1 width=28) (actual time=0.013..0.016 rows=28 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=6
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..9.32 rows=36 width=40) (actual time=0.006..0.007 rows=28 loops=1)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-21'::date) AND (local_date <= '2025-07-27'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=4
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.004..0.005 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.001..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.059 ms
Execution Time: 0.041 ms
```

`SpentBetween`

```
Aggregate  (cost=36.33..36.35 rows=1 width=8) (actual time=0.034..0.034 rows=1 loops=1)
  Buffers: shared hit=7
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..35.52 rows=324 width=8) (actual time=0.006..0.025 rows=287 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-14'::date) AND (local_date <= '2025-07-20'::date))
        Heap Fetches: 0
        Buffers: shared hit=7
Planning Time: 0.023 ms
Execution Time: 0.039 ms
```

`SpentByDay`

```
GroupAggregate  (cost=0.42..10.02 rows=35 width=12) (actual time=0.011..0.011 rows=1 loops=1)
  Group Key: local_date
  Buffers: shared hit=4
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..9.32 rows=36 width=12) (actual time=0.006..0.008 rows=28 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-21'::date) AND (local_date <= '2025-07-27'::date))
        Heap Fetches: 0
        Buffers: shared hit=4
Planning Time: 0.025 ms
Execution Time: 0.018 ms
```

### `GET /summary/stats?period=month`

`SpentByCategory`

```
Sort  (cost=109.33..109.33 rows=2 width=28) (actual time=0.249..0.249 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=15
  ->  GroupAggregate  (cost=109.25..109.32 rows=2 width=28) (actual time=0.198..0.248 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=15
        ->  Sort  (cost=109.25..109.26 rows=5 width=28) (actual time=0.189..0.206 rows=867 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 72kB
              Buffers: shared hit=15
              ->  Hash Join  (cost=9.95..109.19 rows=5 width=28) (actual time=0.014..0.134 rows=867 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=15
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..97.15 rows=949 width=40) (actual time=0.008..0.072 rows=867 loops=1)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=13
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.004..0.004 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.001..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.061 ms
Execution Time: 0.268 ms
```

`SpentBetween`

```
Aggregate  (cost=128.59..128.60 rows=1 width=8) (actual time=0.153..0.153 rows=1 loops=1)
  Buffers: shared hit=18
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..125.50 rows=1233 width=8) (actual time=0.009..0.115 rows=1185 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-06-01'::date) AND (local_date <= '2025-06-30'::date))
        Heap Fetches: 0
        Buffers: shared hit=18
Planning Time: 0.030 ms
Execution Time: 0.158 ms
```

`SpentByDay`

```
GroupAggregate  (cost=0.42..111.44 rows=636 width=12) (actual time=0.013..0.124 rows=21 loops=1)
  Group Key: local_date
  Buffers: shared hit=13
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..97.15 rows=949 width=12) (actual time=0.008..0.067 rows=867 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
        Heap Fetches: 0
        Buffers: shared hit=13
Planning Time: 0.043 ms
Execution Time: 0.130 ms
```

### `GET /summary/stats?period=year`

`SpentByCategory`

```
Sort  (cost=759.70..759.71 rows=2 width=28) (actual time=2.445..2.446 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=109
  ->  GroupAggregate  (cost=759.36..759.69 rows=2 width=28) (actual time=1.947..2.444 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=109
        ->  Sort  (cost=759.36..759.46 rows=40 width=28) (actual time=1.856..2.043 rows=8252 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 836kB
              Buffers: shared hit=109
              ->  Hash Join  (cost=9.95..758.30 rows=40 width=28) (actual time=0.018..1.255 rows=8252 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=109
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..726.48 rows=8414 width=40) (actual time=0.010..0.617 rows=8252 loops=1)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-01-01'::date) AND (local_date <= '2025-12-31'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=107
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.005..0.005 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.002..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.083 ms
Execution Time: 2.478 ms
```

`SpentBetween`

```
Aggregate  (cost=1254.61..1254.62 rows=1 width=8) (actual time=1.450..1.450 rows=1 loops=1)
  Buffers: shared hit=191
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..1217.03 rows=15031 width=8) (actual time=0.009..1.018 rows=15083 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2024-01-01'::date) AND (local_date <= '2024-12-31'::date))
        Heap Fetches: 0
        Buffers: shared hit=191
Planning Time: 0.028 ms
Execution Time: 1.456 ms
```

`SpentByDay`

```
GroupAggregate  (cost=0.42..785.01 rows=1097 width=12) (actual time=0.014..0.917 rows=202 loops=1)
  Group Key: local_date
  Buffers: shared hit=107
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..726.48 rows=8414 width=12) (actual time=0.008..0.553 rows=8252 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-01-01'::date) AND (local_date <= '2025-12-31'::date))
        Heap Fetches: 0
        Buffers: shared hit=107
Planning Time: 0.034 ms
Execution Time: 0.926 ms
```

### `GET /transactions`

`ListTransactions`

```
Limit  (cost=0.42..10.55 rows=51 width=193) (actual time=0.005..0.017 rows=51 loops=1)
  Buffers: shared hit=54
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..10009.92 rows=50390 width=193) (actual time=0.005..0.015 rows=51 loops=1)
        Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
        Buffers: shared hit=54
Planning Time: 0.020 ms
Execution Time: 0.022 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=172.73..175.37 rows=86 width=20) (actual time=0.032..0.036 rows=2 loops=1)
  Group Key: local_date
  Buffers: shared hit=86
  ->  Sort  (cost=172.73..172.95 rows=90 width=19) (actual time=0.029..0.030 rows=82 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 28kB
        Buffers: shared hit=86
        ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..169.80 rows=90 width=19) (actual time=0.007..0.024 rows=82 loops=1)
              Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20}'::date[])))
              Buffers: shared hit=86
Planning Time: 0.028 ms
Execution Time: 0.042 ms
```

### `GET /transactions?cursor=`

`ListTransactions`

```
Limit  (cost=0.42..23.81 rows=51 width=193) (actual time=0.016..0.032 rows=51 loops=1)
  Buffers: shared hit=55
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..7744.77 rows=16885 width=193) (actual time=0.016..0.030 rows=51 loops=1)
        Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (ROW(local_date, id) < ROW('2023-07-21'::date, '01a11bb9-1ff5-7111-8050-d14966c28b6c'::uuid)))
        Buffers: shared hit=55
Planning Time: 0.023 ms
Execution Time: 0.038 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=208.94..212.17 rows=105 width=20) (actual time=0.031..0.035 rows=2 loops=1)
  Group Key: local_date
  Buffers: shared hit=86
  ->  Sort  (cost=208.94..209.22 rows=110 width=19) (actual time=0.027..0.028 rows=82 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 28kB
        Buffers: shared hit=86
        ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..205.21 rows=110 width=19) (actual time=0.005..0.022 rows=82 loops=1)
              Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (local_date = ANY ('{2023-07-21,2023-07-20}'::date[])))
              Buffers: shared hit=86
Planning Time: 0.022 ms
Execution Time: 0.040 ms
```

### `GET /transactions?category_id=&from=&to=`

`ListTransactions`

```
Limit  (cost=2.38..99.09 rows=51 width=193) (actual time=0.017..0.025 rows=51 loops=1)
  Buffers: shared hit=57
  ->  Incremental Sort  (cost=2.38..121.85 rows=63 width=193) (actual time=0.017..0.024 rows=51 loops=1)
        Sort Key: local_date DESC, id DESC
        Presorted Key: local_date
        Full-sort Groups: 2  Sort Method: quicksort  Average Memory: 34kB  Peak Memory: 34kB
        Buffers: shared hit=57
        ->  Index Scan Backward using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..119.09 rows=63 width=193) (actual time=0.005..0.016 rows=54 loops=1)
              Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (category_id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid) AND (local_date >= '2025-06-21'::date) AND (local_date <= '2025-07-21'::date))
              Buffers: shared hit=57
Planning Time: 0.025 ms
Execution Time: 0.031 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=39.52..39.55 rows=1 width=20) (actual time=0.026..0.031 rows=8 loops=1)
  Group Key: local_date
  Buffers: shared hit=77
  ->  Sort  (cost=39.52..39.52 rows=1 width=19) (actual time=0.024..0.025 rows=53 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 27kB
        Buffers: shared hit=77
        ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..39.51 rows=1 width=19) (actual time=0.005..0.021 rows=53 loops=1)
              Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (category_id = '01a11bb9-1d29-7e30-90a8-afedc6d783f6'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20,2025-07-19,2025-07-18,2025-07-17,2025-07-16,2025-07-15,2025-07-14}'::date[])) AND (local_date >= '2025-06-21'::date) AND (local_date <= '2025-07-21'::date))
              Buffers: shared hit=77
Planning Time: 0.033 ms
Execution Time: 0.037 ms
```

### `GET /transactions?kind=expense`

`ListTransactions`

```
Limit  (cost=0.42..11.80 rows=51 width=193) (actual time=0.006..0.022 rows=51 loops=1)
  Buffers: shared hit=59
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..10135.90 rows=45417 width=193) (actual time=0.006..0.019 rows=51 loops=1)
        Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
        Filter: (kind = 'expense'::text)
        Rows Removed by Filter: 5
        Buffers: shared hit=59
Planning Time: 0.023 ms
Execution Time: 0.027 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=18.97..21.35 rows=78 width=20) (actual time=0.022..0.026 rows=2 loops=1)
  Group Key: local_date
  Buffers: shared hit=7
  ->  Sort  (cost=18.97..19.17 rows=81 width=19) (actual time=0.018..0.020 rows=73 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 28kB
        Buffers: shared hit=7
        ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..16.40 rows=81 width=19) (actual time=0.009..0.014 rows=73 loops=1)
              Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (kind = 'expense'::text) AND (local_date = ANY ('{2025-07-21,2025-07-20}'::date[])))
              Heap Fetches: 0
              Buffers: shared hit=7
Planning Time: 0.026 ms
Execution Time: 0.033 ms
```

### `GET /transactions?q=merchant 01`

`ListTransactions`

```
Limit  (cost=0.42..264.06 rows=51 width=193) (actual time=0.008..0.337 rows=51 loops=1)
  Buffers: shared hit=1102
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..10261.87 rows=1985 width=193) (actual time=0.008..0.335 rows=51 loops=1)
        Index Cond: (user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid)
        Filter: ((merchant ~~* '%merchant 01%'::text) OR (note ~~* '%merchant 01%'::text))
        Rows Removed by Filter: 1054
        Buffers: shared hit=1102
Planning:
  Buffers: shared hit=2
Planning Time: 0.069 ms
Execution Time: 0.342 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=488.03..489.27 rows=41 width=20) (actual time=2.814..2.822 rows=23 loops=1)
  Group Key: local_date
  Buffers: shared hit=404
  ->  Sort  (cost=488.03..488.13 rows=42 width=19) (actual time=2.812..2.814 rows=52 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 27kB
        Buffers: shared hit=404
        ->  Bitmap Heap Scan on transactions  (cost=331.32..486.90 rows=42 width=19) (actual time=2.786..2.808 rows=52 loops=1)
              Recheck Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20,2025-07-19,2025-07-18,2025-07-17,2025-07-16,2025-07-14,2025-07-13,2025-07-12,2025-07-11,2025-07-10,2025-07-08,2025-07-07,2025-07-06,2025-07-05,2025-07-04,2025-07-03,2025-07-02,2025-07-01,2025-06-30,2025-06-29,2025-06-28,2025-06-27}'::date[])) AND ((merchant ~~* '%merchant 01%'::text) OR (note ~~* '%merchant 01%'::text)))
              Heap Blocks: exact=51
              Buffers: shared hit=404
              ->  BitmapAnd  (cost=331.26..331.26 rows=42 width=0) (actual time=2.783..2.783 rows=0 loops=1)
                    Buffers: shared hit=353
                    ->  Bitmap Index Scan on transactions_user_id_local_date_id_idx  (cost=0.00..112.24 rows=1058 width=0) (actual time=0.054..0.054 rows=1047 loops=1)
                          Index Cond: ((user_id = '01a11bb9-1d29-7301-a39a-5f7bcc864c78'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20,2025-07-19,2025-07-18,2025-07-17,2025-07-16,2025-07-14,2025-07-13,2025-07-12,2025-07-11,2025-07-10,2025-07-08,2025-07-07,2025-07-06,2025-07-05,2025-07-04,2025-07-03,2025-07-02,2025-07-01,2025-06-30,2025-06-29,2025-06-28,2025-06-27}'::date[])))
                          Buffers: shared hit=70
                    ->  BitmapOr  (cost=218.76..218.76 rows=5908 width=0) (actual time=2.720..2.720 rows=0 loops=1)
                          Buffers: shared hit=283
                          ->  Bitmap Index Scan on transactions_merchant_trgm_idx  (cost=0.00..136.46 rows=5906 width=0) (actual time=2.717..2.717 rows=5757 loops=1)
                                Index Cond: (merchant ~~* '%merchant 01%'::text)
                                Buffers: shared hit=264
                          ->  Bitmap Index Scan on transactions_note_trgm_idx  (cost=0.00..82.28 rows=3 width=0) (actual time=0.003..0.003 rows=0 loops=1)
                                Index Cond: (note ~~* '%merchant 01%'::text)
                                Buffers: shared hit=19
Planning:
  Buffers: shared hit=2
Planning Time: 0.084 ms
Execution Time: 2.852 ms
```
