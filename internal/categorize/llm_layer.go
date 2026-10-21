package categorize

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

const (
	// llmBatch is how many merchants one call asks about.
	llmBatch = 50
	// unknownMerchantTTL is how long a merchant the model could not place
	// is not asked about again.
	unknownMerchantTTL = 30 * 24 * time.Hour
	// minSavedKey is the shortest key worth keeping for every user.
	minSavedKey = 3
)

// llmSchema is the answer to a batch: what the model makes of each
// merchant. Types are checked against the list in code, so a type the
// model invents is dropped rather than refused.
var llmSchema = llm.MustSchema("categorize_merchants", []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["merchants"],
  "properties": {
    "merchants": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["merchant_key", "is_business", "category_type", "display_name", "confidence"],
        "properties": {
          "merchant_key": {"type": "string"},
          "is_business": {"type": "boolean"},
          "category_type": {"type": ["string", "null"]},
          "display_name": {"type": "string", "maxLength": 80},
          "confidence": {"type": "number", "minimum": 0, "maximum": 1}
        }
      }
    }
  }
}`))

const llmSystem = `You categorise card-statement merchants for a personal budgeting app.
For each merchant, say whether it is a business (a shop, service, company or
organisation) rather than a person or a transfer, which one category type
fits what people usually buy there, a clean display name, and how sure you
are from 0 to 1. Use only the category types listed. When you do not know
the merchant, or no type fits, give category_type null and a low
confidence. Answer for every merchant_key given, unchanged.`

type llmMerchant struct {
	MerchantKey string `json:"merchant_key"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	AmountSign  string `json:"amount_sign"`
}

// llmAnswerItem is what the model said about one merchant.
type llmAnswerItem struct {
	MerchantKey  string  `json:"merchant_key"`
	IsBusiness   bool    `json:"is_business"`
	CategoryType *string `json:"category_type"`
	DisplayName  string  `json:"display_name"`
	Confidence   float64 `json:"confidence"`
}

// WithLLM gives the engine layer 5: merchants no other layer placed are
// asked about, when a classification allows it.
func WithLLM(client llm.Client, logger *slog.Logger) EngineOption {
	return func(e *Engine) {
		e.llm = client
		e.logger = logger
	}
}

// llmLayer is layer 5 (PRD §5.2): the distinct merchants still unplaced,
// and not already answered by the model, go to the model in batches. Every
// answer about a business is saved for all users. Any failure leaves the
// merchants it concerns unplaced; it never fails the classification.
func (e *Engine) llmLayer(ctx context.Context, c *classification, types resolver, in []Input) {
	byKey := make(map[string]*llmMerchant)
	var order []string
	for _, i := range c.pending {
		key := c.results[i].MerchantKey
		if key == "" || c.answered[key] {
			continue
		}
		if _, ok := byKey[key]; ok {
			continue
		}
		sign := "-"
		if in[i].Direction == DirectionCredit {
			sign = "+"
		}
		// The merchant is normalised again from masked text: an email or an
		// account number in it is no longer recognisable once normalised. A
		// key that comes out different held one, and is not saved.
		masked := Normalize(llm.Redact(merchantSource(in[i])))
		byKey[key] = &llmMerchant{
			MerchantKey: masked.Key,
			DisplayName: masked.Display,
			Description: llm.Redact(strings.TrimSpace(in[i].RawDescription)),
			AmountSign:  sign,
		}
		order = append(order, key)
	}
	if len(order) == 0 {
		return
	}

	categoryTypes, err := e.q.ListCategoryTypes(ctx)
	if err != nil {
		e.logger.WarnContext(ctx, "categorize: llm layer skipped", "error", err)
		return
	}
	known := make(map[string]bool, len(categoryTypes))
	typeList := make([]string, len(categoryTypes))
	for i, t := range categoryTypes {
		known[t.Key] = true
		typeList[i] = t.Key
	}

	now := e.now()
	placed := make(map[string]llmPlacement)
	for start := 0; start < len(order); start += llmBatch {
		keys := order[start:min(start+llmBatch, len(order))]
		answers, err := e.askLLM(ctx, keys, byKey, typeList)
		if err != nil {
			e.logger.WarnContext(ctx, "categorize: llm batch failed", "merchants", len(keys), "error", err)
			continue
		}
		var save []LLMEntry
		for _, key := range keys {
			answer, ok := answers.byKey[byKey[key].MerchantKey]
			if !ok {
				continue
			}
			if answer.CategoryType != nil && !known[*answer.CategoryType] {
				continue // a type the model made up
			}
			confidence := min(max(answer.Confidence, 0), llmMaxConfidence)
			categoryType := ""
			if answer.CategoryType != nil {
				categoryType = *answer.CategoryType
			}
			display := strings.TrimSpace(answer.DisplayName)
			if display == "" {
				display = byKey[key].DisplayName
			}
			if categoryType != "" && confidence >= Threshold {
				placed[key] = llmPlacement{categoryType: categoryType, confidence: confidence, display: display}
			}
			if !answer.IsBusiness || len(key) < minSavedKey || byKey[key].MerchantKey != key {
				continue // used for this classification, not kept
			}
			entry := LLMEntry{Key: key, CategoryType: categoryType, DisplayName: display, Confidence: confidence, Model: answers.model}
			if categoryType == "" || confidence < Threshold {
				entry.ExpiresAt = now.Add(unknownMerchantTTL)
			}
			save = append(save, entry)
		}
		if err := e.store.SaveLLMEntries(ctx, save); err != nil {
			e.logger.WarnContext(ctx, "categorize: llm answers not saved", "merchants", len(save), "error", err)
		}
	}

	c.keep(func(i int) bool {
		p, ok := placed[c.results[i].MerchantKey]
		if !ok {
			return false
		}
		categoryID, ok := types.resolve(p.categoryType)
		if !ok || !c.place(i, categoryID, p.confidence, SourceLLM) {
			return false
		}
		c.results[i].DisplayMerchant = p.display
		return true
	})
}

type llmPlacement struct {
	categoryType string
	confidence   float64
	display      string
}

// llmAnswers are a batch's answers by the key as it was sent, and the
// model that gave them.
type llmAnswers struct {
	byKey map[string]llmAnswerItem
	model string
}

// askLLM asks about one batch of merchants.
func (e *Engine) askLLM(ctx context.Context, keys []string, byKey map[string]*llmMerchant, types []string) (llmAnswers, error) {
	merchants := make([]*llmMerchant, len(keys))
	for i, key := range keys {
		merchants[i] = byKey[key]
	}
	prompt, err := json.Marshal(struct {
		CategoryTypes []string       `json:"category_types"`
		Merchants     []*llmMerchant `json:"merchants"`
	}{types, merchants})
	if err != nil {
		return llmAnswers{}, fmt.Errorf("prompt: %w", err)
	}
	result, err := e.llm.CompleteJSON(ctx, llm.Request{
		Purpose:   "categorize",
		System:    llmSystem,
		Prompt:    string(prompt),
		MaxTokens: 120 * len(keys),
		Effort:    llm.EffortNone,
	}, llmSchema)
	if err != nil {
		return llmAnswers{}, err
	}
	var answer struct {
		Merchants []llmAnswerItem `json:"merchants"`
	}
	if err := json.Unmarshal(result.JSON, &answer); err != nil {
		return llmAnswers{}, fmt.Errorf("answer: %w", err)
	}
	answers := llmAnswers{byKey: make(map[string]llmAnswerItem, len(answer.Merchants)), model: result.Model}
	for _, item := range answer.Merchants {
		answers.byKey[item.MerchantKey] = item
	}
	return answers, nil
}
