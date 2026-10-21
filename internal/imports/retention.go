package imports

import (
	"context"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
)

// purgePage is how many statement files are deleted a query.
const purgePage = 100

// PurgeFiles deletes the statement files whose time has come (PRD §4.5:
// AbandonedFileTTL after an import is created, ClosedFileTTL after its
// commit or discard) from storage, and forgets the raw cells of their
// imports' rows. It returns how many files went. A file is marked gone
// only once storage has deleted it, so a failure is retried on the next
// run.
func PurgeFiles(ctx context.Context, q db.Querier, store storage.Store, now time.Time) (int, error) {
	purged := 0
	for {
		due, err := q.ListUploadsToPurge(ctx, db.ListUploadsToPurgeParams{Now: &now, RowLimit: purgePage})
		if err != nil {
			return purged, fmt.Errorf("imports: files to purge: %w", err)
		}
		for _, upload := range due {
			if err := store.Delete(ctx, upload.ObjectKey); err != nil {
				return purged, fmt.Errorf("imports: delete statement file: %w", err)
			}
			if err := q.MarkUploadPurged(ctx, db.MarkUploadPurgedParams{ID: upload.ID, Now: &now}); err != nil {
				return purged, fmt.Errorf("imports: purge: %w", err)
			}
			purged++
		}
		if len(due) < purgePage {
			return purged, nil
		}
	}
}
