package imports

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

// Handler implements the import operations of the API.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for service.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// CreateImport starts importing a statement upload.
func (h *Handler) CreateImport(ctx context.Context, request httpx.CreateImportRequestObject) (httpx.CreateImportResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	created, err := h.service.Create(ctx, user.ID, request.Body.UploadID, string(request.Body.Period))
	if err != nil {
		return nil, err
	}
	out, err := ToAPI(created)
	if err != nil {
		return nil, err
	}
	return httpx.CreateImport202JSONResponse(out), nil
}

// GetImport returns an import, to poll while it is processed.
func (h *Handler) GetImport(ctx context.Context, request httpx.GetImportRequestObject) (httpx.GetImportResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	found, err := h.service.Get(ctx, user.ID, request.ID)
	if err != nil {
		return nil, err
	}
	out, err := ToAPI(found)
	if err != nil {
		return nil, err
	}
	return httpx.GetImport200JSONResponse(out), nil
}

// DeleteImport discards an import that has not been committed.
func (h *Handler) DeleteImport(ctx context.Context, request httpx.DeleteImportRequestObject) (httpx.DeleteImportResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if err := h.service.Delete(ctx, user.ID, request.ID); err != nil {
		return nil, err
	}
	return httpx.DeleteImport204Response{}, nil
}

// SetImportMapping says how to read a statement that needs a mapping.
func (h *Handler) SetImportMapping(ctx context.Context, request httpx.SetImportMappingRequestObject) (httpx.SetImportMappingResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	mapping := Mapping{AmountSign: statements.NegativeIsExpense}
	for _, c := range request.Body.Columns {
		mapping.Columns = append(mapping.Columns, statements.ColumnRole{Index: c.Index, Role: string(c.Role)})
	}
	if request.Body.DateFormat != nil {
		mapping.DateFormat = *request.Body.DateFormat
	}
	if request.Body.AmountSign != nil {
		mapping.AmountSign = statements.AmountSign(*request.Body.AmountSign)
	}
	updated, err := h.service.SetMapping(ctx, user.ID, request.ID, mapping)
	if err != nil {
		return nil, err
	}
	out, err := ToAPI(updated)
	if err != nil {
		return nil, err
	}
	return httpx.SetImportMapping202JSONResponse(out), nil
}

// ToAPI is an import on the wire.
func ToAPI(row db.Import) (httpx.Import, error) {
	summary, err := summaryOf(row)
	if err != nil {
		return httpx.Import{}, err
	}
	out := httpx.Import{
		ID:        row.ID,
		Status:    httpx.ImportStatus(row.Status),
		Filename:  row.Filename,
		Period:    httpx.ImportPeriod(row.Period),
		CreatedAt: row.CreatedAt,
		Detected: httpx.ImportDetected{
			Format:        row.Format,
			BankHint:      row.BankHint,
			MappingSource: (*httpx.MappingSource)(row.MappingSource),
		},
		Summary: httpx.ImportSummary{
			RowCount:           summary.RowCount,
			IncludedCount:      summary.IncludedCount,
			TotalExpenseMinor:  summary.TotalExpenseMinor,
			TotalIncomeMinor:   summary.TotalIncomeMinor,
			DuplicateCount:     summary.DuplicateCount,
			UncategorizedCount: summary.UncategorizedCount,
			ErrorCount:         summary.ErrorCount,
			OutOfPeriodCount:   summary.OutOfPeriodCount,
		},
	}
	if len(row.Failure) > 0 {
		var failure Failure
		if err := json.Unmarshal(row.Failure, &failure); err != nil {
			return httpx.Import{}, fmt.Errorf("imports: failure: %w", err)
		}
		out.Failure = &httpx.ImportFailure{Code: httpx.ImportFailureCode(failure.Code), Message: failure.Message}
	}
	if len(row.MappingRequest) > 0 {
		var request httpx.MappingRequest
		if err := json.Unmarshal(row.MappingRequest, &request); err != nil {
			return httpx.Import{}, fmt.Errorf("imports: mapping request: %w", err)
		}
		out.MappingRequest = &request
	}
	return out, nil
}
