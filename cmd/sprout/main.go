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
	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

var errNotImplemented = errors.New("not implemented yet")

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
	run     func(c cli, args []string) error
}

var commands = []command{
	{name: "api", summary: "Serve the HTTP API", run: runAPI},
	{name: "worker", summary: "Run background and periodic jobs", run: configuredStub},
	{name: "migrate", summary: "Apply or inspect database migrations (up, down, status)", run: runMigrate},
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
		fmt.Fprintf(w, "  %-8s %s\n", cmd.name, cmd.summary)
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

	responder := httpx.NewResponder(logger)
	userService := users.NewService(pool)
	opts := []httpx.Option{
		httpx.WithReadinessCheck("postgres", db.Ready(pool)),
		httpx.WithAPIMiddleware(
			auth.Middleware(verifier, responder, logger),
			users.Middleware(userService, responder),
		),
		httpx.WithAPI(newAPI(userService)),
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

// configuredStub validates the configuration and sets up logging, which is
// as far as the worker command goes for now.
func configuredStub(c cli, _ []string) error {
	cfg, err := config.Load(c.lookup)
	if err != nil {
		return err
	}
	logger := logging.New(c.stdout, cfg.LogLevel)
	logger.Info("config loaded", "config", cfg)
	return errNotImplemented
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
	return db.Migrate(c.ctx, databaseURL.Reveal(), args[0], c.stdout)
}
