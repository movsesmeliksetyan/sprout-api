package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/api"
)

func TestIdempotentRoutes(t *testing.T) {
	routes, err := api.IdempotentRoutes()

	require.NoError(t, err)
	// The POSTs the contract marks "Idempotent".
	assert.Equal(t, []string{
		"/v1/goals/{id}/contributions",
		"/v1/imports/{id}/commit",
		"/v1/receipts/{id}/confirm",
		"/v1/transactions",
	}, routes)
}
