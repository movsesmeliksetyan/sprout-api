package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	defaultRequestTimeout = 30 * time.Second
	defaultMaxBodyBytes   = 1 << 20
	defaultCheckTimeout   = 2 * time.Second
	gzipLevel             = 5

	apiBasePath = "/v1"
	specPath    = apiBasePath + "/openapi.yaml"
)

// Server is the HTTP API: a router with the standard middleware stack and
// the health endpoints.
type Server struct {
	logger        *slog.Logger
	router        chi.Router
	responder     *Responder
	api           StrictServerInterface
	apiMiddleware []func(http.Handler) http.Handler
	spec          []byte
	checks        []namedCheck

	requestTimeout time.Duration
	drainTimeout   time.Duration
	checkTimeout   time.Duration
	maxBodyBytes   int64
}

// Option customises a Server.
type Option func(*Server)

// WithReadinessCheck adds a dependency check to /readyz.
func WithReadinessCheck(name string, check Check) Option {
	return func(s *Server) { s.checks = append(s.checks, namedCheck{name: name, check: check}) }
}

// WithRequestTimeout sets the per-request deadline (default 30 s).
func WithRequestTimeout(d time.Duration) Option {
	return func(s *Server) { s.requestTimeout = d }
}

// WithDrainTimeout sets how long Run waits for in-flight requests on
// shutdown (default: the request timeout).
func WithDrainTimeout(d time.Duration) Option {
	return func(s *Server) { s.drainTimeout = d }
}

// WithMaxBodyBytes sets the request body limit (default 1 MB).
func WithMaxBodyBytes(n int64) Option {
	return func(s *Server) { s.maxBodyBytes = n }
}

// WithAPI sets the implementation of the /v1 operations. The default answers
// every one of them with 501.
func WithAPI(api StrictServerInterface) Option {
	return func(s *Server) { s.api = api }
}

// WithAPIMiddleware runs the given middleware, first one outermost, around
// every /v1 operation and before its parameters are read. The health
// endpoints and the OpenAPI document are not covered.
func WithAPIMiddleware(middleware ...func(http.Handler) http.Handler) Option {
	return func(s *Server) { s.apiMiddleware = append(s.apiMiddleware, middleware...) }
}

// WithSpec serves the OpenAPI document at /v1/openapi.yaml and a Swagger UI
// page for it at /v1/docs. Without it neither path exists.
func WithSpec(spec []byte) Option {
	return func(s *Server) { s.spec = spec }
}

// NewServer builds the router. Routes are added through Router.
func NewServer(logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		logger:         logger,
		responder:      NewResponder(logger),
		api:            NotImplemented{},
		requestTimeout: defaultRequestTimeout,
		checkTimeout:   defaultCheckTimeout,
		maxBodyBytes:   defaultMaxBodyBytes,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.drainTimeout == 0 {
		// Long enough for any request that is still within its own deadline.
		s.drainTimeout = s.requestTimeout
	}

	r := chi.NewRouter()
	r.Use(
		requestID,
		accessLog(logger),
		middleware.Compress(gzipLevel, "application/json"),
		recoverer(logger),
		timeout(s.requestTimeout, s.responder),
		bodyLimit(s.maxBodyBytes, s.responder),
	)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.responder.Error(w, r, ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		s.responder.Error(w, r, errMethodNotAllowed)
	})
	r.Get(healthzPath, handleHealthz)
	r.Get(readyzPath, s.handleReadyz)
	s.mountAPI(r)

	s.router = r
	return s
}

// mountAPI registers every operation in api/openapi.yaml under /v1. Failures
// to bind parameters or decode a body, and errors returned by the
// implementation, all leave through the Responder.
func (s *Server) mountAPI(r chi.Router) {
	strict := NewStrictHandlerWithOptions(s.api, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  s.responder.RequestError,
		ResponseErrorHandlerFunc: s.responder.Error,
	})
	r.Group(func(r chi.Router) {
		r.Use(s.apiMiddleware...)
		HandlerWithOptions(strict, ChiServerOptions{
			BaseURL:          apiBasePath,
			BaseRouter:       r,
			ErrorHandlerFunc: s.responder.RequestError,
		})
	})

	if s.spec != nil {
		r.Get(specPath, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(s.spec)
		})
		r.Get(docsPath, handleDocs)
	}
}

// Router exposes the router so feature packages can mount their routes.
func (s *Server) Router() chi.Router { return s.router }

// Responder returns the error writer shared by every handler on this server.
func (s *Server) Responder() *Responder { return s.responder }

// Handler returns the server as an http.Handler.
func (s *Server) Handler() http.Handler { return s.router }

// Run serves on ln until ctx is cancelled, then stops accepting connections
// and waits for in-flight requests to finish. It returns nil after a clean
// drain and an error if serving failed or the drain window ran out.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       s.requestTimeout,
		WriteTimeout:      s.requestTimeout + 5*time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	s.logger.Info("shutting down", slog.String("drain_timeout", s.drainTimeout.String()))

	// ctx is already cancelled; the drain needs its own deadline.
	drainCtx, cancel := context.WithTimeout(context.Background(), s.drainTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
