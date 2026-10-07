package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Env is the deployment environment the process runs in.
type Env string

// Deployment environments.
const (
	EnvDev     Env = "dev"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

const defaultHTTPAddr = ":8080"

// Config is the full, validated process configuration.
type Config struct {
	Env         Env
	LogLevel    slog.Level
	HTTPAddr    string
	DatabaseURL Secret
	Auth0       Auth0
	S3          S3
	LLM         LLM
	APNS        APNS
}

// Auth0 identifies the tenant and API whose access tokens are accepted.
type Auth0 struct {
	Domain   string
	Audience string
}

// S3 configures the S3-compatible object store. Endpoint is empty for AWS
// and set for MinIO.
type S3 struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey Secret
	UsePathStyle    bool
}

// LLM configures the language-model provider.
type LLM struct {
	Provider    string
	APIKey      Secret
	ModelText   string
	ModelVision string
}

// APNS configures token-based Apple push delivery. KeyPath points at the .p8 file.
type APNS struct {
	KeyID    string
	TeamID   string
	BundleID string
	KeyPath  string
}

// LogValue implements slog.LogValuer so a Config can be logged as one group.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", string(c.Env)),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("http_addr", c.HTTPAddr),
		slog.Any("database_url", c.DatabaseURL),
		slog.Group("auth0",
			slog.String("domain", c.Auth0.Domain),
			slog.String("audience", c.Auth0.Audience),
		),
		slog.Group("s3",
			slog.String("endpoint", c.S3.Endpoint),
			slog.String("region", c.S3.Region),
			slog.String("bucket", c.S3.Bucket),
			slog.String("access_key_id", c.S3.AccessKeyID),
			slog.Any("secret_access_key", c.S3.SecretAccessKey),
			slog.Bool("use_path_style", c.S3.UsePathStyle),
		),
		slog.Group("llm",
			slog.String("provider", c.LLM.Provider),
			slog.Any("api_key", c.LLM.APIKey),
			slog.String("model_text", c.LLM.ModelText),
			slog.String("model_vision", c.LLM.ModelVision),
		),
		slog.Group("apns",
			slog.String("key_id", c.APNS.KeyID),
			slog.String("team_id", c.APNS.TeamID),
			slog.String("bundle_id", c.APNS.BundleID),
			slog.String("key_path", c.APNS.KeyPath),
		),
	)
}

// FieldError describes what is wrong with one configuration key.
type FieldError struct {
	Key     string
	Problem string
}

// ValidationError lists every configuration problem found by Load.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid configuration:")
	for _, f := range e.Fields {
		fmt.Fprintf(&b, "\n  %s: %s", f.Key, f.Problem)
	}
	return b.String()
}

// Keys returns the offending keys in the order they were checked.
func (e *ValidationError) Keys() []string {
	keys := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		keys[i] = f.Key
	}
	return keys
}

// LoadDotEnv copies the variables in the file at path into the process
// environment, leaving variables that are already set untouched. A missing
// file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	vars, err := godotenv.Parse(f)
	if err != nil {
		// The parser's message can quote the offending line, which may hold a secret.
		return fmt.Errorf("%s: could not be parsed", path)
	}
	for key, value := range vars {
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s: set %s: %w", path, key, err)
		}
	}
	return nil
}

// Load reads the configuration through lookup (os.LookupEnv in production)
// and validates it. Every problem is reported at once in a *ValidationError.
func Load(lookup func(string) (string, bool)) (Config, error) {
	l := &loader{lookup: lookup}

	cfg := Config{
		Env:         l.env(),
		LogLevel:    l.logLevel(),
		HTTPAddr:    l.httpAddr(),
		DatabaseURL: l.databaseURL(),
		Auth0: Auth0{
			Domain:   l.auth0Domain(),
			Audience: l.required("AUTH0_AUDIENCE"),
		},
	}

	// Keys that are only needed by features built later may be left empty in
	// dev. With an unusable ENV the rule is unknown, so they are not demanded.
	strict := cfg.Env == EnvStaging || cfg.Env == EnvProd

	cfg.S3 = S3{
		Endpoint:        l.s3Endpoint(),
		Region:          l.required("S3_REGION"),
		Bucket:          l.required("S3_BUCKET"),
		AccessKeyID:     l.required("S3_ACCESS_KEY_ID"),
		SecretAccessKey: Secret(l.required("S3_SECRET_ACCESS_KEY")),
		UsePathStyle:    l.boolean("S3_USE_PATH_STYLE", false),
	}
	cfg.LLM = LLM{
		Provider:    l.llmProvider(),
		APIKey:      Secret(l.requiredIf(strict, "LLM_API_KEY")),
		ModelText:   l.required("LLM_MODEL_TEXT"),
		ModelVision: l.required("LLM_MODEL_VISION"),
	}
	cfg.APNS = l.apns(strict)

	if len(l.errs) > 0 {
		return Config{}, &ValidationError{Fields: l.errs}
	}
	return cfg, nil
}

type loader struct {
	lookup func(string) (string, bool)
	errs   []FieldError
}

func (l *loader) get(key string) string {
	v, _ := l.lookup(key)
	return strings.TrimSpace(v)
}

func (l *loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, FieldError{Key: key, Problem: fmt.Sprintf(format, args...)})
}

func (l *loader) required(key string) string {
	return l.requiredIf(true, key)
}

func (l *loader) requiredIf(cond bool, key string) string {
	v := l.get(key)
	if v == "" && cond {
		l.fail(key, "required")
	}
	return v
}

func (l *loader) boolean(key string, def bool) bool {
	v := l.get(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "must be true or false (got %q)", v)
		return def
	}
	return b
}

func (l *loader) env() Env {
	const key = "ENV"
	v := l.required(key)
	switch Env(v) {
	case EnvDev, EnvStaging, EnvProd:
		return Env(v)
	case "":
		return ""
	}
	l.fail(key, "must be one of dev, staging, prod (got %q)", v)
	return ""
}

func (l *loader) logLevel() slog.Level {
	const key = "LOG_LEVEL"
	switch v := strings.ToLower(l.get(key)); v {
	case "", "info":
		return slog.LevelInfo
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		l.fail(key, "must be one of debug, info, warn, error (got %q)", v)
		return slog.LevelInfo
	}
}

func (l *loader) httpAddr() string {
	const key = "HTTP_ADDR"
	v := l.get(key)
	if v == "" {
		return defaultHTTPAddr
	}
	_, port, err := net.SplitHostPort(v)
	if err == nil {
		var n int
		if n, err = strconv.Atoi(port); err == nil && (n < 1 || n > 65535) {
			err = errors.New("port out of range")
		}
	}
	if err != nil {
		l.fail(key, "must be host:port with a port from 1 to 65535 (got %q)", v)
	}
	return v
}

// databaseURL never echoes the value in an error: it carries the password.
func (l *loader) databaseURL() Secret {
	const key = "DATABASE_URL"
	v := l.required(key)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	switch {
	case err != nil:
		l.fail(key, "must be a valid URL")
	case u.Scheme != "postgres" && u.Scheme != "postgresql":
		l.fail(key, "must use the postgres:// or postgresql:// scheme")
	case u.Host == "":
		l.fail(key, "must include a host")
	}
	return Secret(v)
}

func (l *loader) auth0Domain() string {
	const key = "AUTH0_DOMAIN"
	v := l.required(key)
	if strings.Contains(v, "/") {
		l.fail(key, "must be a bare hostname such as tenant.eu.auth0.com (got %q)", v)
	}
	return v
}

func (l *loader) s3Endpoint() string {
	const key = "S3_ENDPOINT"
	v := l.get(key)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail(key, "must be an http:// or https:// URL (got %q)", v)
	}
	return v
}

func (l *loader) llmProvider() string {
	const key = "LLM_PROVIDER"
	v := l.required(key)
	if v != "" && v != "anthropic" {
		l.fail(key, "must be anthropic (got %q)", v)
	}
	return v
}

// apns demands all four keys when strict. Otherwise push may be left
// unconfigured, but a partial set is always a mistake.
func (l *loader) apns(strict bool) APNS {
	keys := []string{"APNS_KEY_ID", "APNS_TEAM_ID", "APNS_BUNDLE_ID", "APNS_KEY_PATH"}
	values := make([]string, len(keys))
	anySet := false
	for i, key := range keys {
		values[i] = l.get(key)
		anySet = anySet || values[i] != ""
	}
	for i, key := range keys {
		switch {
		case values[i] != "":
		case strict:
			l.fail(key, "required")
		case anySet:
			l.fail(key, "required when any APNS_* key is set")
		}
	}
	return APNS{KeyID: values[0], TeamID: values[1], BundleID: values[2], KeyPath: values[3]}
}
