// Package config loads the process-wide settings shared by every feature.
//
// Feature packages own their own settings: each declares a struct with `env`
// tags and calls Load on it, so adding a variable never touches this file.
// Variable names are the Vapor backend's, unchanged, so the same Helm values
// and sealed secrets configure both during the migration.
package config

import (
	"fmt"
	"net"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
)

// Environment is Vapor's environment name: production, staging,
// development or testing.
type Environment string

const (
	Production  Environment = "production"
	Development Environment = "development"
	Testing     Environment = "testing"
)

// Base is what the server itself needs before any feature is mounted.
type Base struct {
	Environment Environment `env:"APP_ENV" envDefault:"development"`
	Port        int         `env:"PORT" envDefault:"8080"`
	Version     string      `env:"APP_VERSION"`
	GitSHA      string      `env:"GIT_SHA"`
	LogLevel    string      `env:"LOG_LEVEL" envDefault:"info"`
	LogFormat   string      `env:"LOG_FORMAT" envDefault:"text"`

	Database Database
	RedisURL string `env:"REDIS_URL"`

	JWTSecret string `env:"JWT_SECRET" envDefault:"dev-secret"`

	AllowedOrigins      string   `env:"ALLOWED_ORIGINS"`
	TrustedProxyCIDRs   string   `env:"TRUSTED_PROXY_CIDRS" envDefault:"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,100.64.0.0/10"`
	EdgeClientIPKey     string   `env:"EDGE_CLIENT_IP_KEY"`
	AdminUserIDsRaw     string   `env:"NORVIQ_ADMIN_USER_IDS"`
	APIDocsEnabled      *bool    `env:"API_DOCS_ENABLED"`
	LegacyUpstreamURL   string   `env:"LEGACY_UPSTREAM_URL"`
	PrometheusEnabled   bool     `env:"PROMETHEUS_ENABLED"`
	MetricsSecret       string   `env:"METRICS_SECRET"`
	ExtraDisabledRoutes []string `env:"V2_DISABLED_ROUTES" envSeparator:","`
	// NativePrefixes, when set, limits v2 to the ported routes under these
	// path prefixes; every other request goes to Vapor. It lets a release
	// flip one domain at a time (e.g. "/v1/auth,/health").
	NativePrefixes []string `env:"V2_NATIVE_PREFIXES" envSeparator:","`
	// PprofAddr, when set (e.g. "127.0.0.1:6060"), serves net/http/pprof
	// on its own listener; see docs/pprof.md.
	PprofAddr string `env:"PPROF_ADDR"`
}

// Database mirrors ConfigureBootstrap's DATABASE_* variables and defaults.
type Database struct {
	Host     string `env:"DATABASE_HOST" envDefault:"localhost"`
	Port     int    `env:"DATABASE_PORT" envDefault:"5432"`
	Username string `env:"DATABASE_USERNAME" envDefault:"vapor_username"`
	Password string `env:"DATABASE_PASSWORD" envDefault:"vapor_password"`
	Name     string `env:"DATABASE_NAME" envDefault:"vapor_database"`
	MaxConns int32  `env:"DATABASE_MAX_CONNECTIONS" envDefault:"20"`
	SSLMode  string `env:"DATABASE_SSLMODE" envDefault:"disable"`
}

// DSN renders a pgx connection string.
func (d Database) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=%s&pool_max_conns=%d",
		urlEscape(d.Username), urlEscape(d.Password),
		net.JoinHostPort(d.Host, fmt.Sprint(d.Port)), d.Name, d.SSLMode, d.MaxConns)
}

func urlEscape(s string) string {
	r := strings.NewReplacer("%", "%25", ":", "%3A", "@", "%40", "/", "%2F", "?", "%3F", "#", "%23")
	return r.Replace(s)
}

// IsProduction matches `app.environment == .production`.
func (b Base) IsProduction() bool { return b.Environment == Production }

// AppVersion is the readiness report's version string.
func (b Base) AppVersion() string {
	switch {
	case b.Version != "":
		return b.Version
	case b.GitSHA != "":
		return b.GitSHA
	}
	return "unknown"
}

// DocsEnabled is shouldExposeOpenAPIDocs: on outside production by default.
func (b Base) DocsEnabled() bool {
	if b.APIDocsEnabled != nil {
		return *b.APIDocsEnabled
	}
	return !b.IsProduction()
}

// AdminUserIDs is AdminGuard.adminUserIDs(): entries that are not UUIDs
// grant nothing.
func (b Base) AdminUserIDs() map[uuid.UUID]struct{} {
	out := map[uuid.UUID]struct{}{}
	for _, raw := range strings.Split(b.AdminUserIDsRaw, ",") {
		if id, err := uuid.Parse(strings.TrimSpace(raw)); err == nil {
			out[id] = struct{}{}
		}
	}
	return out
}

// Load parses environment variables into a feature's settings struct.
func Load[T any]() (T, error) {
	var cfg T
	err := env.Parse(&cfg)
	return cfg, err
}

// Bool reads Vapor's lenient boolean spelling (1/true/yes/y/on and
// 0/false/no/n/off, case-insensitive), falling back to def otherwise. Many
// Vapor flags were read this way, so `env:",bool"` alone would reject values
// production already uses.
func Bool(raw string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	}
	return def
}
