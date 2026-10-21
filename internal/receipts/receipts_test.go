package receipts_test

import (
	"context"
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
	"github.com/movsesmeliksetyan/sprout-api/internal/jobs"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

type api struct {
	*receipts.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

type fixture struct {
	pool    *pgxpool.Pool
	store   *storage.S3
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
		httpx.WithAPI(api{Handler: receipts.NewHandler(receipts.NewService(pool, uploadService, store, inserter))}),
	).Handler()
	return fixture{pool: pool, store: store, uploads: uploadService, server: server}
}

func (f fixture) do(t *testing.T, user testutil.TestUser, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := testutil.AuthedRequest(t, user.Claims, method, "/v1"+path, body)
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	return testutil.Do(f.server, req)
}

func (f fixture) photo(t *testing.T, user testutil.TestUser) uuid.UUID {
	t.Helper()
	return testutil.Upload(t, f.uploads, user.ID, uploads.PurposeReceipt, "image/jpeg", "receipt.jpg", []byte("\xff\xd8\xff jpeg"))
}

func (f fixture) create(t *testing.T, user testutil.TestUser) httpx.Receipt {
	t.Helper()
	rec := f.do(t, user, http.MethodPost, "/receipts", map[string]any{"upload_id": f.photo(t, user)})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Receipt](t, rec)
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestCreateReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	created := f.create(t, user)
	assert.Equal(t, httpx.ReceiptStatus("processing"), created.Status)
	require.NotNil(t, created.ImageURL, "a link to the photo")
	assert.Empty(t, created.LineItems)
	assert.Nil(t, created.Merchant)
	assert.Nil(t, created.Failure)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'process_receipt' AND args->>'receipt_id' = $1`,
		created.ID.String()), "the job is enqueued with the receipt")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM uploads u JOIN receipts r ON r.upload_id = u.id
		WHERE r.id = $1 AND u.purge_after > now() + interval '6 days'`, created.ID), "an unconfirmed photo is kept a week")

	rec := f.do(t, user, http.MethodGet, "/receipts/"+created.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, created.ID, testutil.DecodeJSON[httpx.Receipt](t, rec).ID)
}

func TestCreateReceipt_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	used := f.photo(t, user)
	require.Equal(t, http.StatusAccepted, f.do(t, user, http.MethodPost, "/receipts", map[string]any{"upload_id": used}).Code)
	statement := testutil.Upload(t, f.uploads, user.ID, uploads.PurposeStatement, "text/csv", "s.csv", []byte("Date,Amount\n"))

	for name, upload := range map[string]uuid.UUID{
		"used upload":     used,
		"another user's":  f.photo(t, other),
		"not a receipt":   statement,
		"never requested": uuid.New(),
	} {
		rec := f.do(t, user, http.MethodPost, "/receipts", map[string]any{"upload_id": upload})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
	}
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM receipts`), "nothing else was created")
}

func TestReceipt_OtherUsersAreNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	created := f.create(t, owner)

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := f.do(t, other, method, "/receipts/"+created.ID.String(), nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, method)
	}
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM receipts`))
}

func TestDeleteReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	created := f.create(t, user)

	rec := f.do(t, user, http.MethodDelete, "/receipts/"+created.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusNotFound, f.do(t, user, http.MethodGet, "/receipts/"+created.ID.String(), nil).Code)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM uploads WHERE user_id = $1 AND purpose = 'receipt'
		AND purge_after <= now()`, user.ID), "the photo goes with the next purge")

	confirmed := f.create(t, user)
	_, err := f.pool.Exec(context.Background(), `UPDATE receipts SET status = 'confirmed' WHERE id = $1`, confirmed.ID)
	require.NoError(t, err)
	rec = f.do(t, user, http.MethodDelete, "/receipts/"+confirmed.ID.String(), nil)
	assert.Equal(t, http.StatusConflict, rec.Code, "a confirmed receipt stays with its transaction")
}

func TestProcessReceipt_RunsInTheWorker(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	created := f.create(t, user)

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
		return f.count(t, `SELECT count(*) FROM receipts WHERE id = $1 AND status <> 'processing'`, created.ID) == 1
	}, 10*time.Second, 20*time.Millisecond, "the worker reads the receipt")

	rec := f.do(t, user, http.MethodGet, "/receipts/"+created.ID.String(), nil)
	got := testutil.DecodeJSON[httpx.Receipt](t, rec)
	assert.Equal(t, httpx.ReceiptStatus("failed"), got.Status, "until extraction exists")
	require.NotNil(t, got.Failure)
	assert.Equal(t, httpx.ReceiptFailureCode("internal"), got.Failure.Code)
}
