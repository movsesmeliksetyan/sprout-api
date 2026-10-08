package main

import (
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// Every feature calls its handler type Handler; embedding needs distinct
// field names.
type (
	usersAPI   = users.Handler
	uploadsAPI = uploads.Handler
)

// apiHandlers is the implementation of every /v1 operation: the feature
// handlers, over a 501 for the operations no feature provides yet.
type apiHandlers struct {
	*usersAPI
	*uploadsAPI
	unimplemented
}

// unimplemented puts httpx.NotImplemented one level below the feature
// handlers, so that their methods win.
type unimplemented struct{ httpx.NotImplemented }

var _ httpx.StrictServerInterface = apiHandlers{}

func newAPI(userService *users.Service, uploadService *uploads.Service) apiHandlers {
	return apiHandlers{
		usersAPI:   users.NewHandler(userService),
		uploadsAPI: uploads.NewHandler(uploadService),
	}
}
