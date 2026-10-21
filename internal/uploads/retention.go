package uploads

import (
	"context"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
)

// purgePage is how many files are deleted a query.
const purgePage = 100

// PurgeFiles deletes from storage the uploaded files whose purge_after has
// come — statements after their import's retention (PRD §4.5), receipt
// photos never confirmed or discarded — and forgets the raw cells of the
// rows of their imports. It returns how many files went. A file is marked
// gone only once storage has deleted it, so a failure is retried on the
// next run.
func PurgeFiles(ctx context.Context, q db.Querier, store storage.Store, now time.Time) (int, error) {
	purged := 0
	for {
		due, err := q.ListUploadsToPurge(ctx, db.ListUploadsToPurgeParams{Now: &now, RowLimit: purgePage})
		if err != nil {
			return purged, fmt.Errorf("uploads: files to purge: %w", err)
		}
		for _, upload := range due {
			if err := store.Delete(ctx, upload.ObjectKey); err != nil {
				return purged, fmt.Errorf("uploads: delete file: %w", err)
			}
			if err := q.MarkUploadPurged(ctx, db.MarkUploadPurgedParams{ID: upload.ID, Now: &now}); err != nil {
				return purged, fmt.Errorf("uploads: purge: %w", err)
			}
			purged++
		}
		if len(due) < purgePage {
			return purged, nil
		}
	}
}
