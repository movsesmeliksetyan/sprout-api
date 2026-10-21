package imports_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/summary"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

func (f fixture) commitImport(t *testing.T, user testutil.TestUser, id uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, user, http.MethodPost, "/imports/"+id.String()+"/commit", nil)
}

func TestCommitImport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := newReviewer(t, f)
	got := f.imported(t, user, reviewCSV)
	_, list := f.listRows(t, user, got.ID, "")
	unknown := rowByDescription(t, list, "ZQXW TRADING")

	// An expense without a category blocks the commit, named by its row.
	rec := f.commitImport(t, user, got.ID)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"`+unknown.ID.String()+`":"needs a category"`)
	assert.Zero(t, f.count(t, `SELECT count(*) FROM transactions WHERE user_id = $1`, user.ID))

	category := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Hobbies"), testutil.CategoryType("shopping"))
	rec = f.do(t, user, http.MethodPatch, "/imports/"+got.ID.String()+"/rows/"+unknown.ID.String(), map[string]any{"category_id": category.ID})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = f.commitImport(t, user, got.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"created_count": 3, "skipped_count": 1}`, rec.Body.String(), "the transfer is skipped")
	assert.Equal(t, 3, f.count(t, `SELECT count(*) FROM transactions t JOIN import_rows r ON r.id = t.id
		WHERE t.user_id = $1 AND t.source = 'import' AND t.import_id = $2 AND t.amount_minor = r.amount_minor
		AND t.dedup_hash = r.dedup_hash AND t.local_date = r.local_date`, user.ID, got.ID), "each row is its transaction")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM transactions WHERE import_id = $1 AND kind = 'income' AND category_id IS NULL`, got.ID))
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM merchant_votes WHERE user_id = $1 AND merchant_key = 'zqxw trading'
		AND category_type = 'shopping'`, user.ID), "the expenses vote")
	assert.Equal(t, httpx.ImportStatus("committed"), f.get(t, user, got.ID).Status)

	// The summaries count the new transactions at once.
	ledger := transactions.NewService(f.pool)
	offset := time.Now().Year() - 2025
	overview, err := summary.NewService(f.pool, ledger).Categories(context.Background(), user.User, "year", &offset)
	require.NoError(t, err)
	assert.Equal(t, int64(4280+1200), overview.TotalSpentMinor)

	// Committing again creates nothing and says the same.
	rec = f.commitImport(t, user, got.ID)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"created_count": 3, "skipped_count": 1}`, rec.Body.String())
	assert.Equal(t, 3, f.count(t, `SELECT count(*) FROM transactions WHERE user_id = $1`, user.ID))

	// After commit the import is read-only and cannot be discarded.
	rec = f.do(t, user, http.MethodPatch, "/imports/"+got.ID.String()+"/rows/"+unknown.ID.String(), map[string]any{"included": false})
	assert.Equal(t, http.StatusConflict, rec.Code)
	rec = f.do(t, user, http.MethodDelete, "/imports/"+got.ID.String(), nil)
	assert.Equal(t, http.StatusConflict, rec.Code)

	// Importing the same file again finds every row in the ledger.
	again := f.imported(t, user, reviewCSV)
	assert.Equal(t, 3, again.Summary.DuplicateCount)
}

func TestCommitImport_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := newReviewer(t, f)
	other := newReviewer(t, f)

	processing := f.create(t, user)
	rec := f.commitImport(t, user, processing.ID)
	assert.Equal(t, http.StatusConflict, rec.Code, "still processing")

	ready := f.imported(t, user, reviewCSV)
	rec = f.commitImport(t, other, ready.ID)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.commitImport(t, user, uuid.New())
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCommitImport_Fast(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("timing")
	}
	f := newFixture(t)
	user := newReviewer(t, f)
	var b strings.Builder
	b.WriteString("Date,Description,Amount\n")
	for i := range 1000 {
		fmt.Fprintf(&b, "%02d/07/2025,TESCO STORES %04d,-%d.%02d\n", 1+i%28, i, 1+i%90, i%100)
	}
	got := f.imported(t, user, b.String())
	require.Equal(t, 1000, got.Summary.IncludedCount)
	require.Zero(t, got.Summary.UncategorizedCount)

	started := time.Now()
	rec := f.commitImport(t, user, got.ID)
	took := time.Since(started)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1000, f.count(t, `SELECT count(*) FROM transactions WHERE import_id = $1`, got.ID))
	assert.Less(t, took, 2*time.Second, "commit of 1,000 rows")
}

func TestPurgeFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := newReviewer(t, f)
	ctx := context.Background()
	q := db.New(f.pool)
	objectKey := func(id uuid.UUID) string {
		var key string
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT u.object_key FROM uploads u JOIN imports i ON i.upload_id = u.id
			WHERE i.id = $1`, id).Scan(&key))
		return key
	}
	gone := func(key string) bool {
		_, err := f.store.Head(ctx, key)
		return errors.Is(err, storage.ErrNotFound)
	}

	abandoned := f.imported(t, user, reviewCSV)
	committed := f.imported(t, user, reviewCSV)
	_, err := f.pool.Exec(ctx, `UPDATE import_rows SET included = false WHERE import_id = $1 AND kind = 'expense'`, committed.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, f.commitImport(t, user, committed.ID).Code)
	discarded := f.imported(t, user, reviewCSV)
	discardedKey := objectKey(discarded.ID)
	require.Equal(t, http.StatusNoContent, f.do(t, user, http.MethodDelete, "/imports/"+discarded.ID.String(), nil).Code)
	abandonedKey, committedKey := objectKey(abandoned.ID), objectKey(committed.ID)

	purged, err := imports.PurgeFiles(ctx, q, f.store, time.Now().Add(imports.ClosedFileTTL-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, purged, "nothing is due within a day")

	purged, err = imports.PurgeFiles(ctx, q, f.store, time.Now().Add(imports.ClosedFileTTL+time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, purged, "the committed and the discarded file")
	assert.True(t, gone(committedKey))
	assert.True(t, gone(discardedKey))
	assert.False(t, gone(abandonedKey))
	assert.Zero(t, f.count(t, `SELECT count(*) FROM import_rows WHERE import_id = $1 AND raw IS NOT NULL`, committed.ID))
	assert.Equal(t, 4, f.count(t, `SELECT count(*) FROM import_rows WHERE import_id = $1 AND raw IS NOT NULL`, abandoned.ID))

	purged, err = imports.PurgeFiles(ctx, q, f.store, time.Now().Add(imports.AbandonedFileTTL+time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, purged, "the abandoned one after a week; the others only once")
	assert.True(t, gone(abandonedKey))
	assert.Zero(t, f.count(t, `SELECT count(*) FROM import_rows WHERE import_id = $1 AND raw IS NOT NULL`, abandoned.ID))
}
