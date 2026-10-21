-- name: GetMappingTemplate :one
-- The template for a header fingerprint, unless it has been disabled: a
-- success rate under 80 % over at least 5 uses.
SELECT * FROM mapping_templates
WHERE header_fingerprint = @header_fingerprint
  AND NOT (use_count >= 5 AND success_count * 100 < use_count * 80);

-- name: SaveMappingTemplate :exec
-- Stores a validated spec for a fingerprint. A user's spec replaces any
-- other; any other replaces neither a user's nor a seed's, and counters
-- start over when the spec changes.
INSERT INTO mapping_templates (id, header_fingerprint, format, bank_hint, spec, source)
VALUES (@id, @header_fingerprint, @format, @bank_hint, @spec, @source)
ON CONFLICT (header_fingerprint) DO UPDATE
SET format        = EXCLUDED.format,
    bank_hint     = COALESCE(EXCLUDED.bank_hint, mapping_templates.bank_hint),
    spec          = EXCLUDED.spec,
    source        = EXCLUDED.source,
    use_count     = 0,
    success_count = 0
WHERE EXCLUDED.source = 'user'
   OR mapping_templates.source NOT IN ('user', 'seed')
   OR (mapping_templates.use_count >= 5 AND mapping_templates.success_count * 100 < mapping_templates.use_count * 80);

-- name: RecordMappingTemplateUse :exec
-- Counts an import read with a template, and whether it went well.
UPDATE mapping_templates
SET use_count     = use_count + 1,
    success_count = success_count + CASE WHEN @succeeded::boolean THEN 1 ELSE 0 END
WHERE header_fingerprint = @header_fingerprint;
