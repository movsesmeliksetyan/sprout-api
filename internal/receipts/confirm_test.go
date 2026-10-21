package receipts_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func (f fixture) confirm(t *testing.T, user testutil.TestUser, id uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, user, http.MethodPost, "/receipts/"+id.String()+"/confirm", body)
}

func TestConfirmReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := shopper(t, f)
	scanned, err := f.scan(t, user, &vision{answer: answer(t, map[string]any{"merchant": "ZQXW Hardware", "total": "42.80"})},
		jpegOf(t, picture(600, 900)))
	require.NoError(t, err)
	require.Equal(t, httpx.ReceiptStatus("ready"), scanned.Status)
	category := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("DIY"))
	when := time.Date(2025, time.July, 14, 18, 24, 0, 0, time.UTC)

	// The user corrects the amount and chooses the category.
	body := map[string]any{"amount_minor": 4100, "category_id": category.ID, "occurred_at": when, "merchant": "ZQXW Hardware", "note": "Shelves"}
	rec := f.confirm(t, user, scanned.ID, body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := testutil.DecodeJSON[httpx.Transaction](t, rec)
	assert.Equal(t, int64(4100), created.AmountMinor)
	assert.Equal(t, category.ID, *created.CategoryID)
	assert.Equal(t, httpx.TransactionSource("receipt"), created.Source)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM transactions WHERE id = $1 AND receipt_id = $2 AND note = 'Shelves'`,
		created.ID, scanned.ID))
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM merchant_rules WHERE user_id = $1 AND merchant_key = 'zqxw hardware'
		AND category_id = $2`, user.ID, category.ID), "the categoriser learns the merchant")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM uploads u JOIN receipts r ON r.upload_id = u.id
		WHERE r.id = $1 AND u.purge_after IS NULL`, scanned.ID), "the photo is kept with the transaction")

	got := testutil.DecodeJSON[httpx.Receipt](t, f.do(t, user, http.MethodGet, "/receipts/"+scanned.ID.String(), nil))
	assert.Equal(t, httpx.ReceiptStatus("confirmed"), got.Status)
	assert.Equal(t, created.ID, *got.TransactionID)

	// Confirming again returns the same transaction.
	rec = f.confirm(t, user, scanned.ID, map[string]any{"amount_minor": 999, "category_id": category.ID, "occurred_at": when})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, created.ID, testutil.DecodeJSON[httpx.Transaction](t, rec).ID)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM transactions WHERE user_id = $1`, user.ID))
}

func TestConfirmReceipt_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := shopper(t, f)
	other := shopper(t, f)
	category := testutil.NewCategory(t, f.pool, user.ID)
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Old"), testutil.CategoryArchived())
	when := time.Date(2025, time.July, 14, 12, 0, 0, 0, time.UTC)
	valid := map[string]any{"amount_minor": 500, "category_id": category.ID, "occurred_at": when}

	processing := f.create(t, user)
	assert.Equal(t, http.StatusConflict, f.confirm(t, user, processing.ID, valid).Code, "still being read")

	failed, err := f.scan(t, user, &vision{answer: answer(t, map[string]any{"is_receipt": false})}, jpegOf(t, picture(60, 90)))
	require.NoError(t, err)
	for name, body := range map[string]map[string]any{
		"no amount":            {"amount_minor": 0, "category_id": category.ID, "occurred_at": when},
		"an archived category": {"amount_minor": 500, "category_id": archived.ID, "occurred_at": when},
	} {
		rec := f.confirm(t, user, failed.ID, body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
	}
	assert.Equal(t, http.StatusNotFound, f.confirm(t, other, failed.ID, valid).Code, "another user's")
	assert.Equal(t, http.StatusNotFound, f.confirm(t, user, uuid.New(), valid).Code)
	assert.Zero(t, f.count(t, `SELECT count(*) FROM transactions`))

	// A failed scan can still be confirmed with what the user typed.
	rec := f.confirm(t, user, failed.ID, valid)
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestPurgeUnconfirmed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := shopper(t, f)
	ctx := context.Background()
	old, recent := f.create(t, user), f.create(t, user)
	confirmed, err := f.scan(t, user, &vision{answer: answer(t, map[string]any{"total": "1.00"})}, jpegOf(t, picture(60, 90)))
	require.NoError(t, err)
	category := testutil.NewCategory(t, f.pool, user.ID)
	require.Equal(t, http.StatusCreated, f.confirm(t, user, confirmed.ID,
		map[string]any{"amount_minor": 100, "category_id": category.ID, "occurred_at": time.Now()}).Code)
	_, err = f.pool.Exec(ctx, `UPDATE receipts SET created_at = now() - interval '8 days' WHERE id = ANY($1)`,
		[]uuid.UUID{old.ID, confirmed.ID})
	require.NoError(t, err)

	purged, err := receipts.PurgeUnconfirmed(ctx, db.New(f.pool), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, purged)
	assert.Equal(t, 0, f.count(t, `SELECT count(*) FROM receipts WHERE id = $1`, old.ID))
	assert.Equal(t, 2, f.count(t, `SELECT count(*) FROM receipts WHERE id = ANY($1)`, []uuid.UUID{recent.ID, confirmed.ID}))
}
