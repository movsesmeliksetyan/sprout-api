package categorize

// Handler implements the categorisation operations of the API.
type Handler struct {
	engine *Engine
}

// NewHandler returns the handler for engine.
func NewHandler(engine *Engine) *Handler {
	return &Handler{engine: engine}
}
