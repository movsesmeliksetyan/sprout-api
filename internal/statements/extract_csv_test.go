package statements_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got, as indented JSON, with the golden file at path, or
// rewrites it with -update.
func golden(t *testing.T, path string, got any) {
	t.Helper()
	encoded, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	encoded = append(encoded, '\n')
	if *update {
		require.NoError(t, os.WriteFile(path, encoded, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test -update to create it")
	assert.Equal(t, string(want), string(encoded))
}

func TestExtractCSV_Golden(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("../../testdata/statements/csv/*.csv")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 10)
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(file)
			require.NoError(t, err)
			detected, err := statements.Detect(data)
			require.NoError(t, err)
			require.Equal(t, statements.FormatCSV, detected.Format)
			table, err := statements.ExtractCSV(detected)
			require.NoError(t, err)
			for i, row := range table.Rows {
				require.Len(t, row, len(table.Headers), "row %d is as wide as the header", i)
			}
			golden(t, strings.TrimSuffix(file, ".csv")+".golden.json", table)
		})
	}
}

func TestExtractCSV_HeaderOnly(t *testing.T) {
	t.Parallel()
	detected, err := statements.Detect([]byte("Date,Description,Amount\n2025-07-14,TESCO,-1.00\n"))
	require.NoError(t, err)
	detected.HeaderRow = 5
	_, err = statements.ExtractCSV(detected)
	assert.ErrorIs(t, err, statements.ErrNoRows)
}
