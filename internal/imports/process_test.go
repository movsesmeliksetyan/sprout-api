package imports_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/jobs"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// signedCSV has 20 readable rows and one that is not: 95 %, enough.
var signedCSV = "Date,Description,Amount\n14/07/2025,TESCO STORES 3297,-42.80\n13/07/2025,SALARY ACME,2500.00\n" +
	strings.Repeat("12/07/2025,PRET A MANGER,-1.00\n", 18) + "11/07/2025,BROKEN ROW,abc\n"

// obscureCSV has headers no synonym knows and dates that cannot tell day
// from month: the heuristics give up on it.
const obscureCSV = "Col A;Col B;Col C;Col D\n04.07.2025;Zahlung an XY;-42,80;R1\n03.07.2025;Gutschrift;100,00;R2\n02.07.2025;Kartenzahlung;-7,50;R3\n"

const obscureAnswer = `{"columns": [{"index": 0, "role": "date"}, {"index": 1, "role": "description"}, {"index": 2, "role": "amount"}, {"index": 3, "role": "reference"}],
  "date_format": "DD.MM.YYYY", "amount_sign": "negative_is_expense", "confidence": 0.9}`

func (f fixture) rows(t *testing.T, id uuid.UUID) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM import_rows WHERE import_id = $1`, id)
}

func source(i httpx.Import) string {
	if i.Detected.MappingSource == nil {
		return ""
	}
	return string(*i.Detected.MappingSource)
}

func TestProcess_ReadsAStatement(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	got := f.imported(t, user, signedCSV)
	assert.Equal(t, httpx.ImportStatus("ready"), got.Status)
	assert.Equal(t, "csv", *got.Detected.Format)
	assert.Equal(t, "heuristic", source(got))
	assert.Equal(t, httpx.ImportSummary{RowCount: 21, IncludedCount: 20, TotalExpenseMinor: 4280 + 1800, TotalIncomeMinor: 250000,
		UncategorizedCount: 19, ErrorCount: 1}, got.Summary)
	assert.Nil(t, got.Failure)
	assert.Zero(t, f.model.asked())

	var kind, merchant, localDate string
	var amount int64
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT kind, merchant, amount_minor, local_date::text FROM import_rows
		WHERE import_id = $1 AND row_index = 0`, got.ID).Scan(&kind, &merchant, &amount, &localDate))
	assert.Equal(t, []any{"expense", "Tesco", int64(4280), "2025-07-14"}, []any{kind, merchant, amount, localDate})

	// Processing again rebuilds the same rows.
	again := f.process(t, user, got.ID)
	assert.Equal(t, got.Summary, again.Summary)
	assert.Equal(t, 21, f.rows(t, got.ID))

	// The second file with these headers is read by the saved template.
	second := f.imported(t, user, signedCSV)
	assert.Equal(t, "template", source(second))
}

func TestProcess_Failures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	for content, code := range map[string]string{
		"\x00\x01\x02 binary \x03\x04\x05\x06\x07\x08 junk \x0e\x0f\x10\x11": "unsupported_format",
		"Date,Description,Amount\n": "empty_file",
		"   \n":                     "empty_file",
	} {
		got := f.imported(t, user, content)
		assert.Equal(t, httpx.ImportStatus("failed"), got.Status, "%q", content)
		if assert.NotNil(t, got.Failure, "%q", content) {
			assert.Equal(t, httpx.ImportFailureCode(code), got.Failure.Code, "%q", content)
			assert.NotEmpty(t, got.Failure.Message)
		}
		assert.Zero(t, f.rows(t, got.ID))
	}
}

func TestProcess_KeepsThePeriod(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	content := "Date,Description,Amount\n" +
		"21/07/2025,NEXT WEEK,-1.00\n20/07/2025,SUNDAY,-2.00\n14/07/2025,MONDAY,-3.00\n" +
		"13/07/2025,LAST WEEK,-4.00\n30/06/2025,LAST MONTH,-5.00\n"

	week := f.importedFor(t, user, content, "week")
	assert.Equal(t, httpx.ImportStatus("ready"), week.Status)
	assert.Equal(t, 2, week.Summary.RowCount)
	assert.Equal(t, int64(500), week.Summary.TotalExpenseMinor)
	assert.Equal(t, 3, week.Summary.OutOfPeriodCount)
	assert.Equal(t, 2, f.rows(t, week.ID))

	month := f.importedFor(t, user, content, "month")
	assert.Equal(t, 4, month.Summary.RowCount)
	assert.Equal(t, 1, month.Summary.OutOfPeriodCount)
}

func TestProcess_BalancesCorrectAnInvertedSign(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	content, err := os.ReadFile("../../testdata/statements/validate/inverted_sign.csv")
	require.NoError(t, err)

	got := f.imported(t, user, string(content))
	assert.Equal(t, httpx.ImportStatus("ready"), got.Status)
	assert.Equal(t, int64(250000+2500), got.Summary.TotalIncomeMinor)
	assert.Equal(t, int64(4280+450+6100+1099+14000), got.Summary.TotalExpenseMinor)
}

// commit writes an import's included rows as transactions the way Commit
// does, without needing every expense to have a category.
func (f fixture) commit(t *testing.T, user testutil.TestUser, id uuid.UUID) {
	t.Helper()
	category := testutil.NewCategory(t, f.pool, user.ID)
	_, err := f.pool.Exec(context.Background(), `INSERT INTO transactions (id, user_id, kind, amount_minor, category_id, merchant,
		merchant_key, occurred_at, local_date, source, import_id, dedup_hash, external_ref)
		SELECT id, user_id, kind, amount_minor, CASE WHEN kind = 'expense' THEN $2::uuid END, merchant, merchant_key, occurred_at,
		       local_date, 'import', import_id, dedup_hash, external_ref
		FROM import_rows WHERE import_id = $1 AND included`, id, category.ID)
	require.NoError(t, err)
}

func TestProcess_FindsDuplicates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	early := "Date,Description,Amount\n" +
		"14/07/2025,TESCO STORES 3297,-42.80\n15/07/2025,PRET A MANGER,-3.10\n15/07/2025,PRET A MANGER,-3.10\n16/07/2025,SALARY ACME,2500.00\n"
	late := "Date,Description,Amount\n" +
		"16/07/2025,SALARY ACME,2500.00\n17/07/2025,PRET A MANGER,-3.10\n18/07/2025,SHELL FUEL,-61.00\n"

	first := f.imported(t, user, early)
	assert.Zero(t, first.Summary.DuplicateCount, "two coffees on one day are two coffees")
	assert.Equal(t, 4, first.Summary.IncludedCount)
	f.commit(t, user, first.ID)

	again := f.imported(t, user, early)
	assert.Equal(t, httpx.ImportSummary{RowCount: 4, DuplicateCount: 4}, again.Summary, "the same file again is all duplicates")
	assert.Equal(t, 4, f.count(t, `SELECT count(*) FROM import_rows r JOIN transactions t ON t.id = r.duplicate_of
		WHERE r.import_id = $1 AND r.status = 'duplicate' AND NOT r.included`, again.ID))
	assert.Equal(t, 4, f.count(t, `SELECT count(DISTINCT duplicate_of) FROM import_rows WHERE import_id = $1`, again.ID))

	overlap := f.imported(t, user, late)
	assert.Equal(t, 1, overlap.Summary.DuplicateCount, "only the salary overlaps")
	assert.Equal(t, int64(310+6100), overlap.Summary.TotalExpenseMinor)

	// One coffee of two is already in: the other is new.
	other := testutil.NewTestUser(t, f.pool)
	single := f.imported(t, other, "Date,Description,Amount\n14/07/2025,TESCO STORES 3297,-42.80\n15/07/2025,PRET A MANGER,-3.10\n")
	f.commit(t, other, single.ID)
	two := f.imported(t, other, early)
	assert.Equal(t, 2, two.Summary.DuplicateCount, "Tesco and one coffee")
	assert.Equal(t, int64(310), two.Summary.TotalExpenseMinor)
	assert.Equal(t, int64(250000), two.Summary.TotalIncomeMinor)
}

func TestProcess_DuplicatesByReference(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	ofx := "OFXHEADER:100\n<OFX><STMTTRN><DTPOSTED>20250714<TRNAMT>-42.80<FITID>F1<NAME>TESCO</STMTTRN>" +
		"<STMTTRN><DTPOSTED>20250715<TRNAMT>-3.10<FITID>F2<NAME>PRET</STMTTRN></OFX>"
	first := f.imported(t, user, ofx)
	f.commit(t, user, first.ID)
	// The bank renamed a merchant: the reference still matches.
	renamed := strings.Replace(ofx, "<NAME>PRET", "<NAME>PRET A MANGER LONDON", 1)
	got := f.imported(t, user, renamed)
	assert.Equal(t, 2, got.Summary.DuplicateCount)
}

func TestProcess_OFXIsNative(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	got := f.imported(t, user, "OFXHEADER:100\n<OFX><STMTTRN><DTPOSTED>20250714<TRNAMT>-42.80<FITID>F1<NAME>TESCO</STMTTRN></OFX>")
	assert.Equal(t, httpx.ImportStatus("ready"), got.Status)
	assert.Equal(t, "native", source(got))
	assert.Equal(t, 1, got.Summary.RowCount)
}

func TestProcess_TheModelReadsObscureHeaders(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	f.model.answer = obscureAnswer

	got := f.imported(t, user, obscureCSV)
	assert.Equal(t, httpx.ImportStatus("ready"), got.Status)
	assert.Equal(t, "llm", source(got))
	assert.Equal(t, 3, got.Summary.IncludedCount)
	assert.Equal(t, 1, f.model.asked())

	again := f.imported(t, user, obscureCSV)
	assert.Equal(t, "template", source(again))
	assert.Equal(t, 1, f.model.asked(), "the model is asked once per layout")
}

func TestProcess_NeedsMappingThenTheUsersMapping(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	// A mapping that does not read the file: amounts are not dates.
	f.model.answer = `{"columns": [{"index": 2, "role": "date"}, {"index": 1, "role": "description"}, {"index": 0, "role": "amount"}],
		"date_format": "DD.MM.YYYY", "amount_sign": "negative_is_expense", "confidence": 0.9}`

	got := f.imported(t, user, obscureCSV)
	require.Equal(t, httpx.ImportStatus("needs_mapping"), got.Status)
	require.NotNil(t, got.MappingRequest)
	require.Len(t, got.MappingRequest.Columns, 4)
	assert.Equal(t, "Col A", got.MappingRequest.Columns[0].Header)
	assert.Equal(t, []string{"04.07.2025", "03.07.2025", "02.07.2025"}, got.MappingRequest.Columns[0].Samples)
	assert.NotContains(t, f.do(t, user, http.MethodGet, "/imports/"+got.ID.String(), nil).Body.String(), "sample_rows")
	assert.Zero(t, f.rows(t, got.ID))

	path := "/imports/" + got.ID.String() + "/mapping"
	mapping := map[string]any{
		"columns":     []map[string]any{{"index": 0, "role": "date"}, {"index": 1, "role": "description"}, {"index": 2, "role": "amount"}},
		"date_format": "DD.MM.YYYY", "amount_sign": "negative_is_expense",
	}
	assert.Equal(t, http.StatusNotFound, f.do(t, other, http.MethodPut, path, mapping).Code)
	bad := map[string]any{"columns": []map[string]any{{"index": 1, "role": "description"}, {"index": 2, "role": "amount"}}, "date_format": "DD.MM.YYYY"}
	assert.Equal(t, http.StatusUnprocessableEntity, f.do(t, user, http.MethodPut, path, bad).Code, "no date column")
	wrongOrder := map[string]any{"columns": mapping["columns"], "date_format": "YYYY.MM.DD"}
	assert.Equal(t, http.StatusUnprocessableEntity, f.do(t, user, http.MethodPut, path, wrongOrder).Code, "it does not read the rows")

	rec := f.do(t, user, http.MethodPut, path, mapping)
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, httpx.ImportStatus("processing"), testutil.DecodeJSON[httpx.Import](t, rec).Status)
	assert.Equal(t, 2, f.count(t, `SELECT count(*) FROM river_job WHERE args->>'import_id' = $1`, got.ID.String()), "processed again")

	done := f.process(t, user, got.ID)
	assert.Equal(t, httpx.ImportStatus("ready"), done.Status)
	assert.Equal(t, "template", source(done), "the user's mapping is the template now")
	assert.Equal(t, 3, done.Summary.IncludedCount)
	assert.Equal(t, http.StatusConflict, f.do(t, user, http.MethodPut, path, mapping).Code, "only while it needs a mapping")

	asked := f.model.asked()
	second := f.imported(t, user, obscureCSV)
	assert.Equal(t, httpx.ImportStatus("ready"), second.Status, "the next file with these headers needs no mapping")
	assert.Equal(t, asked, f.model.asked())
}

func TestProcess_RunsInTheWorker(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	rec := f.do(t, user, http.MethodPost, "/imports", map[string]any{"upload_id": f.file(t, user, "s.csv", signedCSV), "period": "all"})
	require.Equal(t, http.StatusAccepted, rec.Code)
	id := testutil.DecodeJSON[httpx.Import](t, rec).ID

	ctx, cancel := context.WithCancel(context.Background())
	worker, err := jobs.NewWorker(f.pool, testutil.Logger(t), jobs.Deps{Store: f.store})
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-stopped)
	})
	require.Eventually(t, func() bool {
		return f.count(t, `SELECT count(*) FROM imports WHERE id = $1 AND status = 'ready'`, id) == 1
	}, 10*time.Second, 20*time.Millisecond, "the worker processes the import")
	assert.Equal(t, 21, f.rows(t, id))
}
