-- name: ListMerchantRules :many
-- The user's rules for any of the keys whose category is still active.
SELECT r.merchant_key, r.category_id
FROM merchant_rules r
JOIN categories c ON c.id = r.category_id AND c.archived_at IS NULL
WHERE r.user_id = @user_id AND r.merchant_key = ANY(@merchant_keys::text[]);

-- name: UpsertMerchantRule :execrows
-- Points the user's rule for a merchant at one of their active categories,
-- counting the use. Writes nothing when the category is not theirs.
INSERT INTO merchant_rules (id, user_id, merchant_key, category_id, hits, last_used_at)
SELECT @id, @user_id, @merchant_key, c.id, 1, @used_at
FROM categories c
WHERE c.id = @category_id AND c.user_id = @user_id AND c.archived_at IS NULL
ON CONFLICT (user_id, merchant_key) DO UPDATE
SET category_id  = EXCLUDED.category_id,
    hits         = merchant_rules.hits + 1,
    last_used_at = EXCLUDED.last_used_at;

-- name: ListDictionaryEntries :many
-- The dictionary rows for any of the keys, leaving out llm rows that have
-- expired.
SELECT merchant_key, category_type, display_name, source, votes, confidence
FROM merchant_dictionary
WHERE merchant_key = ANY(@merchant_keys::text[])
  AND (expires_at IS NULL OR expires_at > @now::timestamptz);

-- name: SaveLLMDictionaryEntries :exec
-- Stores what the model said about merchants. An llm row replaces an llm
-- row only, never a seed or learned one. An empty category_type is a
-- merchant the model did not know; an expires_at of the zero time (year 1)
-- is a row that does not expire.
INSERT INTO merchant_dictionary (id, merchant_key, category_type, display_name, source, confidence, model, expires_at)
SELECT e.id, e.merchant_key, NULLIF(e.category_type, ''), e.display_name, 'llm', e.confidence, e.model,
       NULLIF(e.expires_at, '0001-01-01 00:00:00+00'::timestamptz)
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@merchant_keys::text[]) AS merchant_key,
             unnest(@category_types::text[]) AS category_type, unnest(@display_names::text[]) AS display_name,
             unnest(@confidences::float8[]) AS confidence, unnest(@models::text[]) AS model,
             unnest(@expires_at::timestamptz[]) AS expires_at) AS e
ON CONFLICT (merchant_key) DO UPDATE
SET category_type = EXCLUDED.category_type,
    display_name  = EXCLUDED.display_name,
    confidence    = EXCLUDED.confidence,
    model         = EXCLUDED.model,
    expires_at    = EXCLUDED.expires_at
WHERE merchant_dictionary.source = 'llm';

-- name: SeedDictionary :many
-- Writes the seed merchants. A seed row replaces a seed or llm row, never a
-- learned one, and an unchanged row is left alone. Returns a row for every
-- merchant added or changed.
INSERT INTO merchant_dictionary (id, merchant_key, category_type, display_name, source)
SELECT e.id, e.merchant_key, e.category_type, e.display_name, 'seed'
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@merchant_keys::text[]) AS merchant_key,
             unnest(@category_types::text[]) AS category_type, unnest(@display_names::text[]) AS display_name) AS e
ON CONFLICT (merchant_key) DO UPDATE
SET category_type = EXCLUDED.category_type,
    display_name  = EXCLUDED.display_name,
    source        = 'seed',
    confidence    = NULL,
    model         = NULL,
    expires_at    = NULL
WHERE merchant_dictionary.source = 'llm'
   OR (merchant_dictionary.source = 'seed'
       AND (merchant_dictionary.category_type, merchant_dictionary.display_name)
           IS DISTINCT FROM (EXCLUDED.category_type, EXCLUDED.display_name))
RETURNING (xmax = 0)::boolean AS inserted;

-- name: DeleteSeedDictionaryExcept :execrows
-- Removes the seed merchants the seed file no longer has.
DELETE FROM merchant_dictionary
WHERE source = 'seed' AND NOT merchant_key = ANY(@merchant_keys::text[]);

-- name: ListMCCTypes :many
SELECT mcc, category_type FROM mcc_map WHERE mcc = ANY(@mccs::text[]);

-- name: SeedMCCMap :many
-- Writes the seed codes, leaving unchanged ones alone. Returns a row for
-- every code added or changed.
INSERT INTO mcc_map (mcc, category_type)
SELECT unnest(@mccs::text[]), unnest(@category_types::text[])
ON CONFLICT (mcc) DO UPDATE
SET category_type = EXCLUDED.category_type
WHERE mcc_map.category_type <> EXCLUDED.category_type
RETURNING (xmax = 0)::boolean AS inserted;

-- name: DeleteMCCMapExcept :execrows
DELETE FROM mcc_map WHERE NOT mcc = ANY(@mccs::text[]);

-- name: ListKeywordRules :many
-- Every keyword rule, in the order they are tried.
SELECT pattern, category_type, priority FROM keyword_rules
ORDER BY priority DESC, length(pattern) DESC, pattern;

-- name: SeedKeywordRules :many
-- Writes the seed patterns, leaving unchanged ones alone. Returns a row for
-- every pattern added or changed.
INSERT INTO keyword_rules (pattern, category_type, priority)
SELECT unnest(@patterns::text[]), unnest(@category_types::text[]), unnest(@priorities::integer[])
ON CONFLICT (pattern) DO UPDATE
SET category_type = EXCLUDED.category_type,
    priority      = EXCLUDED.priority
WHERE (keyword_rules.category_type, keyword_rules.priority)
      IS DISTINCT FROM (EXCLUDED.category_type, EXCLUDED.priority)
RETURNING (xmax = 0)::boolean AS inserted;

-- name: DeleteKeywordRulesExcept :execrows
DELETE FROM keyword_rules WHERE NOT pattern = ANY(@patterns::text[]);

-- name: ListTypedCategories :many
-- The user's active categories that have a type, each with how many
-- transactions it holds, in display order.
SELECT c.id, c.category_type::text AS category_type,
       (SELECT count(*) FROM transactions t WHERE t.user_id = c.user_id AND t.category_id = c.id)::bigint AS transactions
FROM categories c
WHERE c.user_id = @user_id AND c.archived_at IS NULL AND c.category_type IS NOT NULL
ORDER BY c.sort_order, c.id;

-- name: ListExpenseMerchantKeys :many
-- Which of the keys the user has spent money with before.
SELECT DISTINCT merchant_key FROM transactions
WHERE user_id = @user_id AND kind = 'expense' AND merchant_key = ANY(@merchant_keys::text[]);

-- name: RecordMerchantVotes :execrows
-- Records the user's votes: each merchant's category, by its type. A
-- category without a type, or not the user's, casts no vote.
INSERT INTO merchant_votes (id, user_id, merchant_key, category_type, display_name, voted_at)
SELECT DISTINCT ON (v.merchant_key) v.id, @user_id, v.merchant_key, c.category_type, v.display_name, @voted_at
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@merchant_keys::text[]) AS merchant_key,
             unnest(@display_names::text[]) AS display_name, unnest(@category_ids::uuid[]) AS category_id) AS v
JOIN categories c ON c.id = v.category_id AND c.user_id = @user_id AND c.category_type IS NOT NULL
WHERE v.merchant_key <> ''
ORDER BY v.merchant_key
ON CONFLICT (user_id, merchant_key) DO UPDATE
SET category_type = EXCLUDED.category_type,
    display_name  = EXCLUDED.display_name,
    voted_at      = EXCLUDED.voted_at;

-- name: DeleteExpiredLLMEntries :execrows
DELETE FROM merchant_dictionary WHERE source = 'llm' AND expires_at <= @now::timestamptz;

-- name: DemoteDictionaryEntries :execrows
-- Removes learned and llm rows that too few voters agree with: under 60 %
-- of at least @min_voters votes for the key.
DELETE FROM merchant_dictionary d
USING (SELECT merchant_key, count(*) AS total FROM merchant_votes GROUP BY merchant_key) t
WHERE d.merchant_key = t.merchant_key
  AND d.source IN ('learned', 'llm')
  AND t.total >= @min_voters::bigint
  AND (SELECT count(*) FROM merchant_votes v
       WHERE v.merchant_key = d.merchant_key AND v.category_type = d.category_type) * 100 < t.total * 60;

-- name: ListPromotionCandidates :many
-- The merchants whose most voted type has at least @min_voters users and
-- 80 % of the votes, with that type and its votes.
SELECT b.merchant_key, b.category_type, b.votes::integer AS votes, b.display_name::text AS display_name
FROM (
    SELECT DISTINCT ON (merchant_key) merchant_key, category_type, n AS votes, display_name,
           sum(n) OVER (PARTITION BY merchant_key) AS total
    FROM (SELECT merchant_key, category_type, count(*) AS n, max(display_name) AS display_name
          FROM merchant_votes GROUP BY merchant_key, category_type) tally
    ORDER BY merchant_key, n DESC, category_type
) b
WHERE b.votes >= @min_voters::bigint AND b.votes * 100 >= b.total * 80;

-- name: PromoteDictionaryEntries :execrows
-- Writes promoted merchants as learned rows, replacing llm and learned
-- rows; an unchanged learned row is left alone. A seed row that agrees
-- takes the vote count; one that does not is left alone.
INSERT INTO merchant_dictionary (id, merchant_key, category_type, display_name, source, votes)
SELECT unnest(@ids::uuid[]), unnest(@merchant_keys::text[]), unnest(@category_types::text[]),
       unnest(@display_names::text[]), 'learned', unnest(@votes::integer[])
ON CONFLICT (merchant_key) DO UPDATE
SET source        = CASE WHEN merchant_dictionary.source = 'seed' THEN 'seed' ELSE 'learned' END,
    category_type = EXCLUDED.category_type,
    display_name  = CASE WHEN merchant_dictionary.source = 'seed' THEN merchant_dictionary.display_name
                         ELSE EXCLUDED.display_name END,
    votes         = EXCLUDED.votes,
    confidence    = NULL,
    model         = NULL,
    expires_at    = NULL
WHERE merchant_dictionary.source = 'llm'
   OR (merchant_dictionary.source = 'learned'
       AND (merchant_dictionary.category_type, merchant_dictionary.display_name, merchant_dictionary.votes)
           IS DISTINCT FROM (EXCLUDED.category_type, EXCLUDED.display_name, EXCLUDED.votes))
   OR (merchant_dictionary.source = 'seed' AND merchant_dictionary.category_type = EXCLUDED.category_type
       AND merchant_dictionary.votes <> EXCLUDED.votes);
