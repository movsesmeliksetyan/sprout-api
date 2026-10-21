package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func TestSeedRules_LoadsOnceThenChangesNothing(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	env := map[string]string{"DATABASE_URL": pool.Config().ConnString()}
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}

	seedRules := func() string {
		var stdout, stderr bytes.Buffer
		code := run([]string{"seed-rules"}, cli{ctx: context.Background(), lookup: lookup, stdout: &stdout, stderr: &stderr})
		require.Equal(t, exitOK, code, stderr.String())
		return stdout.String()
	}

	first := seedRules()
	assert.Regexp(t, `merchant_dictionary: [1-9]\d* added, 0 updated, 0 removed`, first)
	assert.Regexp(t, `mcc_map: [1-9]\d* added, 0 updated, 0 removed`, first)
	assert.Regexp(t, `keyword_rules: [1-9]\d* added, 0 updated, 0 removed`, first)

	assert.Equal(t, "merchant_dictionary: 0 added, 0 updated, 0 removed\n"+
		"mcc_map: 0 added, 0 updated, 0 removed\n"+
		"keyword_rules: 0 added, 0 updated, 0 removed\n", seedRules())
}
