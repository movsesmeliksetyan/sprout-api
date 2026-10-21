package categorize

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// ErrCategoryNotFound is a rule for a category that is not one of the
// user's active categories.
var ErrCategoryNotFound = errors.New("categorize: category not found")

// Where a dictionary row came from (PRD §3).
const (
	DictionarySeed    = "seed"
	DictionaryLearned = "learned"
	DictionaryLLM     = "llm"
)

// Store reads and writes the rules the layers of PRD §5.2 consult. Every
// lookup takes all of a batch's inputs and makes one query.
type Store struct {
	q db.Querier
}

// NewStore returns a store that queries through q.
func NewStore(q db.Querier) *Store {
	return &Store{q: q}
}

// UserRules returns the user's rules for any of keys, by key: the category
// each points at. A rule whose category is archived is left out.
func (s *Store) UserRules(ctx context.Context, userID uuid.UUID, keys []string) (map[string]uuid.UUID, error) {
	rows, err := s.q.ListMerchantRules(ctx, db.ListMerchantRulesParams{UserID: userID, MerchantKeys: keys})
	if err != nil {
		return nil, fmt.Errorf("categorize: user rules: %w", err)
	}
	rules := make(map[string]uuid.UUID, len(rows))
	for _, row := range rows {
		rules[row.MerchantKey] = row.CategoryID
	}
	return rules, nil
}

// SaveUserRule points the user's rule for key at categoryID, which must be
// one of their active categories, and counts the use.
func (s *Store) SaveUserRule(ctx context.Context, userID uuid.UUID, key string, categoryID uuid.UUID, at time.Time) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("categorize: new id: %w", err)
	}
	saved, err := s.q.UpsertMerchantRule(ctx, db.UpsertMerchantRuleParams{
		ID:          id,
		UserID:      userID,
		MerchantKey: key,
		CategoryID:  categoryID,
		UsedAt:      at,
	})
	if err != nil {
		return fmt.Errorf("categorize: save user rule: %w", err)
	}
	if saved == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

// DictionaryEntry is what the global dictionary knows about a merchant.
type DictionaryEntry struct {
	// Key is the dictionary's key, which is the input key or the longest
	// prefix of it that the dictionary has.
	Key string
	// CategoryType is nil on an llm row for a merchant the model did not
	// know.
	CategoryType *string
	DisplayName  string
	// Source is DictionarySeed, DictionaryLearned or DictionaryLLM.
	Source string
	Votes  int
	// Confidence is what the model said, on an llm row only.
	Confidence *float64
}

// Dictionary returns the entry for each of keys that the dictionary
// answers, by input key: the entry for the key itself or, failing that, for
// its longest prefix of whole words (`tesco petrol` is answered by
// `tesco`). An llm row that has expired by now answers nothing.
func (s *Store) Dictionary(ctx context.Context, keys []string, now time.Time) (map[string]DictionaryEntry, error) {
	var candidates []string
	seen := make(map[string]bool)
	for _, key := range keys {
		for _, prefix := range prefixes(key) {
			if !seen[prefix] {
				seen[prefix] = true
				candidates = append(candidates, prefix)
			}
		}
	}
	if len(candidates) == 0 {
		return map[string]DictionaryEntry{}, nil
	}

	rows, err := s.q.ListDictionaryEntries(ctx, db.ListDictionaryEntriesParams{MerchantKeys: candidates, Now: now})
	if err != nil {
		return nil, fmt.Errorf("categorize: dictionary: %w", err)
	}
	found := make(map[string]DictionaryEntry, len(rows))
	for _, row := range rows {
		found[row.MerchantKey] = DictionaryEntry{
			Key:          row.MerchantKey,
			CategoryType: row.CategoryType,
			DisplayName:  row.DisplayName,
			Source:       row.Source,
			Votes:        int(row.Votes),
			Confidence:   row.Confidence,
		}
	}

	entries := make(map[string]DictionaryEntry, len(keys))
	for _, key := range keys {
		candidates := prefixes(key)
		for i := len(candidates) - 1; i >= 0; i-- {
			if entry, ok := found[candidates[i]]; ok {
				entries[key] = entry
				break
			}
		}
	}
	return entries, nil
}

// prefixes are key's prefixes of whole words, shortest first, ending with
// key itself.
func prefixes(key string) []string {
	if key == "" {
		return nil
	}
	var out []string
	for i := range len(key) {
		if key[i] == ' ' {
			out = append(out, key[:i])
		}
	}
	return append(out, key)
}

// LLMEntry is what the model said about a merchant, to be kept so it is not
// asked again (PRD §5.2).
type LLMEntry struct {
	Key string
	// CategoryType is empty for a merchant the model did not know.
	CategoryType string
	DisplayName  string
	Confidence   float64
	Model        string
	// ExpiresAt is when the model may be asked again; the zero time is
	// never.
	ExpiresAt time.Time
}

// SaveLLMEntries stores the model's answers in one query. An answer
// replaces an earlier llm row for its key, never a seed or learned one.
func (s *Store) SaveLLMEntries(ctx context.Context, entries []LLMEntry) error {
	if len(entries) == 0 {
		return nil
	}
	params := db.SaveLLMDictionaryEntriesParams{
		Ids:           make([]uuid.UUID, len(entries)),
		MerchantKeys:  make([]string, len(entries)),
		CategoryTypes: make([]string, len(entries)),
		DisplayNames:  make([]string, len(entries)),
		Confidences:   make([]float64, len(entries)),
		Models:        make([]string, len(entries)),
		ExpiresAt:     make([]time.Time, len(entries)),
	}
	for i, entry := range entries {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("categorize: new id: %w", err)
		}
		params.Ids[i] = id
		params.MerchantKeys[i] = entry.Key
		params.CategoryTypes[i] = entry.CategoryType
		params.DisplayNames[i] = entry.DisplayName
		params.Confidences[i] = entry.Confidence
		params.Models[i] = entry.Model
		params.ExpiresAt[i] = entry.ExpiresAt
	}
	if err := s.q.SaveLLMDictionaryEntries(ctx, params); err != nil {
		return fmt.Errorf("categorize: save llm entries: %w", err)
	}
	return nil
}

// MCCTypes returns the category type of each of mccs that has one, by code.
func (s *Store) MCCTypes(ctx context.Context, mccs []string) (map[string]string, error) {
	rows, err := s.q.ListMCCTypes(ctx, mccs)
	if err != nil {
		return nil, fmt.Errorf("categorize: mcc types: %w", err)
	}
	types := make(map[string]string, len(rows))
	for _, row := range rows {
		types[row.Mcc] = row.CategoryType
	}
	return types, nil
}

// Keyword is a pattern whose words in a description give away its
// category type.
type Keyword struct {
	// Pattern is one or more words in key form; a word ending in * matches
	// every word that starts with it.
	Pattern      string
	CategoryType string
	// Priority orders the patterns: higher is tried first.
	Priority int
}

// Keywords returns every keyword rule in the order they are tried. There
// are a few hundred, so one query answers a whole batch.
func (s *Store) Keywords(ctx context.Context) ([]Keyword, error) {
	rows, err := s.q.ListKeywordRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("categorize: keywords: %w", err)
	}
	keywords := make([]Keyword, len(rows))
	for i, row := range rows {
		keywords[i] = Keyword{Pattern: row.Pattern, CategoryType: row.CategoryType, Priority: int(row.Priority)}
	}
	return keywords, nil
}

// Matches reports whether the pattern's words appear in key, in order and
// next to each other.
func (k Keyword) Matches(key string) bool {
	pattern := strings.Fields(k.Pattern)
	words := strings.Fields(key)
	for start := 0; start+len(pattern) <= len(words); start++ {
		matched := true
		for i, want := range pattern {
			if !wordMatches(want, words[start+i]) {
				matched = false
				break
			}
		}
		if matched && len(pattern) > 0 {
			return true
		}
	}
	return false
}

func wordMatches(pattern, word string) bool {
	if stem, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(word, stem)
	}
	return pattern == word
}
