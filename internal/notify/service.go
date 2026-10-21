package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

// Kind is why a notification was sent (contract §2.12).
type Kind string

// The kinds.
const (
	KindBudgetAlert   Kind = "budget_alert"
	KindWeeklyRecap   Kind = "weekly_recap"
	KindGoalCompleted Kind = "goal_completed"
	KindImportReady   Kind = "import_ready"
)

// DefaultLimit and MaxLimit bound a page of the inbox.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Enqueuer starts delivering a notification in the background, in the
// transaction that stores it.
type Enqueuer interface {
	EnqueuePush(ctx context.Context, tx pgx.Tx, userID, notificationID uuid.UUID) error
}

// Service manages the user's devices and notifications.
type Service struct {
	db     Database
	jobs   Enqueuer
	pusher Pusher
}

// NewService returns a service over the database that enqueues pushes
// through jobs.
func NewService(database Database, jobs Enqueuer, opts ...Option) *Service {
	s := &Service{db: database, jobs: jobs}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option configures a Service.
type Option func(*Service)

// WithPusher sets what delivers pushes, for the worker. Without it Deliver
// fails.
func WithPusher(p Pusher) Option {
	return func(s *Service) { s.pusher = p }
}

// Notification is what to tell the user. DedupKey names the event: a
// second notification with the same key is dropped.
type Notification struct {
	Kind     Kind
	Title    string
	Body     string
	DeepLink string
	DedupKey string
}

// Send stores n in the user's inbox and, when the user wants pushes of its
// kind, enqueues its delivery (PRD BE-56). It reports whether n was new;
// one with a dedup_key the user already has changes nothing.
func (s *Service) Send(ctx context.Context, userID uuid.UUID, n Notification) (bool, error) {
	var created bool
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		created, err = s.SendTx(ctx, tx, userID, n)
		return err
	})
	return created, err
}

// SendTx is Send inside tx, for a change that notifies as it commits.
func (s *Service) SendTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, n Notification) (bool, error) {
	q := db.New(tx)
	stored, err := q.CreateNotification(ctx, db.CreateNotificationParams{
		ID: uuid.Must(uuid.NewV7()), UserID: userID, Kind: string(n.Kind),
		Title: n.Title, Body: n.Body, DeepLink: n.DeepLink, DedupKey: n.DedupKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("notify: store: %w", err)
	}
	user, err := q.GetUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("notify: user: %w", err)
	}
	if !wants(user, n.Kind) {
		return true, nil
	}
	if err := s.jobs.EnqueuePush(ctx, tx, userID, stored.ID); err != nil {
		return false, fmt.Errorf("notify: enqueue push: %w", err)
	}
	return true, nil
}

// wants reports whether the user's preferences let a notification of the
// kind be pushed. The inbox keeps it either way.
func wants(user db.User, kind Kind) bool {
	if !user.NotificationsEnabled {
		return false
	}
	switch kind {
	case KindBudgetAlert:
		return user.BudgetAlerts
	case KindWeeklyRecap:
		return user.WeeklyRecap
	default:
		return true
	}
}

// Delivery counts what became of a notification's pushes.
type Delivery struct {
	Delivered   int
	Invalidated int
	Failed      int
}

// Deliver pushes a stored notification to every device of the user APNs
// has not rejected, the work of the send_push job. A token APNs calls
// invalid is retired. It fails, for the job to retry, only when no device
// took the push and at least one failed for another reason; a notification
// delivered once is not delivered again. Preferences are read again, so
// turning pushes off stops those still queued.
func (s *Service) Deliver(ctx context.Context, userID, notificationID uuid.UUID) (Delivery, error) {
	if s.pusher == nil {
		return Delivery{}, errors.New("notify: no pusher configured")
	}
	q := db.New(s.db)
	n, err := q.GetNotification(ctx, db.GetNotificationParams{ID: notificationID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, nil
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("notify: notification: %w", err)
	}
	if n.SentAt != nil {
		return Delivery{}, nil
	}
	user, err := q.GetUser(ctx, userID)
	if err != nil {
		return Delivery{}, fmt.Errorf("notify: user: %w", err)
	}
	if !wants(user, Kind(n.Kind)) {
		return Delivery{}, nil
	}
	devices, err := q.ListPushDevices(ctx, userID)
	if err != nil {
		return Delivery{}, fmt.Errorf("notify: devices: %w", err)
	}

	var d Delivery
	var failures []error
	for _, device := range devices {
		err := s.pusher.Push(ctx, Push{
			Token: device.ApnsToken, Environment: Environment(device.Environment),
			Title: n.Title, Body: n.Body, NotificationID: n.ID.String(), DeepLink: n.DeepLink,
		})
		switch {
		case err == nil:
			d.Delivered++
		case errors.Is(err, ErrTokenInvalid):
			d.Invalidated++
			if err := q.InvalidateDevice(ctx, db.InvalidateDeviceParams{ID: device.ID, UserID: userID, ApnsToken: device.ApnsToken}); err != nil {
				return d, fmt.Errorf("notify: invalidate device: %w", err)
			}
		default:
			d.Failed++
			failures = append(failures, err)
		}
	}
	if d.Delivered == 0 {
		return d, errors.Join(failures...)
	}
	if err := q.MarkNotificationSent(ctx, db.MarkNotificationSentParams{ID: n.ID, UserID: userID}); err != nil {
		return d, fmt.Errorf("notify: mark sent: %w", err)
	}
	return d, nil
}

// Page is one page of the inbox, newest first.
type Page struct {
	Items []db.Notification
	// Next is where the following page starts; nil on the last page.
	Next *httpx.Cursor
}

// List returns a page of the user's inbox, newest first. A nil limit is
// DefaultLimit, and a nil after starts at the newest notification. The
// cursor's date is the notification's day; the id alone places it.
func (s *Service) List(ctx context.Context, userID uuid.UUID, limit *int, after *httpx.Cursor) (Page, error) {
	size := DefaultLimit
	if limit != nil {
		size = *limit
	}
	if size < 1 || size > MaxLimit {
		return Page{}, httpx.WithMessage(httpx.ErrBadRequest, fmt.Sprintf("The limit must be between 1 and %d.", MaxLimit))
	}
	params := db.ListNotificationsParams{UserID: userID, RowLimit: int32(size) + 1}
	if after != nil {
		params.CursorID = &after.ID
	}
	items, err := db.New(s.db).ListNotifications(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("notify: list: %w", err)
	}
	page := Page{Items: items}
	if len(items) > size {
		page.Items = items[:size]
		last := page.Items[size-1]
		y, m, d := last.CreatedAt.UTC().Date()
		page.Next = &httpx.Cursor{LocalDate: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), ID: last.ID}
	}
	return page, nil
}

// MarkRead marks the given notifications of the user read, or all of them.
// Exactly one of ids and all is expected; an id that is not the user's is
// httpx.ErrNotFound and marks nothing.
func (s *Service) MarkRead(ctx context.Context, userID uuid.UUID, ids []uuid.UUID, all bool) error {
	switch {
	case all && len(ids) > 0:
		return &httpx.ValidationError{Fields: map[string]string{"ids": "send either ids or all, not both"}}
	case all:
		if err := db.New(s.db).MarkAllNotificationsRead(ctx, userID); err != nil {
			return fmt.Errorf("notify: mark all read: %w", err)
		}
		return nil
	case len(ids) == 0:
		return &httpx.ValidationError{Fields: map[string]string{"ids": "send the ids to mark read, or all"}}
	case len(ids) > MaxLimit:
		return &httpx.ValidationError{Fields: map[string]string{"ids": fmt.Sprintf("must be at most %d", MaxLimit)}}
	}
	unique := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		owned, err := q.CountNotifications(ctx, db.CountNotificationsParams{UserID: userID, Ids: unique})
		if err != nil {
			return fmt.Errorf("notify: count: %w", err)
		}
		if int(owned) != len(unique) {
			return httpx.ErrNotFound
		}
		if err := q.MarkNotificationsRead(ctx, db.MarkNotificationsReadParams{UserID: userID, Ids: unique}); err != nil {
			return fmt.Errorf("notify: mark read: %w", err)
		}
		return nil
	})
}
