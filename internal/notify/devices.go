package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

// Environment is the APNs environment that issued a device token.
type Environment string

// The environments.
const (
	EnvironmentSandbox    Environment = "sandbox"
	EnvironmentProduction Environment = "production"
)

const (
	// minTokenLength and maxTokenLength bound an APNs token in hex: 32
	// bytes today, allowed to grow.
	minTokenLength = 64
	maxTokenLength = 200
	// maxAppVersionLength and maxLocaleLength bound the free-text fields.
	maxAppVersionLength = 32
	maxLocaleLength     = 35
)

// Database is what the service needs from the pool: queries, and
// transactions for the writes.
type Database interface {
	db.DBTX
	db.Beginner
}

// Registration is what a device sends to receive pushes.
type Registration struct {
	APNsToken   string
	Environment Environment
	AppVersion  *string
	Locale      *string
}

// RegisterDevice stores, or refreshes, the user's device (PRD BE-55). The
// token is taken from any other device that held it, of this user or
// another, since a phone passed on or signed into another account keeps
// its token. Registering again clears an invalidated token.
func (s *Service) RegisterDevice(ctx context.Context, userID, deviceID uuid.UUID, reg Registration) (db.Device, error) {
	reg.APNsToken = strings.ToLower(strings.TrimSpace(reg.APNsToken))
	if err := checkRegistration(&reg); err != nil {
		return db.Device{}, err
	}
	var device db.Device
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.ReleaseAPNsToken(ctx, db.ReleaseAPNsTokenParams{ApnsToken: reg.APNsToken, UserID: userID, DeviceID: deviceID}); err != nil {
			return fmt.Errorf("notify: release token: %w", err)
		}
		var err error
		device, err = q.UpsertDevice(ctx, db.UpsertDeviceParams{
			ID: uuid.Must(uuid.NewV7()), UserID: userID, DeviceID: deviceID, ApnsToken: reg.APNsToken,
			Environment: string(reg.Environment), AppVersion: reg.AppVersion, Locale: reg.Locale,
		})
		if err != nil {
			return fmt.Errorf("notify: upsert device: %w", err)
		}
		return nil
	})
	return device, err
}

// DeleteDevice removes the user's device, on logout. One that is missing
// or someone else's is httpx.ErrNotFound.
func (s *Service) DeleteDevice(ctx context.Context, userID, deviceID uuid.UUID) error {
	n, err := db.New(s.db).DeleteDevice(ctx, db.DeleteDeviceParams{UserID: userID, DeviceID: deviceID})
	if err != nil {
		return fmt.Errorf("notify: delete device: %w", err)
	}
	if n == 0 {
		return httpx.ErrNotFound
	}
	return nil
}

func checkRegistration(reg *Registration) error {
	fields := map[string]string{}
	if n := len(reg.APNsToken); n < minTokenLength || n > maxTokenLength || n%2 != 0 || strings.Trim(reg.APNsToken, "0123456789abcdef") != "" {
		fields["apns_token"] = "must be the device token in hex"
	}
	if reg.Environment != EnvironmentSandbox && reg.Environment != EnvironmentProduction {
		fields["environment"] = "must be sandbox or production"
	}
	reg.AppVersion = trimmed(reg.AppVersion)
	if reg.AppVersion != nil && len(*reg.AppVersion) > maxAppVersionLength {
		fields["app_version"] = fmt.Sprintf("must be at most %d characters", maxAppVersionLength)
	}
	reg.Locale = trimmed(reg.Locale)
	if reg.Locale != nil && len(*reg.Locale) > maxLocaleLength {
		fields["locale"] = fmt.Sprintf("must be at most %d characters", maxLocaleLength)
	}
	if len(fields) > 0 {
		return &httpx.ValidationError{Fields: fields}
	}
	return nil
}

// trimmed is s without surrounding spaces; nil when nothing is left.
func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
