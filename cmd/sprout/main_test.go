package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

const testSecret = "s3-secret-77c1"

func devEnv() map[string]string {
	return map[string]string{
		"ENV":                  "dev",
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
			name: "api loads config then stops at the stub", args: []string{"api"}, env: devEnv(), wantCode: exitError,
			wantStdout: []string{`"msg":"config loaded"`, `"env":"dev"`, `"secret_access_key":"[redacted]"`},
			wantStderr: []string{"sprout api: not implemented yet"},
		},
		{
			name: "worker loads config then stops at the stub", args: []string{"worker"}, env: devEnv(), wantCode: exitError,
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

			got := run(tt.args, cli{lookup: lookup, stdout: &stdout, stderr: &stderr})

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
