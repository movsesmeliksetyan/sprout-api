package imports_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spec "github.com/movsesmeliksetyan/sprout-api/api"
	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
	"github.com/movsesmeliksetyan/sprout-api/internal/jobs"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

type api struct {
	*imports.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

type fixture struct {
	pool      *pgxpool.Pool
	store     *storage.S3
	uploads   *uploads.Service
	server    http.Handler
	model     *model
	processor *imports.Processor
}

// model is a fake language model with one answer for every mapping
// question, counting the questions.
type model struct {
	mu     sync.Mutex
	answer string
	calls  int
}

func (m *model) Complete(context.Context, llm.Call) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.answer == "" {
		return llm.Completion{}, llm.ErrRefused
	}
	return llm.Completion{Text: m.answer}, nil
}

func (m *model) asked() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool, store := testutil.NewDB(t), testutil.NewStore(t)
	logger := testutil.Logger(t)
	uploadService := uploads.NewService(pool, store)
	inserter, err := jobs.NewInserter(pool, logger)
	require.NoError(t, err)
	responder := httpx.NewResponder(logger)
	routes, err := spec.IdempotentRoutes()
	require.NoError(t, err)
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
			httpx.Idempotency(pool, responder, logger, httpx.IdempotencyConfig{Routes: routes}),
		),
		httpx.WithAPI(api{Handler: imports.NewHandler(imports.NewService(pool, uploadService, inserter))}),
	).Handler()
	m := &model{}
	processor := imports.NewProcessor(pool, store, llm.New(m, llm.Options{TextModel: "test", MaxAttempts: 1}),
		imports.WithClock(func() time.Time { return now }))
	return fixture{pool: pool, store: store, uploads: uploadService, server: server, model: m, processor: processor}
}

// file uploads a statement with the given name and content.
func (f fixture) file(t *testing.T, user testutil.TestUser, name, content string) uuid.UUID {
	t.Helper()
	return testutil.Upload(t, f.uploads, user.ID, uploads.PurposeStatement, "text/csv", name, []byte(content))
}

// now is the processor's clock: Wednesday 16 July 2025, in the week of
// Monday 14 to Sunday 20.
var now = time.Date(2025, time.July, 16, 12, 0, 0, 0, time.UTC)

// imported creates an import of content keeping every period and processes
// it, returning it.
func (f fixture) imported(t *testing.T, user testutil.TestUser, content string) httpx.Import {
	t.Helper()
	return f.importedFor(t, user, content, "all")
}

// importedFor is imported keeping only the given period.
func (f fixture) importedFor(t *testing.T, user testutil.TestUser, content, period string) httpx.Import {
	t.Helper()
	rec := f.do(t, user, http.MethodPost, "/imports", map[string]any{"upload_id": f.file(t, user, "statement.csv", content), "period": period})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	return f.process(t, user, testutil.DecodeJSON[httpx.Import](t, rec).ID)
}

// process runs the pipeline for an import and returns it.
func (f fixture) process(t *testing.T, user testutil.TestUser, id uuid.UUID) httpx.Import {
	t.Helper()
	require.NoError(t, f.processor.Process(context.Background(), id))
	rec := f.do(t, user, http.MethodGet, "/imports/"+id.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	return testutil.DecodeJSON[httpx.Import](t, rec)
}

func (f fixture) do(t *testing.T, user testutil.TestUser, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := testutil.AuthedRequest(t, user.Claims, method, "/v1"+path, body)
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	return testutil.Do(f.server, req)
}

func (f fixture) statement(t *testing.T, user testutil.TestUser) uuid.UUID {
	t.Helper()
	return testutil.Upload(t, f.uploads, user.ID, uploads.PurposeStatement, "text/csv", "statement.csv",
		[]byte("Date,Description,Amount\n2025-07-14,TESCO STORES,-42.80\n"))
}

func (f fixture) create(t *testing.T, user testutil.TestUser) httpx.Import {
	t.Helper()
	rec := f.do(t, user, http.MethodPost, "/imports", map[string]any{"upload_id": f.statement(t, user), "period": "month"})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Import](t, rec)
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestCreateImport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.do(t, user, http.MethodPost, "/imports", map[string]any{"upload_id": f.statement(t, user), "period": "month"})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{
		"status": "processing", "filename": "statement.csv", "period": "month",
		"detected": {"format": null, "bank_hint": null, "mapping_source": null},
		"summary": {"row_count": 0, "included_count": 0, "total_expense_minor": 0, "total_income_minor": 0,
		            "duplicate_count": 0, "uncategorized_count": 0, "error_count": 0, "out_of_period_count": 0},
		"mapping_request": null, "failure": null
	}`, withoutVolatile(t, rec.Body.Bytes()))

	created := testutil.DecodeJSON[httpx.Import](t, rec)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'process_import' AND args->>'import_id' = $1`, created.ID.String()),
		"the job is enqueued with the import")

	got := f.do(t, user, http.MethodGet, "/imports/"+created.ID.String(), nil)
	require.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, created.ID, testutil.DecodeJSON[httpx.Import](t, got).ID)
}

func TestCreateImport_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)

	used := f.statement(t, user)
	require.Equal(t, http.StatusAccepted, f.do(t, user, http.MethodPost, "/imports", map[string]any{"upload_id": used, "period": "all"}).Code)
	avatar := testutil.Upload(t, f.uploads, user.ID, uploads.PurposeAvatar, "image/png", "", []byte("png"))

	for name, body := range map[string]map[string]any{
		"used upload":          {"upload_id": used, "period": "month"},
		"another user's":       {"upload_id": f.statement(t, other), "period": "month"},
		"not a statement":      {"upload_id": avatar, "period": "month"},
		"unknown period":       {"upload_id": f.statement(t, user), "period": "decade"},
		"upload never created": {"upload_id": uuid.New(), "period": "month"},
	} {
		rec := f.do(t, user, http.MethodPost, "/imports", body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
	}
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM imports`), "nothing else was created")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM river_job`), "and no job")
}

func TestImport_OtherUsersAreNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	created := f.create(t, owner)

	assert.Equal(t, http.StatusNotFound, f.do(t, other, http.MethodGet, "/imports/"+created.ID.String(), nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(t, other, http.MethodDelete, "/imports/"+created.ID.String(), nil).Code)
	assert.Equal(t, http.StatusOK, f.do(t, owner, http.MethodGet, "/imports/"+created.ID.String(), nil).Code)
}

func TestDeleteImport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	discarded := f.create(t, user)
	assert.Equal(t, http.StatusNoContent, f.do(t, user, http.MethodDelete, "/imports/"+discarded.ID.String(), nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(t, user, http.MethodGet, "/imports/"+discarded.ID.String(), nil).Code)

	committed := f.create(t, user)
	_, err := f.pool.Exec(context.Background(), `UPDATE imports SET status = 'committed' WHERE id = $1`, committed.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, f.do(t, user, http.MethodDelete, "/imports/"+committed.ID.String(), nil).Code)
}

// withoutVolatile drops the fields a test cannot know.
func withoutVolatile(t *testing.T, body []byte) string {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal(body, &fields))
	delete(fields, "id")
	delete(fields, "created_at")
	out, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(out)
}
