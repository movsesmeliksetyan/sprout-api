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
| `GET /home` | 4.79ms | 7.4ms |
| `GET /summary/categories?period=week` | 500µs | 1ms |
| `GET /summary/categories?period=month` | 830µs | 1.15ms |
| `GET /summary/categories?period=year` | 7.62ms | 8.15ms |
| `GET /summary/categories/{id}?period=week` | 630µs | 940µs |
| `GET /summary/categories/{id}?period=month` | 480µs | 540µs |
| `GET /summary/categories/{id}?period=year` | 680µs | 1.25ms |
| `GET /summary/stats?period=week` | 660µs | 800µs |
| `GET /summary/stats?period=month` | 830µs | 1.06ms |
| `GET /summary/stats?period=year` | 3.9ms | 4.05ms |
| `GET /transactions` | 1.07ms | 1.38ms |
| `GET /transactions?cursor=` | 610µs | 780µs |
| `GET /transactions?category_id=&from=&to=` | 570µs | 820µs |
| `GET /transactions?kind=expense` | 530µs | 820µs |
| `GET /transactions?q=merchant ab` | 4.04ms | 4.77ms |

## Plans

`EXPLAIN (ANALYZE, BUFFERS)` of every query each request runs, with the
arguments of its last run.

### `GET /home`

`LedgerNet`

```
Aggregate  (cost=3813.31..3813.33 rows=1 width=8) (actual time=3.684..3.684 rows=1 loops=1)
  Buffers: shared hit=623
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..3437.49 rows=50110 width=15) (actual time=0.006..2.087 rows=50000 loops=1)
        Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
        Heap Fetches: 0
        Buffers: shared hit=623
Planning Time: 0.018 ms
Execution Time: 3.691 ms
```

`SavedInGoals`

```
Aggregate  (cost=11.31..11.32 rows=1 width=8) (actual time=0.002..0.003 rows=1 loops=1)
  Buffers: shared hit=2
  ->  Bitmap Heap Scan on goal_contributions  (cost=4.17..11.28 rows=3 width=40) (actual time=0.002..0.002 rows=0 loops=1)
        Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
        Buffers: shared hit=2
        ->  Bitmap Index Scan on goal_contributions_user_id_created_at_idx  (cost=0.00..4.17 rows=3 width=0) (actual time=0.000..0.000 rows=0 loops=1)
              Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
              Buffers: shared hit=2
Planning:
  Buffers: shared hit=3
Planning Time: 0.013 ms
Execution Time: 0.011 ms
```

`SpentByCategory`

```
Sort  (cost=109.47..109.47 rows=2 width=28) (actual time=0.245..0.245 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=15
  ->  GroupAggregate  (cost=109.39..109.46 rows=2 width=28) (actual time=0.203..0.244 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=15
        ->  Sort  (cost=109.39..109.40 rows=5 width=28) (actual time=0.194..0.211 rows=867 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 72kB
              Buffers: shared hit=15
              ->  Hash Join  (cost=9.95..109.33 rows=5 width=28) (actual time=0.014..0.134 rows=867 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=15
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..97.27 rows=954 width=40) (actual time=0.008..0.065 rows=867 loops=1)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=13
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.004..0.004 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.002..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.060 ms
Execution Time: 0.266 ms
```

`SpentBetween`

```
Aggregate  (cost=124.34..124.35 rows=1 width=8) (actual time=0.113..0.113 rows=1 loops=1)
  Buffers: shared hit=18
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..121.27 rows=1224 width=8) (actual time=0.008..0.086 rows=1185 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-06-01'::date) AND (local_date <= '2025-06-30'::date))
        Heap Fetches: 0
        Buffers: shared hit=18
Planning Time: 0.025 ms
Execution Time: 0.119 ms
```

`CountActiveCategories`

```
Aggregate  (cost=8.16..8.17 rows=1 width=8) (actual time=0.006..0.006 rows=1 loops=1)
  Buffers: shared hit=2
  ->  Index Only Scan using categories_user_id_name_key on categories  (cost=0.14..8.16 rows=1 width=0) (actual time=0.005..0.005 rows=7 loops=1)
        Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
        Heap Fetches: 7
        Buffers: shared hit=2
Planning Time: 0.013 ms
Execution Time: 0.011 ms
```

`ListTransactions`

```
Limit  (cost=0.42..2.61 rows=11 width=194) (actual time=0.005..0.007 rows=11 loops=1)
  Buffers: shared hit=14
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..9992.64 rows=50110 width=194) (actual time=0.004..0.007 rows=11 loops=1)
        Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
        Buffers: shared hit=14
Planning Time: 0.024 ms
Execution Time: 0.012 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=88.13..89.47 rows=44 width=20) (actual time=0.016..0.016 rows=1 loops=1)
  Group Key: local_date
  Buffers: shared hit=34
  ->  Sort  (cost=88.13..88.24 rows=45 width=19) (actual time=0.013..0.013 rows=31 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 26kB
        Buffers: shared hit=34
        ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..86.90 rows=45 width=19) (actual time=0.004..0.011 rows=31 loops=1)
              Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (local_date = ANY ('{2025-07-21}'::date[])))
              Buffers: shared hit=34
Planning Time: 0.029 ms
Execution Time: 0.025 ms
```

### `GET /summary/categories?period=week`

`CategorySpending`

```
Sort  (cost=62.26..62.26 rows=1 width=48) (actual time=0.138..0.139 rows=7 loops=1)
  Sort Key: ((COALESCE(sum(t.amount_minor) FILTER (WHERE (t.local_date >= '2025-07-21'::date)), '0'::numeric))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=337
  ->  GroupAggregate  (cost=62.17..62.25 rows=1 width=48) (actual time=0.117..0.137 rows=7 loops=1)
        Group Key: c.id
        Filter: ((c.archived_at IS NULL) OR (count(t.id) FILTER (WHERE (t.local_date >= '2025-07-21'::date)) > 0))
        Buffers: shared hit=337
        ->  Sort  (cost=62.17..62.17 rows=2 width=64) (actual time=0.112..0.118 rows=315 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 49kB
              Buffers: shared hit=337
              ->  Nested Loop Left Join  (cost=4.58..62.16 rows=2 width=64) (actual time=0.006..0.087 rows=315 loops=1)
                    Buffers: shared hit=337
                    ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=52) (actual time=0.002..0.002 rows=7 loops=1)
                          Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                          Heap Blocks: exact=1
                          Buffers: shared hit=2
                          ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                Buffers: shared hit=1
                    ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions t  (cost=0.42..26.23 rows=10 width=60) (actual time=0.002..0.009 rows=45 loops=7)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (category_id = c.id) AND (local_date >= '2025-07-14'::date) AND (local_date <= '2025-07-27'::date))
                          Filter: (kind = 'expense'::text)
                          Buffers: shared hit=335
Planning Time: 0.049 ms
Execution Time: 0.159 ms
```

### `GET /summary/categories?period=month`

`CategorySpending`

```
Sort  (cost=245.83..245.84 rows=1 width=48) (actual time=0.795..0.795 rows=7 loops=1)
  Sort Key: ((COALESCE(sum(t.amount_minor) FILTER (WHERE (t.local_date >= '2025-07-01'::date)), '0'::numeric))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=2074
  ->  GroupAggregate  (cost=245.58..245.82 rows=1 width=48) (actual time=0.684..0.794 rows=7 loops=1)
        Group Key: c.id
        Filter: ((c.archived_at IS NULL) OR (count(t.id) FILTER (WHERE (t.local_date >= '2025-07-01'::date)) > 0))
        Buffers: shared hit=2074
        ->  Sort  (cost=245.58..245.60 rows=10 width=64) (actual time=0.663..0.702 rows=2052 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 257kB
              Buffers: shared hit=2074
              ->  Nested Loop Left Join  (cost=4.58..245.41 rows=10 width=64) (actual time=0.006..0.466 rows=2052 loops=1)
                    Buffers: shared hit=2074
                    ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=52) (actual time=0.001..0.002 rows=7 loops=1)
                          Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                          Heap Blocks: exact=1
                          Buffers: shared hit=2
                          ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                Buffers: shared hit=1
                    ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions t  (cost=0.42..117.39 rows=56 width=60) (actual time=0.002..0.048 rows=293 loops=7)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (category_id = c.id) AND (local_date >= '2025-06-01'::date) AND (local_date <= '2025-07-31'::date))
                          Filter: (kind = 'expense'::text)
                          Buffers: shared hit=2072
Planning Time: 0.046 ms
Execution Time: 0.818 ms
```

### `GET /summary/categories?period=year`

`CategorySpending`

```
Sort  (cost=2168.61..2168.62 rows=1 width=48) (actual time=8.715..8.715 rows=7 loops=1)
  Sort Key: ((COALESCE(sum(t.amount_minor) FILTER (WHERE (t.local_date >= '2025-01-01'::date)), '0'::numeric))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=23363
  ->  GroupAggregate  (cost=2166.32..2168.60 rows=1 width=48) (actual time=7.248..8.713 rows=7 loops=1)
        Group Key: c.id
        Filter: ((c.archived_at IS NULL) OR (count(t.id) FILTER (WHERE (t.local_date >= '2025-01-01'::date)) > 0))
        Buffers: shared hit=23363
        ->  Sort  (cost=2166.32..2166.60 rows=112 width=64) (actual time=7.013..7.487 rows=23335 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 2592kB
              Buffers: shared hit=23363
              ->  Nested Loop Left Join  (cost=4.58..2162.51 rows=112 width=64) (actual time=0.007..5.156 rows=23335 loops=1)
                    Buffers: shared hit=23363
                    ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=52) (actual time=0.001..0.002 rows=7 loops=1)
                          Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                          Heap Blocks: exact=1
                          Buffers: shared hit=2
                          ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                Buffers: shared hit=1
                    ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions t  (cost=0.42..1070.41 rows=609 width=60) (actual time=0.003..0.514 rows=3334 loops=7)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (category_id = c.id) AND (local_date >= '2024-01-01'::date) AND (local_date <= '2025-12-31'::date))
                          Filter: (kind = 'expense'::text)
                          Buffers: shared hit=23361
Planning Time: 0.071 ms
Execution Time: 8.771 ms
```

### `GET /summary/categories/{id}?period=week`

`GetCategory`

```
Index Scan using categories_pkey on categories  (cost=0.15..8.17 rows=1 width=166) (actual time=0.004..0.004 rows=1 loops=1)
  Index Cond: (id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid)
  Filter: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
  Buffers: shared hit=2
Planning Time: 0.015 ms
Execution Time: 0.011 ms
```

`CategorySpentByDay`

```
GroupAggregate  (cost=0.42..9.46 rows=2 width=16) (actual time=0.010..0.010 rows=1 loops=1)
  Group Key: local_date
  Buffers: shared hit=4
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..9.41 rows=2 width=12) (actual time=0.008..0.009 rows=6 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-21'::date) AND (local_date <= '2025-07-27'::date))
        Filter: (category_id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid)
        Rows Removed by Filter: 22
        Heap Fetches: 0
        Buffers: shared hit=4
Planning Time: 0.032 ms
Execution Time: 0.017 ms
```

### `GET /summary/categories/{id}?period=month`

`GetCategory`

```
Index Scan using categories_pkey on categories  (cost=0.15..8.17 rows=1 width=166) (actual time=0.003..0.003 rows=1 loops=1)
  Index Cond: (id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid)
  Filter: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
  Buffers: shared hit=2
Planning Time: 0.010 ms
Execution Time: 0.007 ms
```

`CategorySpentByDay`

```
GroupAggregate  (cost=0.42..86.44 rows=39 width=16) (actual time=0.007..0.041 rows=21 loops=1)
  Group Key: local_date
  Buffers: shared hit=140
  ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..85.46 rows=40 width=12) (actual time=0.005..0.030 rows=137 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (category_id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
        Filter: (kind = 'expense'::text)
        Buffers: shared hit=140
Planning Time: 0.025 ms
Execution Time: 0.046 ms
```

### `GET /summary/categories/{id}?period=year`

`GetCategory`

```
Index Scan using categories_pkey on categories  (cost=0.15..8.17 rows=1 width=166) (actual time=0.003..0.003 rows=1 loops=1)
  Index Cond: (id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid)
  Filter: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
  Buffers: shared hit=2
Planning Time: 0.014 ms
Execution Time: 0.007 ms
```

`CategorySpentByDay`

```
GroupAggregate  (cost=0.42..689.14 rows=304 width=16) (actual time=0.007..0.278 rows=201 loops=1)
  Group Key: local_date
  Buffers: shared hit=1170
  ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..681.16 rows=355 width=12) (actual time=0.005..0.185 rows=1165 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (category_id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid) AND (local_date >= '2025-01-01'::date) AND (local_date <= '2025-12-31'::date))
        Filter: (kind = 'expense'::text)
        Buffers: shared hit=1170
Planning Time: 0.031 ms
Execution Time: 0.287 ms
```

### `GET /summary/stats?period=week`

`SpentByCategory`

```
Sort  (cost=18.99..18.99 rows=1 width=28) (actual time=0.025..0.026 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=6
  ->  GroupAggregate  (cost=18.95..18.98 rows=1 width=28) (actual time=0.021..0.024 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=6
        ->  Sort  (cost=18.95..18.96 rows=1 width=28) (actual time=0.020..0.021 rows=28 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 26kB
              Buffers: shared hit=6
              ->  Hash Join  (cost=9.95..18.94 rows=1 width=28) (actual time=0.013..0.017 rows=28 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=6
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..9.32 rows=36 width=40) (actual time=0.006..0.008 rows=28 loops=1)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-21'::date) AND (local_date <= '2025-07-27'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=4
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.005..0.005 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.001..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.067 ms
Execution Time: 0.052 ms
```

`SpentBetween`

```
Aggregate  (cost=40.75..40.76 rows=1 width=8) (actual time=0.035..0.035 rows=1 loops=1)
  Buffers: shared hit=7
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..39.90 rows=339 width=8) (actual time=0.006..0.027 rows=287 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-14'::date) AND (local_date <= '2025-07-20'::date))
        Heap Fetches: 0
        Buffers: shared hit=7
Planning Time: 0.022 ms
Execution Time: 0.040 ms
```

`SpentByDay`

```
GroupAggregate  (cost=0.42..10.02 rows=35 width=12) (actual time=0.011..0.011 rows=1 loops=1)
  Group Key: local_date
  Buffers: shared hit=4
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..9.32 rows=36 width=12) (actual time=0.006..0.007 rows=28 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-21'::date) AND (local_date <= '2025-07-27'::date))
        Heap Fetches: 0
        Buffers: shared hit=4
Planning Time: 0.026 ms
Execution Time: 0.017 ms
```

### `GET /summary/stats?period=month`

`SpentByCategory`

```
Sort  (cost=109.47..109.47 rows=2 width=28) (actual time=0.231..0.231 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=15
  ->  GroupAggregate  (cost=109.39..109.46 rows=2 width=28) (actual time=0.191..0.230 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=15
        ->  Sort  (cost=109.39..109.40 rows=5 width=28) (actual time=0.183..0.200 rows=867 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 72kB
              Buffers: shared hit=15
              ->  Hash Join  (cost=9.95..109.33 rows=5 width=28) (actual time=0.014..0.125 rows=867 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=15
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..97.27 rows=954 width=40) (actual time=0.008..0.065 rows=867 loops=1)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=13
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.004..0.004 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.001..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.059 ms
Execution Time: 0.258 ms
```

`SpentBetween`

```
Aggregate  (cost=124.34..124.35 rows=1 width=8) (actual time=0.118..0.118 rows=1 loops=1)
  Buffers: shared hit=18
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..121.27 rows=1224 width=8) (actual time=0.010..0.089 rows=1185 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-06-01'::date) AND (local_date <= '2025-06-30'::date))
        Heap Fetches: 0
        Buffers: shared hit=18
Planning Time: 0.032 ms
Execution Time: 0.124 ms
```

`SpentByDay`

```
GroupAggregate  (cost=0.42..111.63 rows=639 width=12) (actual time=0.012..0.094 rows=21 loops=1)
  Group Key: local_date
  Buffers: shared hit=13
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..97.27 rows=954 width=12) (actual time=0.008..0.061 rows=867 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-07-01'::date) AND (local_date <= '2025-07-31'::date))
        Heap Fetches: 0
        Buffers: shared hit=13
Planning Time: 0.025 ms
Execution Time: 0.100 ms
```

### `GET /summary/stats?period=year`

`SpentByCategory`

```
Sort  (cost=770.78..770.78 rows=2 width=28) (actual time=2.201..2.202 rows=7 loops=1)
  Sort Key: ((sum(t.amount_minor))::bigint) DESC, c.sort_order, c.id
  Sort Method: quicksort  Memory: 25kB
  Buffers: shared hit=109
  ->  GroupAggregate  (cost=770.43..770.77 rows=2 width=28) (actual time=1.794..2.200 rows=7 loops=1)
        Group Key: c.id
        Buffers: shared hit=109
        ->  Sort  (cost=770.43..770.53 rows=41 width=28) (actual time=1.726..1.899 rows=8252 loops=1)
              Sort Key: c.id
              Sort Method: quicksort  Memory: 836kB
              Buffers: shared hit=109
              ->  Hash Join  (cost=9.95..769.33 rows=41 width=28) (actual time=0.014..1.149 rows=8252 loops=1)
                    Hash Cond: (t.category_id = c.id)
                    Buffers: shared hit=109
                    ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions t  (cost=0.42..737.23 rows=8524 width=40) (actual time=0.008..0.542 rows=8252 loops=1)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-01-01'::date) AND (local_date <= '2025-12-31'::date))
                          Heap Fetches: 0
                          Buffers: shared hit=107
                    ->  Hash  (cost=9.50..9.50 rows=2 width=36) (actual time=0.004..0.004 rows=7 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 9kB
                          Buffers: shared hit=2
                          ->  Bitmap Heap Scan on categories c  (cost=4.16..9.50 rows=2 width=36) (actual time=0.001..0.002 rows=7 loops=1)
                                Recheck Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                Heap Blocks: exact=1
                                Buffers: shared hit=2
                                ->  Bitmap Index Scan on categories_user_id_sort_order_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.001..0.001 rows=7 loops=1)
                                      Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
                                      Buffers: shared hit=1
Planning:
  Buffers: shared hit=2
Planning Time: 0.063 ms
Execution Time: 2.228 ms
```

`SpentBetween`

```
Aggregate  (cost=1260.42..1260.43 rows=1 width=8) (actual time=1.701..1.701 rows=1 loops=1)
  Buffers: shared hit=191
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..1222.68 rows=15097 width=8) (actual time=0.008..1.255 rows=15083 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2024-01-01'::date) AND (local_date <= '2024-12-31'::date))
        Heap Fetches: 0
        Buffers: shared hit=191
Planning Time: 0.021 ms
Execution Time: 1.715 ms
```

`SpentByDay`

```
GroupAggregate  (cost=0.42..796.30 rows=1097 width=12) (actual time=0.018..0.866 rows=202 loops=1)
  Group Key: local_date
  Buffers: shared hit=107
  ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..737.23 rows=8524 width=12) (actual time=0.012..0.547 rows=8252 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date >= '2025-01-01'::date) AND (local_date <= '2025-12-31'::date))
        Heap Fetches: 0
        Buffers: shared hit=107
Planning Time: 0.044 ms
Execution Time: 0.880 ms
```

### `GET /transactions`

`ListTransactions`

```
Limit  (cost=0.42..10.59 rows=51 width=194) (actual time=0.005..0.016 rows=51 loops=1)
  Buffers: shared hit=54
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..9992.64 rows=50110 width=194) (actual time=0.005..0.014 rows=51 loops=1)
        Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
        Buffers: shared hit=54
Planning Time: 0.020 ms
Execution Time: 0.021 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=170.91..173.54 rows=86 width=20) (actual time=0.030..0.034 rows=2 loops=1)
  Group Key: local_date
  Buffers: shared hit=86
  ->  Sort  (cost=170.91..171.14 rows=89 width=19) (actual time=0.026..0.028 rows=82 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 28kB
        Buffers: shared hit=86
        ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..168.03 rows=89 width=19) (actual time=0.005..0.022 rows=82 loops=1)
              Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20}'::date[])))
              Buffers: shared hit=86
Planning Time: 0.026 ms
Execution Time: 0.040 ms
```

### `GET /transactions?cursor=`

`ListTransactions`

```
Limit  (cost=0.42..24.39 rows=51 width=194) (actual time=0.005..0.018 rows=51 loops=1)
  Buffers: shared hit=55
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..7711.94 rows=16405 width=194) (actual time=0.005..0.016 rows=51 loops=1)
        Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (ROW(local_date, id) < ROW('2023-07-21'::date, '01a15ea9-a113-7aba-9ec3-31f849d6af6d'::uuid)))
        Buffers: shared hit=55
Planning Time: 0.022 ms
Execution Time: 0.023 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=170.91..173.54 rows=86 width=20) (actual time=0.031..0.034 rows=2 loops=1)
  Group Key: local_date
  Buffers: shared hit=86
  ->  Sort  (cost=170.91..171.14 rows=89 width=19) (actual time=0.027..0.028 rows=82 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 28kB
        Buffers: shared hit=86
        ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..168.03 rows=89 width=19) (actual time=0.005..0.023 rows=82 loops=1)
              Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (local_date = ANY ('{2023-07-21,2023-07-20}'::date[])))
              Buffers: shared hit=86
Planning Time: 0.023 ms
Execution Time: 0.041 ms
```

### `GET /transactions?category_id=&from=&to=`

`ListTransactions`

```
Limit  (cost=2.38..99.15 rows=51 width=194) (actual time=0.020..0.030 rows=51 loops=1)
  Buffers: shared hit=57
  ->  Incremental Sort  (cost=2.38..120.02 rows=62 width=194) (actual time=0.020..0.028 rows=51 loops=1)
        Sort Key: local_date DESC, id DESC
        Presorted Key: local_date
        Full-sort Groups: 2  Sort Method: quicksort  Average Memory: 34kB  Peak Memory: 34kB
        Buffers: shared hit=57
        ->  Index Scan Backward using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..117.30 rows=62 width=194) (actual time=0.007..0.018 rows=54 loops=1)
              Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (category_id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid) AND (local_date >= '2025-06-21'::date) AND (local_date <= '2025-07-21'::date))
              Buffers: shared hit=57
Planning Time: 0.027 ms
Execution Time: 0.038 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=39.52..39.55 rows=1 width=20) (actual time=0.029..0.033 rows=8 loops=1)
  Group Key: local_date
  Buffers: shared hit=77
  ->  Sort  (cost=39.52..39.52 rows=1 width=19) (actual time=0.027..0.028 rows=53 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 27kB
        Buffers: shared hit=77
        ->  Index Scan using transactions_user_id_category_id_local_date_idx on transactions  (cost=0.42..39.51 rows=1 width=19) (actual time=0.007..0.024 rows=53 loops=1)
              Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (category_id = '01a15ea9-9dca-7e57-9018-0adfe926b2d6'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20,2025-07-19,2025-07-18,2025-07-17,2025-07-16,2025-07-15,2025-07-14}'::date[])) AND (local_date >= '2025-06-21'::date) AND (local_date <= '2025-07-21'::date))
              Buffers: shared hit=77
Planning Time: 0.036 ms
Execution Time: 0.040 ms
```

### `GET /transactions?kind=expense`

`ListTransactions`

```
Limit  (cost=0.42..11.83 rows=51 width=194) (actual time=0.005..0.018 rows=51 loops=1)
  Buffers: shared hit=59
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..10117.91 rows=45216 width=194) (actual time=0.005..0.016 rows=51 loops=1)
        Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
        Filter: (kind = 'expense'::text)
        Rows Removed by Filter: 5
        Buffers: shared hit=59
Planning Time: 0.023 ms
Execution Time: 0.026 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=18.97..21.35 rows=78 width=20) (actual time=0.019..0.022 rows=2 loops=1)
  Group Key: local_date
  Buffers: shared hit=7
  ->  Sort  (cost=18.97..19.17 rows=81 width=19) (actual time=0.016..0.017 rows=73 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 28kB
        Buffers: shared hit=7
        ->  Index Only Scan using transactions_user_id_kind_local_date_idx on transactions  (cost=0.42..16.40 rows=81 width=19) (actual time=0.007..0.012 rows=73 loops=1)
              Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (kind = 'expense'::text) AND (local_date = ANY ('{2025-07-21,2025-07-20}'::date[])))
              Heap Fetches: 0
              Buffers: shared hit=7
Planning Time: 0.026 ms
Execution Time: 0.029 ms
```

### `GET /transactions?q=merchant ab`

`ListTransactions`

```
Limit  (cost=0.42..232.07 rows=51 width=194) (actual time=0.007..0.337 rows=51 loops=1)
  Buffers: shared hit=1102
  ->  Index Scan using transactions_user_id_local_date_id_idx on transactions  (cost=0.42..10243.19 rows=2255 width=194) (actual time=0.007..0.335 rows=51 loops=1)
        Index Cond: (user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid)
        Filter: ((merchant ~~* '%merchant ab%'::text) OR (note ~~* '%merchant ab%'::text))
        Rows Removed by Filter: 1054
        Buffers: shared hit=1102
Planning:
  Buffers: shared hit=2
Planning Time: 0.059 ms
Execution Time: 0.343 ms
```

`ListDayTotals`

```
GroupAggregate  (cost=513.62..515.05 rows=47 width=20) (actual time=2.888..2.897 rows=23 loops=1)
  Group Key: local_date
  Buffers: shared hit=404
  ->  Sort  (cost=513.62..513.74 rows=48 width=19) (actual time=2.887..2.888 rows=52 loops=1)
        Sort Key: local_date DESC
        Sort Method: quicksort  Memory: 27kB
        Buffers: shared hit=404
        ->  Bitmap Heap Scan on transactions  (cost=335.53..512.28 rows=48 width=19) (actual time=2.847..2.882 rows=52 loops=1)
              Recheck Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20,2025-07-19,2025-07-18,2025-07-17,2025-07-16,2025-07-14,2025-07-13,2025-07-12,2025-07-11,2025-07-10,2025-07-08,2025-07-07,2025-07-06,2025-07-05,2025-07-04,2025-07-03,2025-07-02,2025-07-01,2025-06-30,2025-06-29,2025-06-28,2025-06-27}'::date[])) AND ((merchant ~~* '%merchant ab%'::text) OR (note ~~* '%merchant ab%'::text)))
              Heap Blocks: exact=51
              Buffers: shared hit=404
              ->  BitmapAnd  (cost=335.47..335.47 rows=48 width=0) (actual time=2.844..2.844 rows=0 loops=1)
                    Buffers: shared hit=353
                    ->  Bitmap Index Scan on transactions_user_id_local_date_id_idx  (cost=0.00..112.24 rows=1058 width=0) (actual time=0.049..0.049 rows=1047 loops=1)
                          Index Cond: ((user_id = '01a15ea9-9dc9-7f3a-ac12-addbb7708dab'::uuid) AND (local_date = ANY ('{2025-07-21,2025-07-20,2025-07-19,2025-07-18,2025-07-17,2025-07-16,2025-07-14,2025-07-13,2025-07-12,2025-07-11,2025-07-10,2025-07-08,2025-07-07,2025-07-06,2025-07-05,2025-07-04,2025-07-03,2025-07-02,2025-07-01,2025-06-30,2025-06-29,2025-06-28,2025-06-27}'::date[])))
                          Buffers: shared hit=70
                    ->  BitmapOr  (cost=222.97..222.97 rows=6750 width=0) (actual time=2.784..2.784 rows=0 loops=1)
                          Buffers: shared hit=283
                          ->  Bitmap Index Scan on transactions_merchant_trgm_idx  (cost=0.00..140.66 rows=6747 width=0) (actual time=2.780..2.780 rows=5757 loops=1)
                                Index Cond: (merchant ~~* '%merchant ab%'::text)
                                Buffers: shared hit=264
                          ->  Bitmap Index Scan on transactions_note_trgm_idx  (cost=0.00..82.28 rows=3 width=0) (actual time=0.004..0.004 rows=0 loops=1)
                                Index Cond: (note ~~* '%merchant ab%'::text)
                                Buffers: shared hit=19
Planning:
  Buffers: shared hit=2
Planning Time: 0.067 ms
Execution Time: 2.922 ms
```
