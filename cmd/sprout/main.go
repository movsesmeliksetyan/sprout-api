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

	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

var errNotImplemented = errors.New("not implemented yet")

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
	{name: "migrate", summary: "Apply or inspect database migrations (up, down, status)", run: notImplemented},
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

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	logger.Info("listening", "addr", ln.Addr().String())

	if err := httpx.NewServer(logger).Run(c.ctx, ln); err != nil {
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

func notImplemented(cli, []string) error {
	return errNotImplemented
}
