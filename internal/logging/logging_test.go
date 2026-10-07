package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record), "output must be one JSON object: %s", buf.String())
	return record
}

func TestNew_WritesJSON(t *testing.T) {
	var buf bytes.Buffer

	New(&buf, slog.LevelInfo).Info("hello", "count", 3)

	record := decode(t, &buf)
	assert.Equal(t, "hello", record["msg"])
	assert.Equal(t, "INFO", record["level"])
	assert.EqualValues(t, 3, record["count"])
	assert.Contains(t, record, "time")
}

func TestNew_FiltersBelowLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelWarn)

	logger.Info("dropped")
	assert.Empty(t, buf.String())

	logger.Warn("kept")
	assert.Equal(t, "kept", decode(t, &buf)["msg"])
}

func TestWith_AddsContextFields(t *testing.T) {
	var buf bytes.Buffer
	ctx := With(context.Background(), slog.String("request_id", "req-1"))

	New(&buf, slog.LevelInfo).InfoContext(ctx, "handled", "status", 200)

	record := decode(t, &buf)
	assert.Equal(t, "req-1", record["request_id"])
	assert.EqualValues(t, 200, record["status"])
}

func TestWith_Accumulates(t *testing.T) {
	var buf bytes.Buffer
	parent := With(context.Background(), slog.String("request_id", "req-1"))
	child := With(parent, slog.String("user_id", "user-9"))

	logger := New(&buf, slog.LevelInfo)
	logger.InfoContext(child, "child")
	record := decode(t, &buf)
	assert.Equal(t, "req-1", record["request_id"])
	assert.Equal(t, "user-9", record["user_id"])

	buf.Reset()
	logger.InfoContext(parent, "parent")
	assert.NotContains(t, decode(t, &buf), "user_id", "a child context must not change its parent")
}

func TestWith_SameKeyReplaces(t *testing.T) {
	var buf bytes.Buffer
	ctx := With(context.Background(), slog.String("user_id", "anonymous"))
	ctx = With(ctx, slog.String("user_id", "user-9"))

	New(&buf, slog.LevelInfo).InfoContext(ctx, "handled")

	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte(`"user_id"`)))
	assert.Equal(t, "user-9", decode(t, &buf)["user_id"])
}

func TestWith_NoAttrsReturnsSameContext(t *testing.T) {
	ctx := context.Background()

	assert.Equal(t, ctx, With(ctx))
}

func TestContextFieldsSurviveDerivedLoggers(t *testing.T) {
	var buf bytes.Buffer
	ctx := With(context.Background(), slog.String("request_id", "req-1"))

	New(&buf, slog.LevelInfo).With("component", "imports").InfoContext(ctx, "started")

	record := decode(t, &buf)
	assert.Equal(t, "imports", record["component"])
	assert.Equal(t, "req-1", record["request_id"])
}

func TestPlainContextLogsNormally(t *testing.T) {
	var buf bytes.Buffer

	New(&buf, slog.LevelInfo).InfoContext(context.Background(), "plain")

	assert.Equal(t, "plain", decode(t, &buf)["msg"])
}
