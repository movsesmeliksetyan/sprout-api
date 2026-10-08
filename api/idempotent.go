package api

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
)

// IdempotencyKeyHeader is the request header that makes a POST safe to
// repeat.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotentRoutes returns the route pattern, such as "/v1/transactions", of
// every POST in the spec that accepts the Idempotency-Key header. Declaring
// the header on an operation is what makes it idempotent.
func IdempotentRoutes() ([]string, error) {
	doc, err := openapi3.NewLoader().LoadFromData(Spec)
	if err != nil {
		return nil, fmt.Errorf("api: parse spec: %w", err)
	}
	if len(doc.Servers) != 1 {
		return nil, fmt.Errorf("api: spec has %d servers, want the one API root", len(doc.Servers))
	}
	base := doc.Servers[0].URL

	var routes []string
	for path, item := range doc.Paths.Map() {
		op := item.GetOperation(http.MethodPost)
		if op == nil {
			continue
		}
		for _, param := range op.Parameters {
			if p := param.Value; p != nil && p.In == openapi3.ParameterInHeader && p.Name == IdempotencyKeyHeader {
				routes = append(routes, base+path)
				break
			}
		}
	}
	sort.Strings(routes)
	return routes, nil
}
