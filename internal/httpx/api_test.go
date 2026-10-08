package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/api"
)

const testID = "0192f0c8-7b1a-7c3e-9d2f-4a5b6c7d8e9f"

// specRequest builds a well-formed request for an operation in the spec:
// path parameters filled, required query parameters set, an empty JSON
// object as the body where one is expected.
func specRequest(method, path string, item *openapi3.PathItem, op *openapi3.Operation) *http.Request {
	target := apiBasePath + path
	var query []string
	for _, p := range slices.Concat(item.Parameters, op.Parameters) {
		param := p.Value
		switch {
		case param.In == "path":
			target = strings.ReplaceAll(target, "{"+param.Name+"}", testID)
		case param.In == "query" && param.Required:
			value := "x"
			if enum := param.Schema.Value.Enum; len(enum) > 0 {
				value = enum[0].(string)
			}
			query = append(query, param.Name+"="+value)
		}
	}
	if len(query) > 0 {
		target += "?" + strings.Join(query, "&")
	}

	if op.RequestBody == nil {
		return httptest.NewRequest(method, target, nil)
	}
	req := httptest.NewRequest(method, target, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestEveryOperationInTheSpecIsMounted(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(api.Spec)
	require.NoError(t, err)
	s, logs := newTestServer(t)
	mounted := 0

	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if slices.Contains(op.Tags, "health") {
				continue
			}
			mounted++
			t.Run(method+" "+path, func(t *testing.T) {
				rec := do(s, specRequest(method, path, item, op))

				body := requireEnvelope(t, rec, http.StatusNotImplemented, "not_implemented")
				assert.Equal(t, "This endpoint is not implemented yet.", body.Message)
			})
		}
	}

	assert.Equal(t, 44, mounted, "every /v1 operation in the spec")
	for _, line := range logs.records(t) {
		assert.NotEqual(t, "ERROR", line["level"], "a 501 is expected, not a failure: %v", line)
	}
}

func TestAPI_RequestErrors(t *testing.T) {
	s, _ := newTestServer(t)
	jsonBody := func(method, target, body string) *http.Request {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	tests := []struct {
		name       string
		req        *http.Request
		wantStatus int
		wantCode   string
	}{
		{"unknown path under /v1", httptest.NewRequest(http.MethodGet, "/v1/nope", nil), 404, codeNotFound},
		{"wrong method", httptest.NewRequest(http.MethodPut, "/v1/home", nil), 405, codeBadRequest},
		{"path parameter is not a uuid", httptest.NewRequest(http.MethodGet, "/v1/transactions/not-a-uuid", nil), 400, codeBadRequest},
		{"second path parameter is not a uuid", httptest.NewRequest(http.MethodDelete, "/v1/goals/"+testID+"/contributions/7", nil), 400, codeBadRequest},
		{"query parameter of the wrong type", httptest.NewRequest(http.MethodGet, "/v1/transactions?limit=lots", nil), 400, codeBadRequest},
		{"date query parameter malformed", httptest.NewRequest(http.MethodGet, "/v1/transactions?from=21-07-2025", nil), 400, codeBadRequest},
		{"required query parameter missing", httptest.NewRequest(http.MethodGet, "/v1/summary/stats", nil), 400, codeBadRequest},
		{"offset is not a number", httptest.NewRequest(http.MethodGet, "/v1/summary/stats?period=month&offset=last", nil), 400, codeBadRequest},
		{"idempotency key is not a uuid", func() *http.Request {
			req := jsonBody(http.MethodPost, "/v1/transactions", "{}")
			req.Header.Set("Idempotency-Key", "nope")
			return req
		}(), 400, codeBadRequest},
		{"malformed JSON body", jsonBody(http.MethodPost, "/v1/transactions", `{"kind": `), 400, codeBadRequest},
		{"body field of the wrong type", jsonBody(http.MethodPost, "/v1/transactions", `{"amount_minor": "ten"}`), 400, codeBadRequest},
		{"well-formed request reaches the implementation", jsonBody(http.MethodPost, "/v1/transactions", `{"kind":"expense","amount_minor":2440,"unknown_field":1}`), 501, "not_implemented"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireEnvelope(t, do(s, tt.req), tt.wantStatus, tt.wantCode)
		})
	}
}

// The three types below show how feature handlers are layered over
// NotImplemented: the fallback sits one embedding level deeper, so a
// handler's methods win without ambiguity.
type fallback struct{ NotImplemented }

type fakeMeHandler struct{}

func (fakeMeHandler) GetMe(context.Context, GetMeRequestObject) (GetMeResponseObject, error) {
	return GetMe200JSONResponse(Me{
		ID:        uuid.MustParse(testID),
		Name:      "Daisy Walker",
		FirstName: "Daisy",
		Email:     "daisy.walker@email.com",
		Currency:  "USD",
		Timezone:  "Europe/London",
		CreatedAt: time.Date(2025, time.July, 21, 17, 24, 0, 0, time.UTC),
	}), nil
}

func (fakeMeHandler) DeleteMe(context.Context, DeleteMeRequestObject) (DeleteMeResponseObject, error) {
	return DeleteMe204Response{}, nil
}

type fakeGoalsHandler struct{}

func (fakeGoalsHandler) GetGoal(_ context.Context, request GetGoalRequestObject) (GetGoalResponseObject, error) {
	if request.ID != uuid.MustParse(testID) {
		return nil, WithMessage(ErrNotFound, "That goal does not exist.")
	}
	return nil, &ValidationError{Fields: map[string]string{"id": "is the test id"}}
}

type composedAPI struct {
	fallback
	fakeMeHandler
	fakeGoalsHandler
}

func TestWithAPI_LayersHandlersOverNotImplemented(t *testing.T) {
	s, _ := newTestServer(t, WithAPI(composedAPI{}))

	t.Run("an implemented operation returns its response", func(t *testing.T) {
		rec := get(s, "/v1/me")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.NotEmpty(t, rec.Header().Get(requestIDHeader))
		assert.Contains(t, rec.Body.String(), `"first_name":"Daisy"`)
		assert.Contains(t, rec.Body.String(), `"created_at":"2025-07-21T17:24:00Z"`)
		assert.Contains(t, rec.Body.String(), `"avatar_url":null`, "a nil optional is sent as null, not omitted")
	})

	t.Run("a no-content operation", func(t *testing.T) {
		rec := do(s, httptest.NewRequest(http.MethodDelete, "/v1/me", nil))

		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Empty(t, rec.Body.String())
	})

	t.Run("an error from a handler goes through the Responder", func(t *testing.T) {
		rec := get(s, "/v1/goals/"+uuid.NewString())

		body := requireEnvelope(t, rec, http.StatusNotFound, codeNotFound)
		assert.Equal(t, "That goal does not exist.", body.Message)
	})

	t.Run("the path parameter reaches the handler", func(t *testing.T) {
		rec := get(s, "/v1/goals/"+testID)

		body := requireEnvelope(t, rec, http.StatusUnprocessableEntity, codeValidationFailed)
		assert.Equal(t, map[string]string{"id": "is the test id"}, body.Fields)
	})

	t.Run("everything else still answers 501", func(t *testing.T) {
		requireEnvelope(t, get(s, "/v1/home"), http.StatusNotImplemented, "not_implemented")
		requireEnvelope(t, do(s, httptest.NewRequest(http.MethodPatch, "/v1/me", strings.NewReader("{}"))), http.StatusNotImplemented, "not_implemented")
	})
}

func TestSpecEndpoint(t *testing.T) {
	t.Run("served when configured", func(t *testing.T) {
		s, _ := newTestServer(t, WithSpec(api.Spec))

		rec := get(s, specPath)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
		assert.Equal(t, string(api.Spec), rec.Body.String())
	})

	t.Run("absent otherwise", func(t *testing.T) {
		s, _ := newTestServer(t)

		requireEnvelope(t, get(s, specPath), http.StatusNotFound, codeNotFound)
		requireEnvelope(t, get(s, docsPath), http.StatusNotFound, codeNotFound)
	})

	t.Run("comes with a Swagger UI page that loads it", func(t *testing.T) {
		s, _ := newTestServer(t, WithSpec(api.Spec))

		rec := get(s, docsPath)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), `url: "/v1/openapi.yaml"`)
		assert.Equal(t, 2, strings.Count(rec.Body.String(), `integrity="sha384-`), "both assets are pinned")
	})
}

func TestHealthProbesStayOutsideTheAPI(t *testing.T) {
	s, _ := newTestServer(t)

	assert.Equal(t, http.StatusOK, get(s, healthzPath).Code)
	requireEnvelope(t, get(s, apiBasePath+healthzPath), http.StatusNotFound, codeNotFound)
}
