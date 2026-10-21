package receipts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
)

// itemTolerance is how far, in percent of the total, the line items may
// add up away from it before they are flagged.
const itemTolerance = 2

// maxLineItems bounds the items kept from one receipt.
const maxLineItems = 200

// extractionSchema is what the model reads off a receipt photo.
var extractionSchema = llm.MustSchema("receipt_extraction", []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["is_receipt", "merchant", "purchased_at", "total", "currency", "payment_method", "line_items"],
  "properties": {
    "is_receipt": {"type": "boolean"},
    "merchant": {"type": ["string", "null"]},
    "purchased_at": {"type": ["string", "null"]},
    "total": {"type": ["string", "null"]},
    "currency": {"type": ["string", "null"]},
    "payment_method": {
      "type": ["object", "null"],
      "additionalProperties": false,
      "required": ["brand", "last4"],
      "properties": {
        "brand": {"type": ["string", "null"]},
        "last4": {"type": ["string", "null"]}
      }
    },
    "line_items": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "quantity", "amount"],
        "properties": {
          "name": {"type": "string"},
          "quantity": {"type": ["number", "null"]},
          "amount": {"type": ["string", "null"]}
        }
      }
    }
  }
}`))

const extractionSystem = `You read a photo of a shop receipt. Say whether it is a receipt
at all (is_receipt). If it is, give the merchant's name as printed, the
date and time of purchase as YYYY-MM-DDTHH:MM (YYYY-MM-DD when no time is
printed), the total paid, the currency as an ISO 4217 code, the card brand
and only its last four digits if a card paid (never more digits), and every
line item with its quantity and amount. Write amounts as plain decimals
with a point and no thousands separators or currency signs ("1234.50").
Use null for anything not printed or not legible; never guess.`

// extraction is the model's answer.
type extraction struct {
	IsReceipt     bool        `json:"is_receipt"`
	Merchant      *string     `json:"merchant"`
	PurchasedAt   *string     `json:"purchased_at"`
	Total         *string     `json:"total"`
	Currency      *string     `json:"currency"`
	PaymentMethod *cardAnswer `json:"payment_method"`
	LineItems     []struct {
		Name     string   `json:"name"`
		Quantity *float64 `json:"quantity"`
		Amount   *string  `json:"amount"`
	} `json:"line_items"`
}

// cardAnswer is the card that paid, as the model read it.
type cardAnswer struct {
	Brand *string `json:"brand"`
	Last4 *string `json:"last4"`
}

// Extracted is what was read off a receipt, checked.
type Extracted struct {
	IsReceipt     bool
	Merchant      *string
	PurchasedAt   *time.Time
	TotalMinor    *int64
	Currency      *string
	PaymentMethod *string
	LineItems     []LineItem
	CategoryID    *uuid.UUID
	Confidence    *float64
	// Raw is the model's answer with what was noted about it: the items
	// not adding up to the total, a currency other than the user's.
	Raw json.RawMessage
}

// Notes are what the checks found, kept beside the model's answer.
type Notes struct {
	ItemsMismatch    bool   `json:"items_mismatch,omitempty"`
	CurrencyMismatch string `json:"currency_mismatch,omitempty"`
}

// Extract reads a receipt photo with the vision model and checks the
// answer (PRD BE-51): an amount that cannot be read is left out; line
// items more than itemTolerance % away from the total keep the total and
// are flagged; a currency other than the user's keeps the amount and is
// noted. The merchant and the items' names choose the category. A model
// failure is returned as is.
func Extract(ctx context.Context, client llm.Client, engine *categorize.Engine, userID uuid.UUID, img llm.Image, userCurrency string, loc *time.Location) (Extracted, error) {
	result, err := client.VisionJSON(ctx, llm.Request{
		Purpose: "receipt", System: extractionSystem,
		Prompt: "Read this receipt.", Effort: llm.EffortLow,
	}, img, extractionSchema)
	if err != nil {
		return Extracted{}, err
	}
	var answer extraction
	if err := json.Unmarshal(result.JSON, &answer); err != nil {
		return Extracted{}, fmt.Errorf("receipts: extraction: %w", err)
	}
	out := Extracted{IsReceipt: answer.IsReceipt}
	if !answer.IsReceipt {
		return out, nil
	}

	var notes Notes
	currency := userCurrency
	if code := upperCode(answer.Currency); code != "" {
		out.Currency, currency = &code, code
		if code != userCurrency {
			notes.CurrencyMismatch = code
		}
	}
	decimals := money.Decimals(currency)
	out.Merchant = cleanText(answer.Merchant)
	out.PurchasedAt = parsePurchase(answer.PurchasedAt, loc)
	if total, ok := parseMinor(answer.Total, decimals); ok && total > 0 {
		out.TotalMinor = &total
	}
	out.PaymentMethod = paymentMethod(answer.PaymentMethod)

	var itemsSum int64
	for i, item := range answer.LineItems {
		if i == maxLineItems {
			break
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		line := LineItem{Name: name, Quantity: item.Quantity}
		if amount, ok := parseMinor(item.Amount, decimals); ok {
			line.AmountMinor = &amount
			itemsSum += amount
		}
		out.LineItems = append(out.LineItems, line)
	}
	if out.TotalMinor != nil && len(out.LineItems) > 0 {
		diff := itemsSum - *out.TotalMinor
		if diff < 0 {
			diff = -diff
		}
		notes.ItemsMismatch = diff*100 > *out.TotalMinor*itemTolerance
	}

	if err := categorise(ctx, engine, userID, &out); err != nil {
		return Extracted{}, err
	}
	out.Raw, err = json.Marshal(struct {
		Answer json.RawMessage `json:"answer"`
		Notes  Notes           `json:"notes"`
	}{result.JSON, notes})
	if err != nil {
		return Extracted{}, err
	}
	return out, nil
}

// categorise chooses the receipt's category from its merchant, with the
// line items' names as a hint for the keyword layer.
func categorise(ctx context.Context, engine *categorize.Engine, userID uuid.UUID, out *Extracted) error {
	if engine == nil || (out.Merchant == nil && len(out.LineItems) == 0) {
		return nil
	}
	names := make([]string, len(out.LineItems))
	for i, item := range out.LineItems {
		names[i] = item.Name
	}
	input := categorize.Input{RawDescription: strings.Join(names, " "), Direction: categorize.DirectionDebit}
	if out.Merchant != nil {
		input.Merchant = *out.Merchant
	}
	if out.TotalMinor != nil {
		input.AmountMinor = *out.TotalMinor
	}
	results, err := engine.Classify(ctx, userID, []categorize.Input{input}, categorize.Options{AllowLLM: true})
	if err != nil {
		return fmt.Errorf("receipts: categorize: %w", err)
	}
	if results[0].CategoryID != nil {
		confidence := results[0].Confidence
		out.CategoryID, out.Confidence = results[0].CategoryID, &confidence
	}
	return nil
}

// parseMinor reads a plain decimal ("1234.50", "-3", "4,50") as minor
// units of a currency with decimals digits; a leading sign or currency
// symbol is ignored.
func parseMinor(s *string, decimals int) (int64, bool) {
	if s == nil {
		return 0, false
	}
	text := strings.TrimFunc(*s, func(r rune) bool { return !unicode.IsDigit(r) })
	text = strings.ReplaceAll(text, ",", ".")
	whole, fraction, _ := strings.Cut(text, ".")
	if whole == "" && fraction == "" || strings.Contains(fraction, ".") || len(fraction) > decimals {
		return 0, false
	}
	var minor int64
	for _, r := range whole + fraction + strings.Repeat("0", decimals-len(fraction)) {
		if r < '0' || r > '9' || minor > (1<<62)/10 {
			return 0, false
		}
		minor = minor*10 + int64(r-'0')
	}
	return minor, true
}

// parsePurchase reads YYYY-MM-DDTHH:MM, or a date alone (noon, so the day
// survives any timezone), in the user's timezone.
func parsePurchase(s *string, loc *time.Location) *time.Time {
	if s == nil {
		return nil
	}
	text := strings.TrimSpace(*s)
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, text, loc); err == nil {
			return &t
		}
	}
	if d, err := time.ParseInLocation(time.DateOnly, text, loc); err == nil {
		noon := d.Add(12 * time.Hour)
		return &noon
	}
	return nil
}

// paymentMethod is the card as shown, "Visa ·· 4821": the brand and the
// last four digits, never more.
func paymentMethod(pm *cardAnswer) *string {
	if pm == nil {
		return nil
	}
	brand := ""
	if b := cleanText(pm.Brand); b != nil {
		brand = *b
	}
	digits := ""
	if pm.Last4 != nil {
		for _, r := range *pm.Last4 {
			if r >= '0' && r <= '9' {
				digits += string(r)
			}
		}
	}
	if len(digits) >= 4 {
		digits = digits[len(digits)-4:]
	} else {
		digits = ""
	}
	switch {
	case brand != "" && digits != "":
		method := brand + " ·· " + digits
		return &method
	case brand != "":
		return &brand
	case digits != "":
		method := "Card ·· " + digits
		return &method
	}
	return nil
}

func cleanText(s *string) *string {
	if s == nil {
		return nil
	}
	text := strings.Join(strings.Fields(*s), " ")
	if text == "" {
		return nil
	}
	return &text
}

func upperCode(s *string) string {
	if s == nil {
		return ""
	}
	code := strings.ToUpper(strings.TrimSpace(*s))
	if len(code) != 3 {
		return ""
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return ""
		}
	}
	return code
}
