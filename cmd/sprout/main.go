// Command sprout is the Sprout backend binary: the HTTP API, the job worker
// and the migration runner live behind one entry point.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // users' timezones are resolved by name; the production image has no system database

	"github.com/movsesmeliksetyan/sprout-api/api"
	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/backfill"
	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
	"github.com/movsesmeliksetyan/sprout-api/internal/jobs"
	"github.com/movsesmeliksetyan/sprout-api/internal/jobs/schema"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/summary"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// usageError marks a command invoked with the wrong arguments.
type usageError struct{ usage string }

func (e *usageError) Error() string { return "usage: " + e.usage }

// cli is what a command may touch outside its arguments. ctx is cancelled
// when the process is asked to stop.
type cli struct {
	ctx    context.Context
	lookup func(string) (string, bool)
	stdout io.Writer
	stderr io.Writer
}

type command struct {
	name    string
	summary string
	// hidden keeps a development command out of the usage text.
	hidden bool
	run    func(c cli, args []string) error
}

var commands = []command{
	{name: "api", summary: "Serve the HTTP API", run: runAPI},
	{name: "worker", summary: "Run background and periodic jobs", run: runWorker},
	{name: "migrate", summary: "Apply or inspect database migrations (up, down, status)", run: runMigrate},
	{name: "seed-rules", summary: "Load the categorisation seeds: merchants, MCC codes, keywords", run: runSeedRules},
	{name: "seed", summary: "Fill a user's ledger with generated transactions", hidden: true, run: runSeed},
}

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "sprout: %v\n", err)
		os.Exit(exitError)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(os.Args[1:], cli{ctx: ctx, lookup: os.LookupEnv, stdout: os.Stdout, stderr: os.Stderr})
	stop()
	os.Exit(code)
}

func run(args []string, c cli) int {
	if len(args) == 0 {
		usage(c.stderr)
		return exitUsage
	}

	switch args[0] {
	case "-h", "--help", "help":
		usage(c.stdout)
		return exitOK
	}

	for _, cmd := range commands {
		if cmd.name != args[0] {
			continue
		}
		if err := cmd.run(c, args[1:]); err != nil {
			fmt.Fprintf(c.stderr, "sprout %s: %v\n", cmd.name, err)
			var usage *usageError
			if errors.As(err, &usage) {
				return exitUsage
			}
			return exitError
		}
		return exitOK
	}

	fmt.Fprintf(c.stderr, "sprout: unknown command %q\n\n", args[0])
	usage(c.stderr)
	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprint(w, "Sprout backend.\n\nUsage:\n  sprout <command> [arguments]\n\nCommands:\n")
	for _, cmd := range commands {
		if cmd.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-10s %s\n", cmd.name, cmd.summary)
	}
}

// runAPI serves HTTP until the process is asked to stop, then drains
// in-flight requests.
func runAPI(c cli, _ []string) error {
	cfg, err := config.Load(c.lookup)
	if err != nil {
		return err
	}
	logger := logging.New(c.stdout, cfg.LogLevel)
	logger.Info("config loaded", "config", cfg)

	pool, err := db.Open(c.ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()

	verifier, err := auth.NewAuth0Verifier(cfg.Auth0.Domain, cfg.Auth0.Audience)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	logger.Info("listening", "addr", ln.Addr().String())

	store := newStore(cfg)

	idempotentRoutes, err := api.IdempotentRoutes()
	if err != nil {
		return err
	}
	inserter, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return err
	}

	responder := httpx.NewResponder(logger)
	uploadService := uploads.NewService(pool, store)
	userService := users.NewService(pool,
		users.WithAvatars(uploadService, store, logger),
		users.WithOnUserCreated(categories.Seed),
		users.WithTransactionCheck(transactions.HasAny),
	)
	categoryService := categories.NewService(pool, categories.WithUsageCheck(transactions.CategoryInUse))
	transactionService := transactions.NewService(pool, transactions.WithMerchantRuleHook(categorize.Learn))
	llmClient, err := llm.NewFromConfig(cfg.LLM, llm.Options{Logger: logger})
	if err != nil {
		return err
	}
	engine := categorize.NewEngine(db.New(pool), categorize.WithLLM(llmClient, logger), categorize.WithLLMBatch(cfg.LLM.CategorizeBatch))
	opts := []httpx.Option{
		httpx.WithReadinessCheck("postgres", db.Ready(pool)),
		httpx.WithReadinessCheck("storage", store.Ready),
		httpx.WithAPIMiddleware(
			auth.Middleware(verifier, responder, logger),
			users.Middleware(userService, responder),
			httpx.Idempotency(pool, responder, logger, httpx.IdempotencyConfig{Routes: idempotentRoutes}),
		),
		httpx.WithAPI(newAPI(userService, uploadService, categoryService, transactionService,
			summary.NewService(pool, transactionService),
			goals.NewService(pool, goals.WithImages(uploadService, store, logger)), engine,
			imports.NewService(pool, uploadService, inserter),
			receipts.NewService(pool, uploadService, store, inserter))),
	}
	if cfg.Env != config.EnvProd {
		opts = append(opts, httpx.WithSpec(api.Spec))
	}
	if err := httpx.NewServer(logger, opts...).Run(c.ctx, ln); err != nil {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// runWorker runs the background and periodic jobs until the process is
// asked to stop, then lets running jobs finish (jobs.StopTimeout).
func runWorker(c cli, _ []string) error {
	cfg, err := config.Load(c.lookup)
	if err != nil {
		return err
	}
	logger := logging.New(c.stdout, cfg.LogLevel)
	logger.Info("config loaded", "config", cfg)

	pool, err := db.Open(c.ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()

	llmClient, err := llm.NewFromConfig(cfg.LLM, llm.Options{Logger: logger})
	if err != nil {
		return err
	}
	engine := categorize.NewEngine(db.New(pool), categorize.WithLLM(llmClient, logger), categorize.WithLLMBatch(cfg.LLM.CategorizeBatch))
	worker, err := jobs.NewWorker(pool, logger, jobs.Deps{Store: newStore(cfg), LLM: llmClient, Engine: engine})
	if err != nil {
		return err
	}
	logger.Info("worker started")
	if err := worker.Run(c.ctx); err != nil {
		return err
	}
	logger.Info("worker stopped")
	return nil
}

// runMigrate applies or inspects database migrations. It needs only
// DATABASE_URL, not the full configuration.
func runMigrate(c cli, args []string) error {
	usage := &usageError{usage: "sprout migrate up|down|status"}
	if len(args) != 1 {
		return usage
	}
	switch args[0] {
	case db.MigrateUp, db.MigrateDown, db.MigrateStatus:
	default:
		return usage
	}

	databaseURL, err := config.LoadDatabaseURL(c.lookup)
	if err != nil {
		return err
	}
	return db.Migrate(c.ctx, databaseURL.Reveal(), args[0], c.stdout, append(backfill.Migrations(), schema.Migration())...)
}

// runSeedRules makes the global categorisation tables match the seed files
// built into the binary. Running it again changes nothing. It needs only
// DATABASE_URL, not the full configuration.
func runSeedRules(c cli, args []string) error {
	if len(args) != 0 {
		return &usageError{usage: "sprout seed-rules"}
	}
	databaseURL, err := config.LoadDatabaseURL(c.lookup)
	if err != nil {
		return err
	}
	pool, err := db.Open(c.ctx, databaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()

	result, err := categorize.SeedRules(c.ctx, pool)
	if err != nil {
		return err
	}
	for _, table := range []struct {
		name   string
		counts categorize.SeedCounts
	}{
		{"merchant_dictionary", result.Dictionary},
		{"mcc_map", result.MCC},
		{"keyword_rules", result.Keywords},
	} {
		fmt.Fprintf(c.stdout, "%s: %d added, %d updated, %d removed\n",
			table.name, table.counts.Added, table.counts.Updated, table.counts.Removed)
	}
	return nil
}

// newStore is the object storage the configuration names.
func newStore(cfg config.Config) *storage.S3 {
	return storage.NewS3(storage.Config{
		Endpoint:        cfg.S3.Endpoint,
		Region:          cfg.S3.Region,
		Bucket:          cfg.S3.Bucket,
		AccessKeyID:     cfg.S3.AccessKeyID,
		SecretAccessKey: cfg.S3.SecretAccessKey.Reveal(),
		UsePathStyle:    cfg.S3.UsePathStyle,
	})
}
