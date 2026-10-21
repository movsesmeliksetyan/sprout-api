package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// syncBuffer is a buffer the worker and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunWorker_RunsUntilAskedToStop(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	env := devEnv(t)
	env["DATABASE_URL"] = pool.Config().ConnString()
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"worker"}, cli{ctx: ctx, lookup: lookup, stdout: &stdout, stderr: &stderr})
	}()

	// The periodic jobs run on start.
	require.Eventually(t, func() bool { return strings.Contains(stdout.String(), `"msg":"dictionary promoted"`) },
		10*time.Second, 20*time.Millisecond, "stdout: %s\nstderr: %s", stdout.String(), stderr.String())
	cancel()
	select {
	case code := <-done:
		assert.Equal(t, exitOK, code, stderr.String())
	case <-time.After(15 * time.Second):
		t.Fatal("the worker did not stop when asked")
	}
	assert.Contains(t, stdout.String(), `"msg":"worker stopped"`)
}
