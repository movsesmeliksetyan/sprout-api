// Package jobs runs Sprout's background work on river: the jobs, their
// queue and retries, the periodic ones, and the client the API inserts
// with.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
)

const (
	// maxWorkers is how many jobs one worker process runs at once.
	maxWorkers = 10
	// promoteEvery and purgeEvery space the periodic jobs.
	promoteEvery = 24 * time.Hour
	purgeEvery   = time.Hour
	// StopTimeout is how long a stopping worker lets running jobs finish
	// before it cancels them.
	StopTimeout = 30 * time.Second
)

// Client inserts jobs, and runs them in a worker process.
type Client struct {
	river *river.Client[pgx.Tx]
}

// NewInserter returns a client that only inserts jobs, for the API.
func NewInserter(pool *pgxpool.Pool, logger *slog.Logger) (*Client, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("jobs: client: %w", err)
	}
	return &Client{river: client}, nil
}

// Deps are what the jobs work with besides the database.
type Deps struct {
	// Store holds uploaded files.
	Store storage.Store
	// LLM reads the statements the deterministic stages cannot; nil leaves
	// them to the user.
	LLM llm.Client
	// Engine categorises imported rows; nil is one without a language
	// model.
	Engine *categorize.Engine
}

// NewWorker returns a client that runs every job, the periodic ones
// included, for `sprout worker`.
func NewWorker(pool *pgxpool.Pool, logger *slog.Logger, deps Deps) (*Client, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &processImport{pool: pool, logger: logger, processor: imports.NewProcessor(pool, deps.Store, deps.LLM, imports.WithEngine(deps.Engine))})
	river.AddWorker(workers, &processReceipt{pool: pool, logger: logger, processor: receipts.NewProcessor(pool, deps.Store, deps.LLM, deps.Engine)})
	river.AddWorker(workers, &promoteDictionary{pool: pool, logger: logger})
	river.AddWorker(workers, &purgeIdempotencyKeys{pool: pool, logger: logger})
	river.AddWorker(workers, &purgeUploadFiles{pool: pool, store: deps.Store, logger: logger})
	river.AddWorker(workers, &purgeReceipts{pool: pool, logger: logger})

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:       logger,
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: maxWorkers}},
		Workers:      workers,
		ErrorHandler: errorHandler{logger: logger},
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(promoteEvery),
				func() (river.JobArgs, *river.InsertOpts) { return PromoteDictionaryArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(purgeEvery),
				func() (river.JobArgs, *river.InsertOpts) { return PurgeIdempotencyKeysArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(purgeEvery),
				func() (river.JobArgs, *river.InsertOpts) { return PurgeUploadFilesArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(purgeEvery),
				func() (river.JobArgs, *river.InsertOpts) { return PurgeReceiptsArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: worker: %w", err)
	}
	return &Client{river: client}, nil
}

// Run works jobs until ctx is cancelled, then lets running jobs finish
// for up to StopTimeout before cancelling them.
func (c *Client) Run(ctx context.Context) error {
	if err := c.river.Start(ctx); err != nil {
		return fmt.Errorf("jobs: start: %w", err)
	}
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), StopTimeout)
	defer cancel()
	if err := c.river.Stop(stopCtx); err != nil {
		cancelCtx, cancelNow := context.WithTimeout(context.Background(), StopTimeout)
		defer cancelNow()
		if err := c.river.StopAndCancel(cancelCtx); err != nil {
			return fmt.Errorf("jobs: stop: %w", err)
		}
	}
	return nil
}

// errorHandler logs a job's failures; river retries it with backoff until
// its attempts run out.
type errorHandler struct{ logger *slog.Logger }

func (h errorHandler) HandleError(ctx context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	h.logger.WarnContext(ctx, "job failed", "kind", job.Kind, "job_id", job.ID, "attempt", job.Attempt,
		"max_attempts", job.MaxAttempts, "error", err.Error())
	return nil
}

func (h errorHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicVal any, trace string) *river.ErrorHandlerResult {
	h.logger.ErrorContext(ctx, "job panicked", "kind", job.Kind, "job_id", job.ID, "attempt", job.Attempt,
		"panic", fmt.Sprint(panicVal), "trace", trace)
	return nil
}
