package imports_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
	"github.com/movsesmeliksetyan/sprout-api/internal/jobs"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

// reviewCSV has a known merchant (Tesco: groceries), one no layer knows,
// a salary and a transfer to savings.
const reviewCSV = "Date,Description,Amount\n" +
	"14/07/2025,TESCO STORES 3297,-42.80\n" +
	"15/07/2025,ZQXW TRADING,-12.00\n" +
	"16/07/2025,SALARY ACME LTD,2500.00\n" +
	"17/07/2025,TRANSFER TO SAVINGS,-100.00\n"

// newReviewer is a user with the default categories, with the global
// categorisation seeds loaded.
func newReviewer(t *testing.T, f fixture) testutil.TestUser {
	t.Helper()
	ctx := context.Background()
	_, err := categorize.SeedRules(ctx, f.pool)
	require.NoError(t, err)
	user := testutil.NewTestUser(t, f.pool)
	require.NoError(t, categories.Seed(ctx, db.New(f.pool), user.User))
	return user
}

func (f fixture) listRows(t *testing.T, user testutil.TestUser, id uuid.UUID, query string) (*httptest.ResponseRecorder, httpx.ImportRowList) {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, "/imports/"+id.String()+"/rows"+query, nil)
	if rec.Code != http.StatusOK {
		return rec, httpx.ImportRowList{}
	}
	return rec, testutil.DecodeJSON[httpx.ImportRowList](t, rec)
}

// get returns an import as the API shows it.
func (f fixture) get(t *testing.T, user testutil.TestUser, id uuid.UUID) httpx.Import {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, "/imports/"+id.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	return testutil.DecodeJSON[httpx.Import](t, rec)
}

func rowByDescription(t *testing.T, list httpx.ImportRowList, description string) httpx.ImportRow {
	t.Helper()
	for _, row := range list.Items {
		if row.RawDescription == description {
			return row
		}
	}
	require.Failf(t, "no row", "%q", description)
	return httpx.ImportRow{}
}

func xlsxStatement(t *testing.T) []byte {
	t.Helper()
	file := excelize.NewFile()
	rows := [][]any{
		{"Date", "Description", "Amount"},
		{"2025-07-14", "TESCO STORES 3297", -42.80},
		{"2025-07-16", "SALARY ACME LTD", 2500.00},
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		require.NoError(t, err)
		require.NoError(t, file.SetSheetRow("Sheet1", cell, &row))
	}
	var buf bytes.Buffer
	require.NoError(t, file.Write(&buf))
	return buf.Bytes()
}

// From upload to ready in the worker, for each format the deterministic
// stages read.
func TestImport_EndToEnd(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := newReviewer(t, f)
	ofx := "OFXHEADER:100\n<OFX><STMTTRN><DTPOSTED>20250714<TRNAMT>-42.80<FITID>F1<NAME>TESCO STORES 3297</STMTTRN>" +
		"<STMTTRN><DTPOSTED>20250716<TRNAMT>2500.00<FITID>F2<NAME>SALARY ACME LTD</STMTTRN></OFX>"

	ctx, cancel := context.WithCancel(context.Background())
	worker, err := jobs.NewWorker(f.pool, testutil.Logger(t), jobs.Deps{Store: f.store})
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-stopped)
	})

	for _, file := range []struct {
		name, contentType, format string
		content                   []byte
	}{
		{"statement.csv", "text/csv", "csv", []byte(reviewCSV)},
		{"statement.ofx", "application/x-ofx", "ofx", []byte(ofx)},
		{"statement.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx", xlsxStatement(t)},
	} {
		upload := testutil.Upload(t, f.uploads, user.ID, uploads.PurposeStatement, file.contentType, file.name, file.content)
		rec := f.do(t, user, http.MethodPost, "/imports", map[string]any{"upload_id": upload, "period": "all"})
		require.Equal(t, http.StatusAccepted, rec.Code, "%s: %s", file.name, rec.Body.String())
		id := testutil.DecodeJSON[httpx.Import](t, rec).ID

		var got httpx.Import
		require.Eventually(t, func() bool {
			rec := f.do(t, user, http.MethodGet, "/imports/"+id.String(), nil)
			got = testutil.DecodeJSON[httpx.Import](t, rec)
			return got.Status != "processing"
		}, 10*time.Second, 20*time.Millisecond, file.name)
		require.Equal(t, httpx.ImportStatus("ready"), got.Status, file.name)
		assert.Equal(t, file.format, *got.Detected.Format, file.name)

		_, list := f.listRows(t, user, id, "")
		tesco := rowByDescription(t, list, "TESCO STORES 3297")
		assert.Equal(t, "Tesco", *tesco.Merchant, file.name)
		require.NotNil(t, tesco.CategoryID, file.name)
		assert.Equal(t, httpx.CategorizationSource("dictionary"), *tesco.CategorySource, file.name)
		salary := rowByDescription(t, list, "SALARY ACME LTD")
		assert.Equal(t, httpx.ImportRowKind("income"), *salary.Kind, file.name)
		assert.Nil(t, salary.CategoryID, file.name)
	}
}

func TestImportRows_ReviewAndPatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := newReviewer(t, f)
	got := f.imported(t, user, reviewCSV)
	require.Equal(t, httpx.ImportStatus("ready"), got.Status)
	assert.Equal(t, httpx.ImportSummary{RowCount: 4, IncludedCount: 3, TotalExpenseMinor: 4280 + 1200, TotalIncomeMinor: 250000,
		UncategorizedCount: 1}, got.Summary, "the transfer is left out; the unknown merchant needs a category")

	_, list := f.listRows(t, user, got.ID, "")
	require.Len(t, list.Items, 4)
	unknown := list.Items[0]
	assert.Equal(t, "ZQXW TRADING", unknown.RawDescription, "the uncategorised expense comes first")
	assert.Equal(t, httpx.CategorizationSource("none"), *unknown.CategorySource)
	assert.Equal(t, []string{"TRANSFER TO SAVINGS", "SALARY ACME LTD", "TESCO STORES 3297"},
		[]string{list.Items[1].RawDescription, list.Items[2].RawDescription, list.Items[3].RawDescription}, "then newest first")
	transfer := list.Items[1]
	assert.Equal(t, httpx.ImportRowKind("transfer"), *transfer.Kind)
	assert.False(t, transfer.Included)

	// Pages follow the same order.
	_, first := f.listRows(t, user, got.ID, "?limit=3")
	require.Len(t, first.Items, 3)
	require.NotNil(t, first.NextCursor)
	_, second := f.listRows(t, user, got.ID, "?limit=3&cursor="+*first.NextCursor)
	require.Len(t, second.Items, 1)
	assert.Equal(t, "TESCO STORES 3297", second.Items[0].RawDescription)
	assert.Nil(t, second.NextCursor)
	rec, _ := f.listRows(t, user, got.ID, "?cursor=nonsense")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Choosing a category files the row, updates the summary and teaches
	// the categoriser.
	category := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Hobbies"))
	path := "/imports/" + got.ID.String() + "/rows/" + unknown.ID.String()
	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"category_id": category.ID, "merchant": "ZQXW Hobby Shop"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patched := testutil.DecodeJSON[httpx.ImportRow](t, rec)
	assert.Equal(t, category.ID, *patched.CategoryID)
	assert.Equal(t, httpx.CategorizationSource("user_rule"), *patched.CategorySource)
	assert.Equal(t, 1.0, *patched.CategoryConfidence)
	assert.Equal(t, "ZQXW Hobby Shop", *patched.Merchant)
	assert.Zero(t, f.get(t, user, got.ID).Summary.UncategorizedCount)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM merchant_rules WHERE user_id = $1 AND merchant_key = 'zqxw hobby shop'
		AND category_id = $2`, user.ID, category.ID))

	// Including the transfer as an expense, and leaving out the salary.
	rec = f.do(t, user, http.MethodPatch, "/imports/"+got.ID.String()+"/rows/"+transfer.ID.String(),
		map[string]any{"kind": "expense", "included": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	salary := rowByDescription(t, list, "SALARY ACME LTD")
	rec = f.do(t, user, http.MethodPatch, "/imports/"+got.ID.String()+"/rows/"+salary.ID.String(), map[string]any{"included": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, httpx.ImportSummary{RowCount: 4, IncludedCount: 3, TotalExpenseMinor: 4280 + 1200 + 10000,
		UncategorizedCount: 1}, f.get(t, user, got.ID).Summary)

	// An expense that becomes income loses its category.
	tesco := rowByDescription(t, list, "TESCO STORES 3297")
	rec = f.do(t, user, http.MethodPatch, "/imports/"+got.ID.String()+"/rows/"+tesco.ID.String(), map[string]any{"kind": "income"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Nil(t, testutil.DecodeJSON[httpx.ImportRow](t, rec).CategoryID)
}

func TestImportRows_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := newReviewer(t, f)
	other := newReviewer(t, f)
	// Enough readable rows that one broken row does not stop the heuristics.
	got := f.imported(t, user, reviewCSV+strings.Repeat("19/07/2025,PRET A MANGER,-1.00\n", 20)+"18/07/2025,BROKEN,abc\n")
	require.Equal(t, httpx.ImportStatus("ready"), got.Status)
	_, list := f.listRows(t, user, got.ID, "?limit=200")
	broken := rowByDescription(t, list, "BROKEN")
	transfer := rowByDescription(t, list, "TRANSFER TO SAVINGS")
	salary := rowByDescription(t, list, "SALARY ACME LTD")
	tesco := rowByDescription(t, list, "TESCO STORES 3297")
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Old"), testutil.CategoryArchived())
	theirs := testutil.NewCategory(t, f.pool, other.ID)
	rowPath := func(row httpx.ImportRow) string { return "/imports/" + got.ID.String() + "/rows/" + row.ID.String() }

	for name, tc := range map[string]struct {
		row   httpx.ImportRow
		body  map[string]any
		field string
	}{
		"including an error row":     {broken, map[string]any{"included": true}, "included"},
		"giving an error row a kind": {broken, map[string]any{"kind": "expense"}, "kind"},
		"including a transfer":       {transfer, map[string]any{"included": true}, "included"},
		"a category on income":       {salary, map[string]any{"category_id": tesco.CategoryID}, "category_id"},
		"an archived category":       {tesco, map[string]any{"category_id": archived.ID}, "category_id"},
		"another user's category":    {tesco, map[string]any{"category_id": theirs.ID}, "category_id"},
		"a long merchant":            {tesco, map[string]any{"merchant": string(bytes.Repeat([]byte("x"), 81))}, "merchant"},
	} {
		rec := f.do(t, user, http.MethodPatch, rowPath(tc.row), tc.body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"`+tc.field+`"`, name)
	}
	assert.Equal(t, got.Summary, f.get(t, user, got.ID).Summary, "nothing changed")

	// Another user's import and rows are not found.
	rec := f.do(t, other, http.MethodPatch, rowPath(tesco), map[string]any{"included": false})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec, _ = f.listRows(t, other, got.ID, "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.do(t, user, http.MethodPatch, "/imports/"+got.ID.String()+"/rows/"+uuid.NewString(), map[string]any{"included": false})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Only a ready import is edited.
	_, err := f.pool.Exec(context.Background(), `UPDATE imports SET status = 'committed' WHERE id = $1`, got.ID)
	require.NoError(t, err)
	rec = f.do(t, user, http.MethodPatch, rowPath(tesco), map[string]any{"included": false})
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// A slow import tells the user when it is ready; a quick one does not.
func TestProcess_ReadyHook(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	var notified []uuid.UUID
	for _, took := range []time.Duration{time.Second, imports.ReadyNoticeAfter + time.Second} {
		clock := now
		processor := imports.NewProcessor(f.pool, f.store, nil,
			imports.WithClock(func() time.Time { clock = clock.Add(took); return clock }),
			imports.WithReadyHook(func(_ context.Context, userID, importID uuid.UUID) error {
				assert.Equal(t, user.ID, userID)
				notified = append(notified, importID)
				return nil
			}))
		rec := f.do(t, user, http.MethodPost, "/imports", map[string]any{"upload_id": f.file(t, user, "s.csv", reviewCSV), "period": "all"})
		require.Equal(t, http.StatusAccepted, rec.Code)
		id := testutil.DecodeJSON[httpx.Import](t, rec).ID
		require.NoError(t, processor.Process(context.Background(), id))
		if took > imports.ReadyNoticeAfter {
			assert.Equal(t, []uuid.UUID{id}, notified)
		} else {
			assert.Empty(t, notified)
		}
	}
}
