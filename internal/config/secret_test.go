package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
)

func TestConfig_SecretsNeverPrinted(t *testing.T) {
	cfg, err := Load(lookupIn(validEnv()))
	require.NoError(t, err)

	var logged bytes.Buffer
	logging.New(&logged, slog.LevelDebug).Info("config loaded", "config", cfg)

	marshalled, err := json.Marshal(cfg)
	require.NoError(t, err)

	outputs := map[string]string{
		"slog as LogValuer": logged.String(),
		"json.Marshal":      string(marshalled),
		"%v":                fmt.Sprintf("%v", cfg),
		"%+v":               fmt.Sprintf("%+v", cfg),
		"%#v":               fmt.Sprintf("%#v", cfg),
		"%q on a secret":    fmt.Sprintf("%q", cfg.LLM.APIKey),
		"pointer %+v":       fmt.Sprintf("%+v", &cfg),
	}

	for name, out := range outputs {
		t.Run(name, func(t *testing.T) {
			for _, secret := range []string{testDBPassword, testS3Secret, testLLMKey} {
				assert.NotContains(t, out, secret)
			}
			assert.Contains(t, out, redacted)
		})
	}
}

func TestConfig_LogValueKeepsNonSecretFields(t *testing.T) {
	cfg, err := Load(lookupIn(validEnv()))
	require.NoError(t, err)

	var logged bytes.Buffer
	logging.New(&logged, slog.LevelDebug).Info("config loaded", "config", cfg)

	var record struct {
		Config struct {
			Env         string `json:"env"`
			HTTPAddr    string `json:"http_addr"`
			DatabaseURL string `json:"database_url"`
			S3          struct {
				Bucket          string `json:"bucket"`
				SecretAccessKey string `json:"secret_access_key"`
			} `json:"s3"`
		} `json:"config"`
	}
	require.NoError(t, json.Unmarshal(logged.Bytes(), &record))

	assert.Equal(t, "prod", record.Config.Env)
	assert.Equal(t, ":8080", record.Config.HTTPAddr)
	assert.Equal(t, "sprout-prod", record.Config.S3.Bucket)
	assert.Equal(t, redacted, record.Config.DatabaseURL)
	assert.Equal(t, redacted, record.Config.S3.SecretAccessKey)
}

func TestSecret(t *testing.T) {
	assert.Equal(t, "hunter2", Secret("hunter2").Reveal())
	assert.Equal(t, redacted, Secret("hunter2").String())
	assert.Empty(t, Secret("").String(), "an unset secret prints as empty, not as redacted")
}
