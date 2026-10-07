package api

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractOperations lists every endpoint in docs/api-contract.md §2, which
// is also everything the traceability table in §3 points at. The spec must
// match it exactly: add here only what the contract adds.
var contractOperations = []string{
	// §2.13 Health
	"GET /healthz",
	"GET /readyz",
	// §2.1 Me
	"GET /me",
	"PATCH /me",
	"DELETE /me",
	// §2.2 Uploads
	"POST /uploads",
	// §2.3 Categories
	"GET /categories",
	"POST /categories",
	"PATCH /categories/{id}",
	"DELETE /categories/{id}",
	"PUT /categories/order",
	"PUT /budgets",
	// §2.4 Transactions
	"POST /transactions",
	"GET /transactions/{id}",
	"PATCH /transactions/{id}",
	"DELETE /transactions/{id}",
	"GET /transactions",
	// §2.5 Home
	"GET /home",
	// §2.6 Summaries
	"GET /summary/categories",
	"GET /summary/categories/{id}",
	"GET /summary/stats",
	// §2.7 Insights
	"GET /insights",
	// §2.8 Goals
	"GET /goals",
	"POST /goals",
	"GET /goals/{id}",
	"PATCH /goals/{id}",
	"DELETE /goals/{id}",
	"GET /goals/{id}/contributions",
	"POST /goals/{id}/contributions",
	"DELETE /goals/{id}/contributions/{cid}",
	// §2.9 Categorisation
	"POST /categorization/suggest",
	// §2.10 Statement imports
	"POST /imports",
	"GET /imports/{id}",
	"PUT /imports/{id}/mapping",
	"GET /imports/{id}/rows",
	"PATCH /imports/{id}/rows/{row_id}",
	"POST /imports/{id}/commit",
	"DELETE /imports/{id}",
	// §2.11 Receipts
	"POST /receipts",
	"GET /receipts/{id}",
	"POST /receipts/{id}/confirm",
	"DELETE /receipts/{id}",
	// §2.12 Devices & notifications
	"PUT /devices/{device_id}",
	"DELETE /devices/{device_id}",
	"GET /notifications",
	"POST /notifications/read",
}

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(Spec)
	require.NoError(t, err)
	return doc
}

type operation struct {
	key  string // "GET /me"
	path string
	op   *openapi3.Operation
}

func operations(doc *openapi3.T) []operation {
	var ops []operation
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			ops = append(ops, operation{key: method + " " + path, path: path, op: op})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].key < ops[j].key })
	return ops
}

func isHealth(op *openapi3.Operation) bool {
	return slices.Contains(op.Tags, "health")
}

func TestSpecIsValid(t *testing.T) {
	doc := loadSpec(t)

	require.NoError(t, doc.Validate(context.Background()))
	assert.Equal(t, "3.0.3", doc.OpenAPI)
}

func TestSpecMatchesContract(t *testing.T) {
	var got []string
	for _, o := range operations(loadSpec(t)) {
		got = append(got, o.key)
	}

	assert.ElementsMatch(t, contractOperations, got,
		"the spec and docs/api-contract.md must list the same endpoints")
}

func TestEveryOperationFollowsTheConventions(t *testing.T) {
	doc := loadSpec(t)
	seen := map[string]string{}

	for _, o := range operations(doc) {
		t.Run(o.key, func(t *testing.T) {
			require.NotEmpty(t, o.op.OperationID)
			assert.Empty(t, seen[o.op.OperationID], "operationId must be unique")
			seen[o.op.OperationID] = o.key

			require.Len(t, o.op.Tags, 1)
			assert.NotEmpty(t, o.op.Summary)

			fallback := o.op.Responses.Default()
			require.NotNil(t, fallback, "every operation documents the error envelope as its default response")
			assert.Equal(t, "#/components/responses/Error", fallback.Ref)

			if isHealth(o.op) {
				require.NotNil(t, o.op.Security, "health probes are explicitly unauthenticated")
				assert.Empty(t, *o.op.Security)
				return
			}
			assert.Nil(t, o.op.Security, "API operations inherit the global bearer requirement")
		})
	}

	require.Len(t, doc.Security, 1)
	assert.Contains(t, doc.Security[0], "bearerAuth")
}

func TestHealthProbesAreServedFromTheRoot(t *testing.T) {
	doc := loadSpec(t)

	require.Len(t, doc.Servers, 1)
	assert.Equal(t, "/v1", doc.Servers[0].URL)
	for _, path := range []string{"/healthz", "/readyz"} {
		item := doc.Paths.Find(path)
		require.NotNil(t, item, path)
		require.Len(t, item.Servers, 1, path)
		assert.Equal(t, "/", item.Servers[0].URL, path)
	}
}

// TestResponsePropertiesAreAlwaysPresent pins the contract's null rule:
// optional response fields are sent as null, never omitted. In the spec that
// means every property of a response schema is required (and nullable when
// it may be absent), which makes the generator emit it without omitempty.
func TestResponsePropertiesAreAlwaysPresent(t *testing.T) {
	doc := loadSpec(t)
	visited := map[*openapi3.Schema]bool{}

	var check func(t *testing.T, where string, ref *openapi3.SchemaRef)
	check = func(t *testing.T, where string, ref *openapi3.SchemaRef) {
		if ref == nil || ref.Value == nil || visited[ref.Value] {
			return
		}
		visited[ref.Value] = true
		schema := ref.Value
		if ref.Ref != "" {
			where = strings.TrimPrefix(ref.Ref, "#/components/schemas/")
		}
		for name, prop := range schema.Properties {
			assert.Contains(t, schema.Required, name, "%s.%s must be required", where, name)
			check(t, where+"."+name, prop)
		}
		check(t, where+"[]", schema.Items)
		for _, sub := range schema.AllOf {
			check(t, where, sub)
		}
	}

	for _, o := range operations(doc) {
		for status, resp := range o.op.Responses.Map() {
			if resp.Value == nil {
				continue
			}
			for _, media := range resp.Value.Content {
				check(t, o.key+" "+status, media.Schema)
			}
		}
	}

	assert.Greater(t, len(visited), 40, "the walk should reach every response schema")
}

func TestCreatingPostsAcceptAnIdempotencyKey(t *testing.T) {
	doc := loadSpec(t)
	// The operations the contract marks "Idempotent".
	want := []string{
		"POST /transactions",
		"POST /goals/{id}/contributions",
		"POST /imports/{id}/commit",
		"POST /receipts/{id}/confirm",
	}

	var got []string
	for _, o := range operations(doc) {
		for _, p := range o.op.Parameters {
			if p.Value != nil && p.Value.In == "header" && p.Value.Name == "Idempotency-Key" {
				got = append(got, o.key)
			}
		}
	}

	assert.ElementsMatch(t, want, got)
	assert.Equal(t, http.MethodPost, strings.Fields(want[0])[0])
}
