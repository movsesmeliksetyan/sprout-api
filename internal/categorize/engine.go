package categorize

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// Source is the layer that categorised a transaction (contract §1.3,
// categorization.source).
type Source string

// The layers of PRD §5.2, and none for a transaction no layer placed.
const (
	SourceUserRule   Source = "user_rule"
	SourceDictionary Source = "dictionary"
	SourceMCC        Source = "mcc"
	SourceKeyword    Source = "keyword"
	SourceLLM        Source = "llm"
	SourceNone       Source = "none"
)

// Threshold is the confidence a layer's answer must reach to be used.
const Threshold = 0.60

// The layers' confidences (PRD §5.2), in percent: they are worked out in
// whole numbers and divided once, so 0.80 is 0.80.
const (
	userRuleConfidence = 99
	// A seed or learned dictionary row starts at dictionaryConfidence and
	// gains a point a vote, up to dictionaryMaxConfidence.
	dictionaryConfidence    = 90
	dictionaryMaxConfidence = 97
	// llmMaxConfidence caps what the model says of itself.
	llmMaxConfidence = 0.90
	mccConfidence    = 85
	// A keyword's confidence runs from keywordMinConfidence at priority
	// keywordMinPriority to keywordMaxConfidence at keywordMaxPriority.
	keywordMinConfidence = 70
	keywordMaxConfidence = 80
	keywordMinPriority   = 10
	keywordMaxPriority   = 30
)

// percent is a confidence in percent as a fraction.
func percent(p int) float64 { return float64(p) / 100 }

// Direction is which way money moved.
type Direction string

// The directions. Only debits are categorised: income has no category
// (contract §2.5).
const (
	DirectionDebit  Direction = "debit"
	DirectionCredit Direction = "credit"
)

// Input is one transaction to categorise.
type Input struct {
	// RawDescription is the statement's text; Merchant is a merchant name
	// already known, typed or read from a receipt. The merchant is read
	// from Merchant when there is one.
	RawDescription string
	Merchant       string
	// MCC is the four-digit merchant category code, when the file has one.
	MCC         string
	AmountMinor int64
	Direction   Direction
}

// Result is where a transaction belongs.
type Result struct {
	// CategoryID is the user's category; nil for Source none.
	CategoryID *uuid.UUID
	Confidence float64
	Source     Source
	// MerchantKey and DisplayMerchant are the normalised merchant (§5.1);
	// both empty when the input names none.
	MerchantKey     string
	DisplayMerchant string
}

// Options choose the layers a classification may use.
type Options struct {
	// AllowLLM lets unresolved transactions go to the language model. It has
	// no effect until the LLM layer exists (BE-32).
	AllowLLM bool
}

// Engine categorises transactions through the layers of PRD §5.2, cheapest
// first; the first answer that reaches Threshold wins.
type Engine struct {
	q     db.Querier
	store *Store
	now   func() time.Time
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithClock replaces time.Now, which decides when model answers expire.
func WithClock(now func() time.Time) EngineOption {
	return func(e *Engine) { e.now = now }
}

// NewEngine returns an engine that reads through q.
func NewEngine(q db.Querier, opts ...EngineOption) *Engine {
	e := &Engine{q: q, store: NewStore(q), now: time.Now}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Classify categorises a batch for a user, one result per input in the
// same order. Each layer makes at most one query whatever the batch size,
// and a layer is skipped once nothing is left for it.
func (e *Engine) Classify(ctx context.Context, userID uuid.UUID, in []Input, _ Options) ([]Result, error) {
	c := classification{results: make([]Result, len(in))}
	for i, input := range in {
		name := input.Merchant
		if strings.TrimSpace(name) == "" {
			name = input.RawDescription
		}
		merchant := Normalize(name)
		c.results[i] = Result{Source: SourceNone, MerchantKey: merchant.Key, DisplayMerchant: merchant.Display}
		if input.Direction != DirectionCredit {
			c.pending = append(c.pending, i)
		}
	}

	if err := c.userRules(ctx, e.store, userID); err != nil {
		return nil, err
	}
	if len(c.pending) == 0 {
		return c.results, nil
	}
	types, err := newResolver(ctx, e.q, userID)
	if err != nil {
		return nil, err
	}
	if err := c.dictionary(ctx, e.store, types, e.now()); err != nil {
		return nil, err
	}
	if err := c.mcc(ctx, e.store, types, in); err != nil {
		return nil, err
	}
	if err := c.keywords(ctx, e.store, types, in); err != nil {
		return nil, err
	}
	return c.results, nil
}

// classification is a batch on its way through the layers.
type classification struct {
	results []Result
	// pending are the indexes of the inputs no layer has placed yet.
	pending []int
}

// place gives the input at i its category, if the answer reaches the
// threshold, and reports whether it did.
func (c *classification) place(i int, categoryID uuid.UUID, confidence float64, source Source) bool {
	if confidence < Threshold {
		return false
	}
	c.results[i].CategoryID = &categoryID
	c.results[i].Confidence = confidence
	c.results[i].Source = source
	return true
}

// keep runs placed over the pending inputs and keeps those it did not place.
func (c *classification) keep(placed func(i int) bool) {
	left := c.pending[:0]
	for _, i := range c.pending {
		if !placed(i) {
			left = append(left, i)
		}
	}
	c.pending = left
}

// keys are the distinct non-empty merchant keys of the pending inputs.
func (c *classification) keys() []string {
	var keys []string
	seen := make(map[string]bool)
	for _, i := range c.pending {
		key := c.results[i].MerchantKey
		if key != "" && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

// userRules is layer 1: the user's own rule for the merchant.
func (c *classification) userRules(ctx context.Context, store *Store, userID uuid.UUID) error {
	keys := c.keys()
	if len(keys) == 0 {
		return nil
	}
	rules, err := store.UserRules(ctx, userID, keys)
	if err != nil {
		return err
	}
	c.keep(func(i int) bool {
		categoryID, ok := rules[c.results[i].MerchantKey]
		return ok && c.place(i, categoryID, percent(userRuleConfidence), SourceUserRule)
	})
	return nil
}

// dictionary is layer 2: what is known about the merchant for every user.
func (c *classification) dictionary(ctx context.Context, store *Store, types resolver, now time.Time) error {
	keys := c.keys()
	if len(keys) == 0 {
		return nil
	}
	entries, err := store.Dictionary(ctx, keys, now)
	if err != nil {
		return err
	}
	c.keep(func(i int) bool {
		entry, ok := entries[c.results[i].MerchantKey]
		if !ok || entry.CategoryType == nil {
			return false
		}
		categoryID, ok := types.resolve(*entry.CategoryType)
		if !ok {
			return false
		}
		confidence, source := dictionaryConfidenceOf(entry)
		if !c.place(i, categoryID, confidence, source) {
			return false
		}
		if entry.Key == c.results[i].MerchantKey {
			c.results[i].DisplayMerchant = entry.DisplayName
		}
		return true
	})
	return nil
}

// dictionaryConfidenceOf is how sure a dictionary row is, and the source a
// result from it carries.
func dictionaryConfidenceOf(entry DictionaryEntry) (float64, Source) {
	if entry.Source == DictionaryLLM {
		confidence := 0.0
		if entry.Confidence != nil {
			confidence = min(*entry.Confidence, llmMaxConfidence)
		}
		return confidence, SourceLLM
	}
	return percent(min(dictionaryConfidence+entry.Votes, dictionaryMaxConfidence)), SourceDictionary
}

// mcc is layer 3: the merchant category code, when the input has one.
func (c *classification) mcc(ctx context.Context, store *Store, types resolver, in []Input) error {
	var codes []string
	seen := make(map[string]bool)
	for _, i := range c.pending {
		if code := in[i].MCC; code != "" && !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		return nil
	}
	mccTypes, err := store.MCCTypes(ctx, codes)
	if err != nil {
		return err
	}
	c.keep(func(i int) bool {
		categoryType, ok := mccTypes[in[i].MCC]
		if !ok {
			return false
		}
		categoryID, ok := types.resolve(categoryType)
		return ok && c.place(i, categoryID, percent(mccConfidence), SourceMCC)
	})
	return nil
}

// keywords is layer 4: words in the description that give away its type.
// Patterns are tried highest priority first; one whose type the user has
// no category of is passed over.
func (c *classification) keywords(ctx context.Context, store *Store, types resolver, in []Input) error {
	if len(c.pending) == 0 {
		return nil
	}
	keywords, err := store.Keywords(ctx)
	if err != nil {
		return err
	}
	c.keep(func(i int) bool {
		text := keywordText(in[i].RawDescription + " " + in[i].Merchant)
		if text == "" {
			return false
		}
		for _, keyword := range keywords {
			if !keyword.Matches(text) {
				continue
			}
			if categoryID, ok := types.resolve(keyword.CategoryType); ok {
				return c.place(i, categoryID, keywordConfidence(keyword.Priority), SourceKeyword)
			}
		}
		return false
	})
	return nil
}

// keywordConfidence maps a pattern's priority onto the keyword layer's
// confidence range.
func keywordConfidence(priority int) float64 {
	priority = min(max(priority, keywordMinPriority), keywordMaxPriority)
	return percent(keywordMinConfidence + (priority-keywordMinPriority)*(keywordMaxConfidence-keywordMinConfidence)/
		(keywordMaxPriority-keywordMinPriority))
}

// keywordText is a description as keywords see it: lower case, words of
// letters, digits and &, one space apart. Unlike the merchant key it keeps
// every word, prefixes and places included.
func keywordText(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '&'
	}), " ")
}
