package receipts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

// vision is a fake vision model with one answer, or one error, for every
// photo; it keeps what it was shown.
type vision struct {
	mu     sync.Mutex
	answer string
	err    error
	images []llm.Image
}

func (v *vision) Complete(_ context.Context, call llm.Call) (llm.Completion, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if call.Image != nil {
		v.images = append(v.images, *call.Image)
	}
	if v.err != nil {
		return llm.Completion{}, v.err
	}
	return llm.Completion{Text: v.answer}, nil
}

// scan uploads a photo for a user with the default categories, creates a
// receipt and processes it with the model's answer, returning the receipt
// and what processing returned.
func (f fixture) scan(t *testing.T, user testutil.TestUser, model *vision, photo []byte) (httpx.Receipt, error) {
	t.Helper()
	upload := testutil.Upload(t, f.uploads, user.ID, uploads.PurposeReceipt, "image/jpeg", "receipt.jpg", photo)
	rec := f.do(t, user, http.MethodPost, "/receipts", map[string]any{"upload_id": upload})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	id := testutil.DecodeJSON[httpx.Receipt](t, rec).ID

	client := llm.New(model, llm.Options{TextModel: "text", VisionModel: "vision", MaxAttempts: 1})
	processErr := receipts.NewProcessor(f.pool, f.store, client, categorize.NewEngine(db.New(f.pool))).Process(context.Background(), id)
	rec = f.do(t, user, http.MethodGet, "/receipts/"+id.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	return testutil.DecodeJSON[httpx.Receipt](t, rec), processErr
}

func shopper(t *testing.T, f fixture) testutil.TestUser {
	t.Helper()
	ctx := context.Background()
	_, err := categorize.SeedRules(ctx, f.pool)
	require.NoError(t, err)
	user := testutil.NewTestUser(t, f.pool)
	require.NoError(t, categories.Seed(ctx, db.New(f.pool), user.User))
	return user
}

func answer(t *testing.T, fields map[string]any) string {
	t.Helper()
	full := map[string]any{"is_receipt": true, "merchant": nil, "purchased_at": nil, "total": nil, "currency": nil,
		"payment_method": nil, "line_items": []any{}}
	for k, v := range fields {
		full[k] = v
	}
	encoded, err := json.Marshal(full)
	require.NoError(t, err)
	return string(encoded)
}

func item(name, amount string) map[string]any {
	return map[string]any{"name": name, "quantity": 1, "amount": amount}
}

func notes(t *testing.T, f fixture, id uuid.UUID) receipts.Notes {
	t.Helper()
	var raw []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT raw_extraction FROM receipts WHERE id = $1`, id).Scan(&raw))
	var stored struct {
		Notes receipts.Notes `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(raw, &stored))
	return stored.Notes
}

func TestExtract_CleanReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := shopper(t, f)
	model := &vision{answer: answer(t, map[string]any{
		"merchant": "TESCO EXPRESS", "purchased_at": "2025-07-14T18:24", "total": "42.80", "currency": strings.ToLower(user.Currency),
		"payment_method": map[string]any{"brand": "Visa", "last4": "4111 1111 1111 4821"},
		"line_items":     []any{item("Semi-skimmed milk 2L", "1.85"), item("Bread", "1.20"), item("Wine", "39.75")},
	})}

	got, err := f.scan(t, user, model, jpegOf(t, picture(2400, 3200)))
	require.NoError(t, err)
	assert.Equal(t, httpx.ReceiptStatus("ready"), got.Status)
	assert.Equal(t, "TESCO EXPRESS", *got.Merchant)
	assert.Equal(t, int64(4280), *got.TotalMinor)
	assert.Equal(t, user.Currency, *got.Currency)
	assert.Equal(t, "Visa ·· 4821", *got.PaymentMethod, "the last four digits only")
	require.NotNil(t, got.PurchasedAt)
	loc, err := time.LoadLocation(user.Timezone)
	require.NoError(t, err)
	assert.Equal(t, "2025-07-14 18:24", got.PurchasedAt.In(loc).Format("2006-01-02 15:04"), "in the user's timezone")
	require.Len(t, got.LineItems, 3)
	assert.Equal(t, int64(185), *got.LineItems[0].AmountMinor)
	require.NotNil(t, got.CategoryID, "Tesco is food")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM categories WHERE id = $1 AND category_type = 'food'`, *got.CategoryID))
	assert.Equal(t, receipts.Notes{}, notes(t, f, got.ID))

	require.Len(t, model.images, 1)
	assert.Equal(t, "image/jpeg", model.images[0].MediaType, "the photo is sent as a scaled JPEG")
}

func TestExtract_Partial(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := shopper(t, f)
	photo := jpegOf(t, picture(800, 1200))

	t.Run("missing date", func(t *testing.T) {
		got, err := f.scan(t, user, &vision{answer: answer(t, map[string]any{"merchant": "Corner Shop", "total": "3.50"})}, photo)
		require.NoError(t, err)
		assert.Equal(t, httpx.ReceiptStatus("ready"), got.Status)
		assert.Nil(t, got.PurchasedAt)
		assert.Nil(t, got.PaymentMethod)
		assert.Equal(t, int64(350), *got.TotalMinor)
	})
	t.Run("a long receipt", func(t *testing.T) {
		items := make([]any, 80)
		for i := range items {
			items[i] = item(fmt.Sprintf("Item %d", i+1), "1.00")
		}
		got, err := f.scan(t, user, &vision{answer: answer(t, map[string]any{"merchant": "Wholesale Club", "total": "80.00", "line_items": items})}, photo)
		require.NoError(t, err)
		assert.Len(t, got.LineItems, 80)
		assert.False(t, notes(t, f, got.ID).ItemsMismatch)
	})
	t.Run("items that do not add up keep the total", func(t *testing.T) {
		got, err := f.scan(t, user, &vision{answer: answer(t, map[string]any{"total": "42.80",
			"line_items": []any{item("Milk", "1.85"), map[string]any{"name": "Unreadable", "quantity": nil, "amount": nil}}})}, photo)
		require.NoError(t, err)
		assert.Equal(t, int64(4280), *got.TotalMinor)
		assert.Len(t, got.LineItems, 2)
		assert.Nil(t, got.LineItems[1].AmountMinor)
		assert.True(t, notes(t, f, got.ID).ItemsMismatch)
	})
	t.Run("another currency keeps the amount", func(t *testing.T) {
		other := "JPY"
		if user.Currency == "JPY" {
			other = "EUR"
		}
		got, err := f.scan(t, user, &vision{answer: answer(t, map[string]any{"total": "1200", "currency": other})}, photo)
		require.NoError(t, err)
		assert.Equal(t, other, *got.Currency)
		assert.Equal(t, other, notes(t, f, got.ID).CurrencyMismatch)
		if other == "JPY" {
			assert.Equal(t, int64(1200), *got.TotalMinor, "yen have no minor unit")
		}
	})
}

func TestExtract_Failures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := shopper(t, f)
	photo := jpegOf(t, picture(800, 1200))

	for name, tc := range map[string]struct {
		model *vision
		photo []byte
		code  string
	}{
		"a photo of a cat":        {&vision{answer: answer(t, map[string]any{"is_receipt": false})}, photo, "not_a_receipt"},
		"the model declines":      {&vision{err: llm.ErrRefused}, photo, "unreadable"},
		"an answer out of schema": {&vision{answer: `{"total": 12}`}, photo, "unreadable"},
		"not an image":            {&vision{answer: answer(t, nil)}, []byte("%PDF-1.7 not a photo"), "unreadable"},
	} {
		got, err := f.scan(t, user, tc.model, tc.photo)
		require.NoError(t, err, name)
		assert.Equal(t, httpx.ReceiptStatus("failed"), got.Status, name)
		if assert.NotNil(t, got.Failure, name) {
			assert.Equal(t, httpx.ReceiptFailureCode(tc.code), got.Failure.Code, name)
		}
	}

	// A provider that does not answer is retried by the job.
	got, err := f.scan(t, user, &vision{err: llm.ErrUnavailable}, photo)
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.Equal(t, httpx.ReceiptStatus("processing"), got.Status)
}
