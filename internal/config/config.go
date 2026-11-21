// Package config reads process configuration from flags and environment
// variables (CERT_ALERTER_*). Settings that admins change at runtime live in
// the database instead (see package settings).
package config

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the process configuration.
type Config struct {
	Listen        string
	DBDriver      string
	DBDSN         string
	DataDir       string
	BaseURL       string
	SecretKey     string
	TLSCert       string
	TLSKey        string
	TrustProxy    bool
	CookieSecure  string // auto | always | never
	LogLevel      string
	LogFormat     string
	LogFile       string // application log: stderr, stdout, off or a file path
	AccessLog     string // HTTP access log: stdout, stderr, off or a file path
	AccessFormat  string // text or json
	AdminEmail    string
	AdminPassword string
	Scheduler     bool
	CAFile        string
	StaticDir     string
	InstanceID    string
}

const envPrefix = "CERT_ALERTER_"

func env(name, def string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		return v
	}
	return def
}

func envBool(name string, def bool) bool {
	v, ok := os.LookupEnv(envPrefix + name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

// Register defines the flags shared by commands that open the database.
func Register(fs *flag.FlagSet) *Config {
	c := &Config{}
	fs.StringVar(&c.DBDriver, "db-driver", env("DB_DRIVER", "sqlite"), "database driver: sqlite or mysql [$CERT_ALERTER_DB_DRIVER]")
	fs.StringVar(&c.DBDSN, "db-dsn", env("DB_DSN", ""), "database DSN; sqlite: file path (default <data-dir>/cert-alerter.db), mysql: user:pass@tcp(host:3306)/db [$CERT_ALERTER_DB_DSN]")
	fs.StringVar(&c.DataDir, "data-dir", env("DATA_DIR", "data"), "directory for the SQLite database and the generated secret key [$CERT_ALERTER_DATA_DIR]")
	fs.StringVar(&c.SecretKey, "secret-key", env("SECRET_KEY", ""), "passphrase used to encrypt stored secrets (default: random key in <data-dir>/secret.key) [$CERT_ALERTER_SECRET_KEY]")
	fs.StringVar(&c.LogLevel, "log-level", env("LOG_LEVEL", "info"), "log level: debug, info, warn, error [$CERT_ALERTER_LOG_LEVEL]")
	fs.StringVar(&c.LogFormat, "log-format", env("LOG_FORMAT", "text"), "application log format: text or json [$CERT_ALERTER_LOG_FORMAT]")
	fs.StringVar(&c.LogFile, "log-file", env("LOG_FILE", "stderr"), "application log destination: stderr, stdout, off or a file path [$CERT_ALERTER_LOG_FILE]")
	return c
}

// RegisterServe adds the flags of the serve command.
func (c *Config) RegisterServe(fs *flag.FlagSet) {
	fs.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "HTTP listen address [$CERT_ALERTER_LISTEN]")
	fs.StringVar(&c.BaseURL, "base-url", env("BASE_URL", ""), "public URL used in e-mail links; overrides the value from the settings page [$CERT_ALERTER_BASE_URL]")
	fs.StringVar(&c.TLSCert, "tls-cert", env("TLS_CERT", ""), "TLS certificate file to serve HTTPS directly [$CERT_ALERTER_TLS_CERT]")
	fs.StringVar(&c.TLSKey, "tls-key", env("TLS_KEY", ""), "TLS private key file [$CERT_ALERTER_TLS_KEY]")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", envBool("TRUST_PROXY", false), "trust X-Forwarded-For / X-Forwarded-Proto headers from a reverse proxy [$CERT_ALERTER_TRUST_PROXY]")
	fs.StringVar(&c.CookieSecure, "cookie-secure", env("COOKIE_SECURE", "auto"), "session cookie Secure flag: auto, always, never [$CERT_ALERTER_COOKIE_SECURE]")
	fs.StringVar(&c.AdminEmail, "admin-email", env("ADMIN_EMAIL", ""), "create this admin on first start (with -admin-password) [$CERT_ALERTER_ADMIN_EMAIL]")
	fs.StringVar(&c.AdminPassword, "admin-password", env("ADMIN_PASSWORD", ""), "password of the bootstrap admin [$CERT_ALERTER_ADMIN_PASSWORD]")
	fs.BoolVar(&c.Scheduler, "scheduler", envBool("SCHEDULER", true), "run scheduled checks and alerts on this instance [$CERT_ALERTER_SCHEDULER]")
	fs.StringVar(&c.StaticDir, "static-dir", env("STATIC_DIR", ""), "development: serve the web UI from this directory instead of the embedded copy [$CERT_ALERTER_STATIC_DIR]")
	fs.StringVar(&c.AccessLog, "access-log", env("ACCESS_LOG", "stdout"), "HTTP access log destination: stdout, stderr, off or a file path [$CERT_ALERTER_ACCESS_LOG]")
	fs.StringVar(&c.AccessFormat, "access-log-format", env("ACCESS_LOG_FORMAT", "text"), "HTTP access log format: text or json [$CERT_ALERTER_ACCESS_LOG_FORMAT]")
	fs.StringVar(&c.CAFile, "ca-file", env("CA_FILE", ""), "PEM file with additional trusted CA certificates (e.g. an internal corporate CA) [$CERT_ALERTER_CA_FILE]")
}

// Finalize derives defaults and validates values.
func (c *Config) Finalize() error {
	c.DBDriver = strings.ToLower(strings.TrimSpace(c.DBDriver))
	if c.DBDSN == "" && (c.DBDriver == "" || strings.HasPrefix(c.DBDriver, "sqlite")) {
		c.DBDSN = filepath.Join(c.DataDir, "cert-alerter.db")
	}
	switch c.CookieSecure {
	case "":
		// Commands other than serve do not register the flag.
		c.CookieSecure = "auto"
	case "auto", "always", "never":
	default:
		return fmt.Errorf("invalid cookie-secure value %q", c.CookieSecure)
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return fmt.Errorf("both tls-cert and tls-key must be set")
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.InstanceID == "" {
		host, _ := os.Hostname()
		buf := make([]byte, 4)
		_, _ = rand.Read(buf)
		c.InstanceID = fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(buf))
		if len(c.InstanceID) > 64 {
			c.InstanceID = c.InstanceID[len(c.InstanceID)-64:]
		}
	}
	return nil
}
