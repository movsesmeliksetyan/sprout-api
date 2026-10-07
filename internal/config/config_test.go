package config

import (
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDBPassword = "db-pass-4f9a"
	testS3Secret   = "s3-secret-77c1"
	testLLMKey     = "llm-key-b2e8"
)

// validEnv is a complete environment that passes validation in every Env.
func validEnv() map[string]string {
	return map[string]string{
		"ENV":                  "prod",
		"DATABASE_URL":         "postgres://sprout:" + testDBPassword + "@db.internal:5432/sprout",
		"AUTH0_DOMAIN":         "sprout.eu.auth0.com",
		"AUTH0_AUDIENCE":       "https://api.sprout.app",
		"S3_REGION":            "eu-west-2",
		"S3_BUCKET":            "sprout-prod",
		"S3_ACCESS_KEY_ID":     "AKIAEXAMPLE",
		"S3_SECRET_ACCESS_KEY": testS3Secret,
		"LLM_PROVIDER":         "anthropic",
		"LLM_API_KEY":          testLLMKey,
		"LLM_MODEL_TEXT":       "text-model",
		"LLM_MODEL_VISION":     "vision-model",
		"APNS_KEY_ID":          "ABC123DEFG",
		"APNS_TEAM_ID":         "TEAM123456",
		"APNS_BUNDLE_ID":       "app.sprout.ios",
		"APNS_KEY_PATH":        "/secrets/apns.p8",
	}
}

// envWith returns validEnv with overrides applied; an empty value removes the key.
func envWith(overrides map[string]string) map[string]string {
	env := validEnv()
	for k, v := range overrides {
		if v == "" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return env
}

func lookupIn(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func loadErr(t *testing.T, env map[string]string) *ValidationError {
	t.Helper()
	_, err := Load(lookupIn(env))
	var verr *ValidationError
	require.ErrorAs(t, err, &verr)
	return verr
}

func TestLoad_Valid(t *testing.T) {
	cfg, err := Load(lookupIn(validEnv()))
	require.NoError(t, err)

	assert.Equal(t, EnvProd, cfg.Env)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel, "default level")
	assert.Equal(t, ":8080", cfg.HTTPAddr, "default address")
	assert.Equal(t, "postgres://sprout:"+testDBPassword+"@db.internal:5432/sprout", cfg.DatabaseURL.Reveal())
	assert.Equal(t, Auth0{Domain: "sprout.eu.auth0.com", Audience: "https://api.sprout.app"}, cfg.Auth0)
	assert.Equal(t, S3{
		Region: "eu-west-2", Bucket: "sprout-prod", AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: testS3Secret,
	}, cfg.S3)
	assert.Equal(t, LLM{
		Provider: "anthropic", APIKey: testLLMKey, ModelText: "text-model", ModelVision: "vision-model",
	}, cfg.LLM)
	assert.Equal(t, APNS{
		KeyID: "ABC123DEFG", TeamID: "TEAM123456", BundleID: "app.sprout.ios", KeyPath: "/secrets/apns.p8",
	}, cfg.APNS)
}

func TestLoad_OptionalValues(t *testing.T) {
	cfg, err := Load(lookupIn(envWith(map[string]string{
		"LOG_LEVEL":         "DEBUG",
		"HTTP_ADDR":         "127.0.0.1:9090",
		"S3_ENDPOINT":       "http://localhost:9000",
		"S3_USE_PATH_STYLE": "true",
		"AUTH0_AUDIENCE":    "  https://api.sprout.app  ",
	})))
	require.NoError(t, err)

	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
	assert.Equal(t, "127.0.0.1:9090", cfg.HTTPAddr)
	assert.Equal(t, "http://localhost:9000", cfg.S3.Endpoint)
	assert.True(t, cfg.S3.UsePathStyle)
	assert.Equal(t, "https://api.sprout.app", cfg.Auth0.Audience, "surrounding whitespace is trimmed")
}

func TestLoad_MissingRequiredKey(t *testing.T) {
	for key := range validEnv() {
		t.Run(key, func(t *testing.T) {
			verr := loadErr(t, envWith(map[string]string{key: ""}))

			assert.Contains(t, verr.Keys(), key)
			assert.Contains(t, verr.Error(), key+": required")
		})
	}
}

func TestLoad_BlankValueCountsAsMissing(t *testing.T) {
	verr := loadErr(t, envWith(map[string]string{"S3_BUCKET": "   "}))

	assert.Equal(t, []string{"S3_BUCKET"}, verr.Keys())
}

func TestLoad_ReportsEveryProblemAtOnce(t *testing.T) {
	verr := loadErr(t, map[string]string{})

	assert.ElementsMatch(t, []string{
		"ENV", "DATABASE_URL", "AUTH0_DOMAIN", "AUTH0_AUDIENCE",
		"S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY",
		"LLM_PROVIDER", "LLM_MODEL_TEXT", "LLM_MODEL_VISION",
	}, verr.Keys(), "with no usable ENV, staging/prod-only keys are not demanded")
}

func TestLoad_EmptyEnvironmentInDevNamesOnlyDevKeys(t *testing.T) {
	verr := loadErr(t, map[string]string{"ENV": "dev"})

	assert.ElementsMatch(t, []string{
		"DATABASE_URL", "AUTH0_DOMAIN", "AUTH0_AUDIENCE",
		"S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY",
		"LLM_PROVIDER", "LLM_MODEL_TEXT", "LLM_MODEL_VISION",
	}, verr.Keys())
}

func TestLoad_InvalidValues(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		value       string
		wantProblem string
	}{
		{"unknown env", "ENV", "local", `must be one of dev, staging, prod (got "local")`},
		{"unknown log level", "LOG_LEVEL", "verbose", `must be one of debug, info, warn, error (got "verbose")`},
		{"address without port", "HTTP_ADDR", "localhost", "must be host:port"},
		{"non-numeric port", "HTTP_ADDR", ":http", "must be host:port"},
		{"port out of range", "HTTP_ADDR", ":70000", "must be host:port"},
		{"database url wrong scheme", "DATABASE_URL", "mysql://u:" + testDBPassword + "@h/db", "must use the postgres:// or postgresql:// scheme"},
		{"database url without host", "DATABASE_URL", "postgres:///sprout", "must include a host"},
		{"database url unparseable", "DATABASE_URL", "postgres://u:" + testDBPassword + "@h:port/db", "must be a valid URL"},
		{"auth0 domain with scheme", "AUTH0_DOMAIN", "https://sprout.eu.auth0.com", "must be a bare hostname"},
		{"auth0 domain with path", "AUTH0_DOMAIN", "sprout.eu.auth0.com/", "must be a bare hostname"},
		{"s3 endpoint without scheme", "S3_ENDPOINT", "localhost:9000", "must be an http:// or https:// URL"},
		{"s3 endpoint wrong scheme", "S3_ENDPOINT", "s3://bucket", "must be an http:// or https:// URL"},
		{"path style not a bool", "S3_USE_PATH_STYLE", "yes", `must be true or false (got "yes")`},
		{"unsupported llm provider", "LLM_PROVIDER", "openai", `must be anthropic (got "openai")`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verr := loadErr(t, envWith(map[string]string{tt.key: tt.value}))

			require.Len(t, verr.Fields, 1)
			assert.Equal(t, tt.key, verr.Fields[0].Key)
			assert.Contains(t, verr.Fields[0].Problem, tt.wantProblem)
			assert.NotContains(t, verr.Error(), testDBPassword, "errors must not echo secrets")
		})
	}
}

func TestLoad_PostgresqlSchemeAccepted(t *testing.T) {
	_, err := Load(lookupIn(envWith(map[string]string{"DATABASE_URL": "postgresql://u:p@localhost/sprout"})))

	assert.NoError(t, err)
}

func TestLoad_DevExemptions(t *testing.T) {
	withoutLaterKeys := map[string]string{
		"LLM_API_KEY": "", "APNS_KEY_ID": "", "APNS_TEAM_ID": "", "APNS_BUNDLE_ID": "", "APNS_KEY_PATH": "",
	}
	laterKeys := slices.Collect(maps.Keys(withoutLaterKeys))

	t.Run("dev may omit LLM key and APNS", func(t *testing.T) {
		env := envWith(withoutLaterKeys)
		env["ENV"] = "dev"

		cfg, err := Load(lookupIn(env))

		require.NoError(t, err)
		assert.Empty(t, cfg.LLM.APIKey)
		assert.Equal(t, APNS{}, cfg.APNS)
	})

	for _, env := range []string{"staging", "prod"} {
		t.Run(env+" requires them", func(t *testing.T) {
			vars := envWith(withoutLaterKeys)
			vars["ENV"] = env

			verr := loadErr(t, vars)

			assert.ElementsMatch(t, laterKeys, verr.Keys())
		})
	}

	t.Run("dev rejects a partial APNS set", func(t *testing.T) {
		env := envWith(map[string]string{"ENV": "dev", "APNS_KEY_PATH": "", "APNS_TEAM_ID": ""})

		verr := loadErr(t, env)

		assert.ElementsMatch(t, []string{"APNS_TEAM_ID", "APNS_KEY_PATH"}, verr.Keys())
		assert.Contains(t, verr.Error(), "APNS_KEY_PATH: required when any APNS_* key is set")
	})
}

func TestLoadDotEnv(t *testing.T) {
	t.Run("missing file is not an error", func(t *testing.T) {
		assert.NoError(t, LoadDotEnv(filepath.Join(t.TempDir(), ".env")))
	})

	t.Run("sets unset variables and keeps existing ones", func(t *testing.T) {
		const fromFile, alreadySet = "SPROUT_TEST_DOTENV_NEW", "SPROUT_TEST_DOTENV_EXISTING"
		path := filepath.Join(t.TempDir(), ".env")
		require.NoError(t, os.WriteFile(path, []byte(fromFile+"=from-file\n"+alreadySet+"=from-file\n"), 0o600))
		t.Setenv(alreadySet, "from-env")
		t.Cleanup(func() { _ = os.Unsetenv(fromFile) })

		require.NoError(t, LoadDotEnv(path))

		assert.Equal(t, "from-file", os.Getenv(fromFile))
		assert.Equal(t, "from-env", os.Getenv(alreadySet))
	})

	t.Run("parse error does not quote the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		require.NoError(t, os.WriteFile(path, []byte("LLM_API_KEY=\""+testLLMKey+"\nBROKEN LINE\n"), 0o600))

		err := LoadDotEnv(path)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), testLLMKey)
	})
}
