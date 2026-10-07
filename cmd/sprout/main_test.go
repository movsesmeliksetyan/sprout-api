package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

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
		{name: "migrate without a command", args: []string{"migrate"}, wantCode: exitUsage, wantStderr: []string{"sprout migrate: usage: sprout migrate up|down|status"}},
		{name: "migrate with an unknown command", args: []string{"migrate", "redo"}, wantCode: exitUsage, wantStderr: []string{"usage: sprout migrate up|down|status"}},
		{name: "migrate with extra arguments", args: []string{"migrate", "up", "now"}, wantCode: exitUsage, wantStderr: []string{"usage: sprout migrate up|down|status"}},
		{
			name: "migrate needs only DATABASE_URL", args: []string{"migrate", "up"}, wantCode: exitError,
			wantStderr: []string{"sprout migrate: invalid configuration:", "DATABASE_URL: required"},
		},
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

// serveAPI runs `sprout api` with env until the test ends and returns its
// base URL once it is accepting requests.
func serveAPI(t *testing.T, env map[string]string) string {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
	exited := make(chan int, 1)
	go func() {
		exited <- run([]string{"api"}, cli{ctx: ctx, lookup: lookup, stdout: io.Discard, stderr: io.Discard})
	}()
	t.Cleanup(func() {
		stop()
		assert.Equal(t, exitOK, <-exited)
	})

	baseURL := "http://" + env["HTTP_ADDR"]
	require.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the API did not start")
	return baseURL
}

func TestRunAPI_ServesTheSpecOutsideProduction(t *testing.T) {
	prodOnly := map[string]string{
		"LLM_API_KEY": "test-key", "APNS_KEY_ID": "KEY", "APNS_TEAM_ID": "TEAM",
		"APNS_BUNDLE_ID": "app.sprout.ios", "APNS_KEY_PATH": "/secrets/apns.p8",
	}

	tests := []struct {
		env        string
		wantStatus int
	}{
		{"dev", http.StatusOK},
		{"staging", http.StatusOK},
		{"prod", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			env := devEnv(t)
			env["ENV"] = tt.env
			for k, v := range prodOnly {
				env[k] = v
			}
			baseURL := serveAPI(t, env)

			resp, err := http.Get(baseURL + "/v1/openapi.yaml")
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			if tt.wantStatus == http.StatusOK {
				assert.Contains(t, string(body), "openapi: 3.0.3")
			}

			anonymous, err := http.Get(baseURL + "/v1/me")
			require.NoError(t, err)
			_ = anonymous.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, anonymous.StatusCode, "the API is behind the token check")
		})
	}
}
