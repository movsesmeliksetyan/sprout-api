package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{name: "help flag lists subcommands", args: []string{"--help"}, wantCode: exitOK, wantStdout: []string{"api", "worker", "migrate"}},
		{name: "short help flag", args: []string{"-h"}, wantCode: exitOK, wantStdout: []string{"Usage:"}},
		{name: "help command", args: []string{"help"}, wantCode: exitOK, wantStdout: []string{"Usage:"}},
		{name: "no arguments", args: nil, wantCode: exitUsage, wantStderr: []string{"Usage:"}},
		{name: "unknown command", args: []string{"serve"}, wantCode: exitUsage, wantStderr: []string{`unknown command "serve"`, "Usage:"}},
		{name: "api stub", args: []string{"api"}, wantCode: exitError, wantStderr: []string{"sprout api: not implemented yet"}},
		{name: "worker stub", args: []string{"worker"}, wantCode: exitError, wantStderr: []string{"sprout worker: not implemented yet"}},
		{name: "migrate stub", args: []string{"migrate", "up"}, wantCode: exitError, wantStderr: []string{"sprout migrate: not implemented yet"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if got := run(tt.args, &stdout, &stderr); got != tt.wantCode {
				t.Errorf("exit code = %d, want %d", got, tt.wantCode)
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
				}
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
				}
			}
		})
	}
}
