package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

// Mapping is how the user says to read a statement (contract §2.10).
type Mapping struct {
	Columns    []statements.ColumnRole
	DateFormat string
	AmountSign statements.AmountSign
}

// userRoles are the roles a user may give a column.
var userRoles = map[string]bool{
	"date": true, "description": true, "amount": true, "debit": true, "credit": true,
	"balance": true, "currency": true, "reference": true, "mcc": true, "ignore": true,
}

// SetMapping reads an import that needs a mapping the way the user says:
// the mapping is checked against the statement's first rows, saved as the
// template for its headers — a user's template wins over every other, so
// the next file with these headers needs no mapping — and the import is
// processed again.
func (s *Service) SetMapping(ctx context.Context, userID, id uuid.UUID, mapping Mapping) (db.Import, error) {
	var updated db.Import
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		found, err := q.GetImportForUpdate(ctx, db.GetImportForUpdateParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("imports: mapping: %w", err)
		}
		if Status(found.Status) != StatusNeedsMapping || len(found.MappingRequest) == 0 || found.HeaderFingerprint == nil {
			return httpx.ErrConflict
		}
		var request statements.MappingRequest
		if err := json.Unmarshal(found.MappingRequest, &request); err != nil {
			return fmt.Errorf("imports: mapping request: %w", err)
		}
		for _, c := range mapping.Columns {
			if !userRoles[c.Role] {
				return &httpx.ValidationError{Fields: map[string]string{"columns": fmt.Sprintf("role %q is not known", c.Role)}}
			}
		}
		if mapping.DateFormat == "" && request.Suggested.DateFormat != nil {
			mapping.DateFormat = *request.Suggested.DateFormat
		}
		sample := request.SampleTable()
		spec, err := statements.SpecFromRoles(sample, mapping.Columns, mapping.DateFormat, mapping.AmountSign)
		if err == nil {
			err = spec.Check(sample, 0)
		}
		if err != nil {
			return &httpx.ValidationError{Message: "This mapping does not read the statement: " + err.Error()}
		}
		format := statements.FormatCSV
		if found.Format != nil {
			format = statements.Format(*found.Format)
		}
		if err := statements.NewResolver(q).Save(ctx, *found.HeaderFingerprint, format, nil, spec, statements.SourceUser); err != nil {
			return err
		}
		if err := q.StartImportProcessing(ctx, id); err != nil {
			return fmt.Errorf("imports: mapping: %w", err)
		}
		if err := s.jobs.EnqueueImport(ctx, tx, id); err != nil {
			return err
		}
		updated, err = q.GetImport(ctx, db.GetImportParams{ID: id, UserID: userID})
		return err
	})
	if err != nil {
		return db.Import{}, err
	}
	return updated, nil
}
