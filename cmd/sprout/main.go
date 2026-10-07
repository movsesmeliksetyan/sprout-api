// Command sprout is the Sprout backend binary: the HTTP API, the job worker
// and the migration runner live behind one entry point.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

var errNotImplemented = errors.New("not implemented yet")

type command struct {
	name    string
	summary string
	run     func(args []string) error
}

var commands = []command{
	{name: "api", summary: "Serve the HTTP API", run: notImplemented},
	{name: "worker", summary: "Run background and periodic jobs", run: notImplemented},
	{name: "migrate", summary: "Apply or inspect database migrations (up, down, status)", run: notImplemented},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}

	switch args[0] {
	case "-h", "--help", "help":
		usage(stdout)
		return exitOK
	}

	for _, cmd := range commands {
		if cmd.name != args[0] {
			continue
		}
		if err := cmd.run(args[1:]); err != nil {
			fmt.Fprintf(stderr, "sprout %s: %v\n", cmd.name, err)
			return exitError
		}
		return exitOK
	}

	fmt.Fprintf(stderr, "sprout: unknown command %q\n\n", args[0])
	usage(stderr)
	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprint(w, "Sprout backend.\n\nUsage:\n  sprout <command> [arguments]\n\nCommands:\n")
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", cmd.name, cmd.summary)
	}
}

func notImplemented([]string) error {
	return errNotImplemented
}
