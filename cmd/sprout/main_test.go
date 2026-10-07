package main

import (
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "s3-secret-77c1"

// freeAddr returns a loopback address that was free a moment ago. The
// configuration rejects port 0, so tests cannot ask the kernel to pick one.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, ln.Close())
	return ln.Addr().String()
}

func devEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"ENV":                  "dev",
		"HTTP_ADDR":            freeAddr(t),
		"DATABASE_URL":         "postgres://sprout:sprout@localhost:5432/sprout",
		"AUTH0_DOMAIN":         "sprout.eu.auth0.com",
		"AUTH0_AUDIENCE":       "https://api.sprout.local",
		"S3_REGION":            "us-east-1",
		"S3_BUCKET":            "sprout",
		"S3_ACCESS_KEY_ID":     "minioadmin",
		"S3_SECRET_ACCESS_KEY": testSecret,
		"LLM_PROVIDER":         "anthropic",
		"LLM_MODEL_TEXT":       "text-model",
		"LLM_MODEL_VISION":     "vision-model",
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{name: "help flag lists subcommands", args: []string{"--help"}, wantCode: exitOK, wantStdout: []string{"api", "worker", "migrate"}},
		{name: "short help flag", args: []string{"-h"}, wantCode: exitOK, wantStdout: []string{"Usage:"}},
		{name: "help command", args: []string{"help"}, wantCode: exitOK, wantStdout: []string{"Usage:"}},
		{name: "no arguments", args: nil, wantCode: exitUsage, wantStderr: []string{"Usage:"}},
		{name: "unknown command", args: []string{"serve"}, wantCode: exitUsage, wantStderr: []string{`unknown command "serve"`, "Usage:"}},
		{
			name: "api rejects an empty environment", args: []string{"api"}, wantCode: exitError,
			wantStderr: []string{"sprout api: invalid configuration:", "ENV: required", "DATABASE_URL: required"},
		},
		{
			name: "api serves until asked to stop, then exits cleanly", args: []string{"api"}, env: devEnv(t), wantCode: exitOK,
			wantStdout: []string{
				`"msg":"config loaded"`, `"env":"dev"`, `"secret_access_key":"[redacted]"`,
				`"msg":"listening"`, `"msg":"shutting down"`, `"msg":"shutdown complete"`,
			},
		},
		{
			name: "worker loads config then stops at the stub", args: []string{"worker"}, env: devEnv(t), wantCode: exitError,
			wantStdout: []string{`"msg":"config loaded"`},
			wantStderr: []string{"sprout worker: not implemented yet"},
		},
		{name: "migrate stub needs no config", args: []string{"migrate", "up"}, wantCode: exitError, wantStderr: []string{"sprout migrate: not implemented yet"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			lookup := func(key string) (string, bool) {
				v, ok := tt.env[key]
				return v, ok
			}

			// Already cancelled: a command that serves starts up and shuts straight down.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			got := run(tt.args, cli{ctx: ctx, lookup: lookup, stdout: &stdout, stderr: &stderr})

			assert.Equal(t, tt.wantCode, got, "exit code")
			for _, want := range tt.wantStdout {
				assert.Contains(t, stdout.String(), want)
			}
			for _, want := range tt.wantStderr {
				assert.Contains(t, stderr.String(), want)
			}
			assert.NotContains(t, stdout.String()+stderr.String(), testSecret)
		})
	}
}

func TestRunAPI_AddressInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	env := devEnv(t)
	env["HTTP_ADDR"] = taken.Addr().String()
	var stdout, stderr bytes.Buffer
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}

	got := run([]string{"api"}, cli{ctx: context.Background(), lookup: lookup, stdout: &stdout, stderr: &stderr})

	assert.Equal(t, exitError, got)
	assert.Contains(t, stderr.String(), "sprout api: listen on "+taken.Addr().String())
	assert.NotContains(t, stdout.String(), `"msg":"listening"`)
}
