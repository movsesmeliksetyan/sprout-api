package main

import (
	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
	"github.com/movsesmeliksetyan/sprout-api/internal/summary"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// Every feature calls its handler type Handler; embedding needs distinct
// field names.
type (
	usersAPI        = users.Handler
	uploadsAPI      = uploads.Handler
	categoriesAPI   = categories.Handler
	transactionsAPI = transactions.Handler
	summaryAPI      = summary.Handler
	goalsAPI        = goals.Handler
	categorizeAPI   = categorize.Handler
	importsAPI      = imports.Handler
	receiptsAPI     = receipts.Handler
)

// apiHandlers is the implementation of every /v1 operation: the feature
// handlers, over a 501 for the operations no feature provides yet.
type apiHandlers struct {
	*usersAPI
	*uploadsAPI
	*categoriesAPI
	*transactionsAPI
	*summaryAPI
	*goalsAPI
	*categorizeAPI
	*importsAPI
	*receiptsAPI
	unimplemented
}

// unimplemented puts httpx.NotImplemented one level below the feature
// handlers, so that their methods win.
type unimplemented struct{ httpx.NotImplemented }

var _ httpx.StrictServerInterface = apiHandlers{}

func newAPI(
	userService *users.Service,
	uploadService *uploads.Service,
	categoryService *categories.Service,
	transactionService *transactions.Service,
	summaryService *summary.Service,
	goalService *goals.Service,
	engine *categorize.Engine,
	importService *imports.Service,
	receiptService *receipts.Service,
) apiHandlers {
	return apiHandlers{
		usersAPI:        users.NewHandler(userService),
		uploadsAPI:      uploads.NewHandler(uploadService),
		categoriesAPI:   categories.NewHandler(categoryService),
		transactionsAPI: transactions.NewHandler(transactionService),
		summaryAPI:      summary.NewHandler(summaryService),
		goalsAPI:        goals.NewHandler(goalService),
		categorizeAPI:   categorize.NewHandler(engine),
		importsAPI:      imports.NewHandler(importService),
		receiptsAPI:     receipts.NewHandler(receiptService),
	}
}
