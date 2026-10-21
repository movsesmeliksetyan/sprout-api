package categorize

import (
	"context"
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

//go:embed data/dictionary.csv data/mcc.csv data/keywords.csv
var seedData embed.FS

// Database is what loading the seeds needs: queries, and a transaction so
// a failed load changes nothing.
type Database interface {
	db.DBTX
	db.Beginner
}

// SeedCounts is what loading one seed file changed.
type SeedCounts struct {
	Added, Updated, Removed int
}

// SeedResult is what SeedRules changed, by table.
type SeedResult struct {
	Dictionary, MCC, Keywords SeedCounts
}

// SeedRules makes the global rule tables match the embedded seed files:
// the seed merchants of the dictionary, the MCC map and the keyword rules.
// Rows that already match are left alone, so a second run changes nothing.
// Dictionary rows that users' votes promoted are never replaced; llm rows
// are, by a seed for the same merchant.
func SeedRules(ctx context.Context, database Database) (SeedResult, error) {
	seeds, err := loadSeeds(seedData)
	if err != nil {
		return SeedResult{}, err
	}
	var result SeedResult
	err = db.WithTx(ctx, database, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		if result.Dictionary, err = seedDictionary(ctx, q, seeds.dictionary); err != nil {
			return err
		}
		if result.MCC, err = seedMCC(ctx, q, seeds.mcc); err != nil {
			return err
		}
		result.Keywords, err = seedKeywords(ctx, q, seeds.keywords)
		return err
	})
	if err != nil {
		return SeedResult{}, fmt.Errorf("categorize: seed rules: %w", err)
	}
	return result, nil
}

func seedDictionary(ctx context.Context, q db.Querier, entries []seedMerchant) (SeedCounts, error) {
	params := db.SeedDictionaryParams{
		Ids:           make([]uuid.UUID, len(entries)),
		MerchantKeys:  make([]string, len(entries)),
		CategoryTypes: make([]string, len(entries)),
		DisplayNames:  make([]string, len(entries)),
	}
	for i, entry := range entries {
		id, err := uuid.NewV7()
		if err != nil {
			return SeedCounts{}, fmt.Errorf("new id: %w", err)
		}
		params.Ids[i] = id
		params.MerchantKeys[i] = entry.key
		params.CategoryTypes[i] = entry.categoryType
		params.DisplayNames[i] = entry.display
	}
	changed, err := q.SeedDictionary(ctx, params)
	if err != nil {
		return SeedCounts{}, fmt.Errorf("dictionary: %w", err)
	}
	removed, err := q.DeleteSeedDictionaryExcept(ctx, params.MerchantKeys)
	if err != nil {
		return SeedCounts{}, fmt.Errorf("dictionary: %w", err)
	}
	return counts(changed, removed), nil
}

func seedMCC(ctx context.Context, q db.Querier, codes []seedMCCCode) (SeedCounts, error) {
	params := db.SeedMCCMapParams{
		Mccs:          make([]string, len(codes)),
		CategoryTypes: make([]string, len(codes)),
	}
	for i, code := range codes {
		params.Mccs[i] = code.mcc
		params.CategoryTypes[i] = code.categoryType
	}
	changed, err := q.SeedMCCMap(ctx, params)
	if err != nil {
		return SeedCounts{}, fmt.Errorf("mcc map: %w", err)
	}
	removed, err := q.DeleteMCCMapExcept(ctx, params.Mccs)
	if err != nil {
		return SeedCounts{}, fmt.Errorf("mcc map: %w", err)
	}
	return counts(changed, removed), nil
}

func seedKeywords(ctx context.Context, q db.Querier, keywords []Keyword) (SeedCounts, error) {
	params := db.SeedKeywordRulesParams{
		Patterns:      make([]string, len(keywords)),
		CategoryTypes: make([]string, len(keywords)),
		Priorities:    make([]int32, len(keywords)),
	}
	for i, keyword := range keywords {
		params.Patterns[i] = keyword.Pattern
		params.CategoryTypes[i] = keyword.CategoryType
		params.Priorities[i] = int32(keyword.Priority)
	}
	changed, err := q.SeedKeywordRules(ctx, params)
	if err != nil {
		return SeedCounts{}, fmt.Errorf("keyword rules: %w", err)
	}
	removed, err := q.DeleteKeywordRulesExcept(ctx, params.Patterns)
	if err != nil {
		return SeedCounts{}, fmt.Errorf("keyword rules: %w", err)
	}
	return counts(changed, removed), nil
}

// counts tallies the rows an upsert returned, one for each it added or
// changed, and the rows a delete removed.
func counts(changed []bool, removed int64) SeedCounts {
	c := SeedCounts{Removed: int(removed)}
	for _, inserted := range changed {
		if inserted {
			c.Added++
		} else {
			c.Updated++
		}
	}
	return c
}

// seeds are the parsed seed files.
type seeds struct {
	dictionary []seedMerchant
	mcc        []seedMCCCode
	keywords   []Keyword
}

type seedMerchant struct {
	key, display, categoryType string
}

type seedMCCCode struct {
	mcc, categoryType string
}

// loadSeeds parses and checks the seed files. A merchant's key must be
// what Normalize makes of its display name, so a statement row that names
// the merchant finds it; keys, codes and patterns must each be unique.
func loadSeeds(files fs.FS) (seeds, error) {
	var s seeds
	var err error
	if s.dictionary, err = loadDictionary(files); err != nil {
		return seeds{}, err
	}
	if s.mcc, err = loadMCC(files); err != nil {
		return seeds{}, err
	}
	if s.keywords, err = loadKeywords(files); err != nil {
		return seeds{}, err
	}
	return s, nil
}

func loadDictionary(files fs.FS) ([]seedMerchant, error) {
	const name = "data/dictionary.csv"
	records, err := readCSV(files, name, []string{"merchant_key", "display_name", "category_type"})
	if err != nil {
		return nil, err
	}
	entries := make([]seedMerchant, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, r := range records {
		entry := seedMerchant{key: r.fields[0], display: r.fields[1], categoryType: r.fields[2]}
		if got := Normalize(entry.display).Key; got != entry.key {
			return nil, fmt.Errorf("categorize: %s:%d: %q normalises to %q, not %q", name, r.line, entry.display, got, entry.key)
		}
		if seen[entry.key] {
			return nil, fmt.Errorf("categorize: %s:%d: merchant %q is listed twice", name, r.line, entry.key)
		}
		if entry.categoryType == "" {
			return nil, fmt.Errorf("categorize: %s:%d: %q has no category type", name, r.line, entry.key)
		}
		seen[entry.key] = true
		entries = append(entries, entry)
	}
	return entries, nil
}

func loadMCC(files fs.FS) ([]seedMCCCode, error) {
	const name = "data/mcc.csv"
	records, err := readCSV(files, name, []string{"from", "to", "category_type", "description"})
	if err != nil {
		return nil, err
	}
	var codes []seedMCCCode
	seen := make(map[int]bool)
	for _, r := range records {
		from, errFrom := parseMCC(r.fields[0])
		to, errTo := parseMCC(r.fields[1])
		if err := errors.Join(errFrom, errTo); err != nil {
			return nil, fmt.Errorf("categorize: %s:%d: %w", name, r.line, err)
		}
		if from > to {
			return nil, fmt.Errorf("categorize: %s:%d: range %04d–%04d runs backwards", name, r.line, from, to)
		}
		if r.fields[2] == "" {
			return nil, fmt.Errorf("categorize: %s:%d: no category type", name, r.line)
		}
		for code := from; code <= to; code++ {
			if seen[code] {
				return nil, fmt.Errorf("categorize: %s:%d: code %04d is in two ranges", name, r.line, code)
			}
			seen[code] = true
			codes = append(codes, seedMCCCode{mcc: fmt.Sprintf("%04d", code), categoryType: r.fields[2]})
		}
	}
	return codes, nil
}

// parseMCC reads a four-digit merchant category code.
func parseMCC(field string) (int, error) {
	code, err := strconv.Atoi(field)
	if err != nil || len(field) != 4 {
		return 0, fmt.Errorf("%q is not a four-digit code", field)
	}
	return code, nil
}

func loadKeywords(files fs.FS) ([]Keyword, error) {
	const name = "data/keywords.csv"
	records, err := readCSV(files, name, []string{"pattern", "category_type", "priority"})
	if err != nil {
		return nil, err
	}
	keywords := make([]Keyword, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, r := range records {
		pattern := r.fields[0]
		if !validPattern(pattern) {
			return nil, fmt.Errorf("categorize: %s:%d: pattern %q is not in key form", name, r.line, pattern)
		}
		if seen[pattern] {
			return nil, fmt.Errorf("categorize: %s:%d: pattern %q is listed twice", name, r.line, pattern)
		}
		priority, err := strconv.Atoi(r.fields[2])
		if err != nil {
			return nil, fmt.Errorf("categorize: %s:%d: priority %q is not a number", name, r.line, r.fields[2])
		}
		if r.fields[1] == "" {
			return nil, fmt.Errorf("categorize: %s:%d: no category type", name, r.line)
		}
		seen[pattern] = true
		keywords = append(keywords, Keyword{Pattern: pattern, CategoryType: r.fields[1], Priority: priority})
	}
	return keywords, nil
}

// validPattern reports whether a pattern is words in key form, each of
// which may end in one *.
func validPattern(pattern string) bool {
	words := strings.Split(pattern, " ")
	for i, word := range words {
		words[i] = strings.TrimSuffix(word, "*")
		if words[i] == "" {
			return false
		}
	}
	return key(words) == strings.Join(words, " ")
}

// record is a CSV row and the line it starts on.
type record struct {
	line   int
	fields []string
}

// readCSV reads a seed file: lines starting with # are comments, blank lines
// are skipped and the first row must be header.
func readCSV(files fs.FS, name string, header []string) ([]record, error) {
	f, err := files.Open(name)
	if err != nil {
		return nil, fmt.Errorf("categorize: %w", err)
	}
	defer func() { _ = f.Close() }()

	reader := csv.NewReader(f)
	reader.Comment = '#'
	reader.FieldsPerRecord = len(header)
	got, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("categorize: %s: %w", name, err)
	}
	if strings.Join(got, ",") != strings.Join(header, ",") {
		return nil, fmt.Errorf("categorize: %s: header is %q, want %q", name, got, header)
	}
	var records []record
	for {
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return records, nil
		}
		if err != nil {
			return nil, fmt.Errorf("categorize: %s: %w", name, err)
		}
		line, _ := reader.FieldPos(0)
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		records = append(records, record{line: line, fields: fields})
	}
}
