package imports_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	pool    *pgxpool.Pool
	uploads *uploads.Service
	server  http.Handler
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
	return fixture{pool: pool, uploads: uploadService, server: server}
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

func TestProcess_RunsInTheWorkerAndIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	created := f.create(t, user)

	// Rows an earlier attempt left behind are wiped by the next.
	_, err := f.pool.Exec(context.Background(), `INSERT INTO import_rows (id, import_id, user_id, row_index, status, included)
		VALUES ($1, $2, $3, 0, 'ok', true)`, uuid.Must(uuid.NewV7()), created.ID, user.ID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	worker, err := jobs.NewWorker(f.pool, testutil.Logger(t))
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-stopped)
	})

	require.Eventually(t, func() bool {
		return f.count(t, `SELECT count(*) FROM imports WHERE id = $1 AND status = 'failed'`, created.ID) == 1
	}, 10*time.Second, 20*time.Millisecond, "the worker processes the import")
	got := testutil.DecodeJSON[httpx.Import](t, f.do(t, user, http.MethodGet, "/imports/"+created.ID.String(), nil))
	require.NotNil(t, got.Failure)
	assert.Equal(t, httpx.ImportFailureCode("internal"), got.Failure.Code)
	assert.Equal(t, 0, f.count(t, `SELECT count(*) FROM import_rows WHERE import_id = $1`, created.ID))

	// Running it again gives the same result.
	require.NoError(t, imports.Process(context.Background(), f.pool, created.ID))
	require.NoError(t, imports.Process(context.Background(), f.pool, created.ID))
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM imports WHERE id = $1 AND status = 'failed'`, created.ID))

	// A committed import is left alone.
	_, err = f.pool.Exec(context.Background(), `UPDATE imports SET status = 'committed', failure = NULL WHERE id = $1`, created.ID)
	require.NoError(t, err)
	require.NoError(t, imports.Process(context.Background(), f.pool, created.ID))
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM imports WHERE id = $1 AND status = 'committed'`, created.ID))
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
