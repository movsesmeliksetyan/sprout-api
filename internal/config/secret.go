package config

import "log/slog"

const redacted = "[redacted]"

// Secret holds a sensitive value. Every way of printing, logging or
// serialising it yields a placeholder; only Reveal returns the real value.
type Secret string

// Reveal returns the underlying value. Call it only where the secret is used.
func (s Secret) Reveal() string { return string(s) }

func (s Secret) mask() string {
	if s == "" {
		return ""
	}
	return redacted
}

// String implements fmt.Stringer.
func (s Secret) String() string { return s.mask() }

// GoString implements fmt.GoStringer, covering the %#v verb.
func (s Secret) GoString() string { return s.mask() }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.mask()) }

// MarshalText implements encoding.TextMarshaler, covering JSON and similar encoders.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.mask()), nil }
