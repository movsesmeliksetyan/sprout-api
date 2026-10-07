package main

import (
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// apiHandlers is the implementation of every /v1 operation: the feature
// handlers, over a 501 for the operations no feature provides yet.
type apiHandlers struct {
	*users.Handler
	unimplemented
}

// unimplemented puts httpx.NotImplemented one level below the feature
// handlers, so that their methods win.
type unimplemented struct{ httpx.NotImplemented }

var _ httpx.StrictServerInterface = apiHandlers{}

func newAPI() apiHandlers {
	return apiHandlers{Handler: users.NewHandler()}
}
