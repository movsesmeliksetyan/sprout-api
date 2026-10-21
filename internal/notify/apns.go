package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

// ErrTokenInvalid is what a Pusher returns when APNs says a device token
// is no longer valid: 410 Unregistered, or BadDeviceToken. The device is
// not pushed to again until it registers anew.
var ErrTokenInvalid = errors.New("notify: device token is no longer valid")

// Push is one notification on its way to one device.
type Push struct {
	Token       string
	Environment Environment
	Title       string
	Body        string
	// NotificationID and DeepLink go in the payload's custom data
	// (contract §2.12).
	NotificationID string
	DeepLink       string
}

// Pusher delivers pushes. APNs is the real one; FakeAPNs stands in for it
// in tests.
type Pusher interface {
	Push(ctx context.Context, p Push) error
}

// APNsConfig is what token-based APNs delivery needs (config.APNS).
type APNsConfig struct {
	KeyID    string
	TeamID   string
	BundleID string
	KeyPath  string
}

// APNs delivers through Apple's push service over HTTP/2, signing with the
// team's .p8 key. Each device goes to the environment its token came from.
type APNs struct {
	topic      string
	sandbox    *apns2.Client
	production *apns2.Client
}

// NewAPNs reads the .p8 key and returns a pusher for the app's bundle.
func NewAPNs(cfg APNsConfig) (*APNs, error) {
	key, err := token.AuthKeyFromFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("notify: apns key: %w", err)
	}
	t := &token.Token{AuthKey: key, KeyID: cfg.KeyID, TeamID: cfg.TeamID}
	return &APNs{
		topic:      cfg.BundleID,
		sandbox:    apns2.NewTokenClient(t).Development(),
		production: apns2.NewTokenClient(t).Production(),
	}, nil
}

// Push sends one alert.
func (a *APNs) Push(ctx context.Context, p Push) error {
	client := a.production
	if p.Environment == EnvironmentSandbox {
		client = a.sandbox
	}
	res, err := client.PushWithContext(ctx, &apns2.Notification{
		DeviceToken: p.Token,
		Topic:       a.topic,
		PushType:    apns2.PushTypeAlert,
		Priority:    apns2.PriorityHigh,
		Payload: payload.NewPayload().AlertTitle(p.Title).AlertBody(p.Body).Sound("default").
			Custom("notification_id", p.NotificationID).Custom("deep_link", p.DeepLink),
	})
	if err != nil {
		return fmt.Errorf("notify: apns: %w", err)
	}
	switch {
	case res.Sent():
		return nil
	case res.StatusCode == http.StatusGone, res.Reason == apns2.ReasonBadDeviceToken, res.Reason == apns2.ReasonUnregistered:
		return ErrTokenInvalid
	default:
		return fmt.Errorf("notify: apns refused the push: %d %s", res.StatusCode, res.Reason)
	}
}

// FakeAPNs records pushes instead of sending them. A token in Invalid is
// answered with ErrTokenInvalid and one in Failing with Failing's error.
type FakeAPNs struct {
	mu      sync.Mutex
	pushed  []Push
	Invalid map[string]bool
	Failing map[string]error
}

// Push records p, or refuses it as set up.
func (f *FakeAPNs) Push(_ context.Context, p Push) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Invalid[p.Token] {
		return ErrTokenInvalid
	}
	if err := f.Failing[p.Token]; err != nil {
		return err
	}
	f.pushed = append(f.pushed, p)
	return nil
}

// Pushed returns what was delivered, in order.
func (f *FakeAPNs) Pushed() []Push {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Push(nil), f.pushed...)
}

// Discard drops every push, for development without APNs keys; the inbox
// still fills.
type Discard struct{}

// Push does nothing.
func (Discard) Push(context.Context, Push) error { return nil }
