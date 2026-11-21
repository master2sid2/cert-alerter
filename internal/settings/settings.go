// Package settings holds the runtime configuration that admins edit in the
// web interface (SMTP, monitoring, security policy, general options).
// Values are stored as JSON documents in the settings table and cached.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/master2sid2/cert-alerter/internal/auth"
	"github.com/master2sid2/cert-alerter/internal/secret"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

const (
	keySMTP       = "smtp"
	keyMonitoring = "monitoring"
	keySecurity   = "security"
	keyGeneral    = "general"
)

// SMTP security modes.
const (
	SecurityNone     = "none"
	SecuritySTARTTLS = "starttls"
	SecurityTLS      = "tls"
)

// SMTP configures outgoing e-mail.
type SMTP struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Security   string `json:"security"`    // none | starttls | tls
	AuthMethod string `json:"auth_method"` // auto | plain | login | cram-md5 | none
	Username   string `json:"username"`
	// Password is the decrypted password; it is never serialised.
	Password       string `json:"-"`
	PasswordEnc    string `json:"password_enc,omitempty"`
	FromEmail      string `json:"from_email"`
	FromName       string `json:"from_name"`
	SkipVerify     bool   `json:"skip_verify"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	HELO           string `json:"helo"`
}

// Configured reports whether e-mail can be sent.
func (s SMTP) Configured() bool { return s.Host != "" && s.FromEmail != "" }

// Monitoring configures checks and alerting.
type Monitoring struct {
	CheckIntervalMinutes int   `json:"check_interval_minutes"`
	CheckTimeoutSeconds  int   `json:"check_timeout_seconds"`
	Concurrency          int   `json:"concurrency"`
	Thresholds           []int `json:"thresholds"`
	NotifyExpired        bool  `json:"notify_expired"`
	CriticalDays         int   `json:"critical_days"`
	HistoryRetentionDays int   `json:"history_retention_days"`
}

// WarningDays is the largest alert threshold; certificates inside it are
// shown as "expiring soon".
func (m Monitoring) WarningDays() int {
	if len(m.Thresholds) == 0 {
		return 30
	}
	return slices.Max(m.Thresholds)
}

// Security configures the password policy, lockout and sessions.
type Security struct {
	Password             auth.PasswordPolicy `json:"password"`
	MaxFailedLogins      int                 `json:"max_failed_logins"`
	LockoutMinutes       int                 `json:"lockout_minutes"`
	SessionLifetimeHours int                 `json:"session_lifetime_hours"`
	SessionIdleMinutes   int                 `json:"session_idle_minutes"`
	InviteTTLHours       int                 `json:"invite_ttl_hours"`
	ResetTTLHours        int                 `json:"reset_ttl_hours"`
}

// EmailLanguages are the languages e-mails can be written in.
var EmailLanguages = []string{"en"}

// General holds instance wide options.
type General struct {
	AppName       string `json:"app_name"`
	BaseURL       string `json:"base_url"`
	EmailLanguage string `json:"email_language"` // see EmailLanguages
	Timezone      string `json:"timezone"`
}

// Location returns the configured time zone (UTC on error).
func (g General) Location() *time.Location {
	if loc, err := time.LoadLocation(g.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

// All bundles every section.
type All struct {
	SMTP       SMTP       `json:"smtp"`
	Monitoring Monitoring `json:"monitoring"`
	Security   Security   `json:"security"`
	General    General    `json:"general"`
}

// Defaults returns the factory configuration.
func Defaults() All {
	return All{
		SMTP: SMTP{
			Port:           587,
			Security:       SecuritySTARTTLS,
			AuthMethod:     "auto",
			FromName:       "Cert Alerter",
			TimeoutSeconds: 20,
		},
		Monitoring: Monitoring{
			CheckIntervalMinutes: 360,
			CheckTimeoutSeconds:  10,
			Concurrency:          8,
			Thresholds:           []int{30, 14, 7, 3},
			NotifyExpired:        true,
			CriticalDays:         7,
			HistoryRetentionDays: 365,
		},
		Security: Security{
			Password:             auth.DefaultPasswordPolicy(),
			MaxFailedLogins:      5,
			LockoutMinutes:       15,
			SessionLifetimeHours: 24,
			SessionIdleMinutes:   120,
			InviteTTLHours:       72,
			ResetTTLHours:        2,
		},
		General: General{
			AppName:       "Cert Alerter",
			EmailLanguage: "en",
			Timezone:      "UTC",
		},
	}
}

// Service loads, validates, caches and stores settings.
type Service struct {
	store           *store.Store
	box             *secret.Box
	baseURLOverride string
	log             *slog.Logger

	mu       sync.Mutex
	cached   *All
	cachedAt time.Time
}

// cacheTTL bounds staleness when several instances share one database.
const cacheTTL = 15 * time.Second

// NewService creates a settings service. A non-empty baseURLOverride (from
// the environment) always wins over the stored base URL.
func NewService(st *store.Store, box *secret.Box, baseURLOverride string, log *slog.Logger) *Service {
	return &Service{store: st, box: box, baseURLOverride: strings.TrimRight(baseURLOverride, "/"), log: log}
}

// BaseURLLocked reports whether the base URL comes from the environment.
func (s *Service) BaseURLLocked() bool { return s.baseURLOverride != "" }

// Get returns the current settings (a copy that may be modified).
func (s *Service) Get(ctx context.Context) (All, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Since(s.cachedAt) < cacheTTL {
		return s.cached.clone(), nil
	}
	raw, err := s.store.AllSettings(ctx)
	if err != nil {
		if s.cached != nil { // serve stale settings rather than failing
			return s.cached.clone(), nil
		}
		return All{}, err
	}
	all := Defaults()
	for key, target := range map[string]any{
		keySMTP: &all.SMTP, keyMonitoring: &all.Monitoring, keySecurity: &all.Security, keyGeneral: &all.General,
	} {
		if v, ok := raw[key]; ok {
			if err := json.Unmarshal([]byte(v), target); err != nil {
				s.log.Error("invalid stored settings, using defaults", "section", key, "err", err)
			}
		}
	}
	if all.SMTP.PasswordEnc != "" {
		pw, err := s.box.Decrypt(all.SMTP.PasswordEnc)
		if err != nil {
			s.log.Error("cannot decrypt SMTP password", "err", err)
		}
		all.SMTP.Password = pw
	}
	if s.baseURLOverride != "" {
		all.General.BaseURL = s.baseURLOverride
	}
	all.sanitize()
	s.cached, s.cachedAt = &all, time.Now()
	return all.clone(), nil
}

// Invalidate drops the cache.
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

func (s *Service) save(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, key, string(data)); err != nil {
		return err
	}
	s.Invalidate()
	return nil
}

// SaveSMTP validates and stores SMTP settings. password == nil keeps the
// stored password; an empty string clears it.
func (s *Service) SaveSMTP(ctx context.Context, in SMTP, password *string) error {
	cur, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if password != nil {
		in.Password = *password
	} else {
		in.Password = cur.SMTP.Password
	}
	if err := ValidateSMTP(&in); err != nil {
		return err
	}
	in.PasswordEnc, err = s.box.Encrypt(in.Password)
	if err != nil {
		return err
	}
	return s.save(ctx, keySMTP, in)
}

// SaveMonitoring validates and stores monitoring settings.
func (s *Service) SaveMonitoring(ctx context.Context, in Monitoring) error {
	if err := ValidateMonitoring(&in); err != nil {
		return err
	}
	return s.save(ctx, keyMonitoring, in)
}

// SaveSecurity validates and stores security settings.
func (s *Service) SaveSecurity(ctx context.Context, in Security) error {
	if err := ValidateSecurity(&in); err != nil {
		return err
	}
	return s.save(ctx, keySecurity, in)
}

// SaveGeneral validates and stores general settings.
func (s *Service) SaveGeneral(ctx context.Context, in General) error {
	if s.baseURLOverride != "" {
		in.BaseURL = s.baseURLOverride
	}
	if err := ValidateGeneral(&in); err != nil {
		return err
	}
	return s.save(ctx, keyGeneral, in)
}

// SetBaseURLIfEmpty stores a base URL when none is configured yet (used by
// the first-run setup, which knows the URL the admin is using).
func (s *Service) SetBaseURLIfEmpty(ctx context.Context, base string) error {
	cur, err := s.Get(ctx)
	if err != nil || cur.General.BaseURL != "" {
		return err
	}
	g := cur.General
	g.BaseURL = base
	if err := ValidateGeneral(&g); err != nil {
		return err
	}
	return s.save(ctx, keyGeneral, g)
}

func (a All) clone() All {
	a.Monitoring.Thresholds = slices.Clone(a.Monitoring.Thresholds)
	return a
}

// sanitize repairs values that would break the application if a stored
// document was edited by hand.
func (a *All) sanitize() {
	d := Defaults()
	m := &a.Monitoring
	if m.CheckIntervalMinutes < 5 {
		m.CheckIntervalMinutes = d.Monitoring.CheckIntervalMinutes
	}
	if m.CheckTimeoutSeconds < 1 {
		m.CheckTimeoutSeconds = d.Monitoring.CheckTimeoutSeconds
	}
	if m.Concurrency < 1 {
		m.Concurrency = d.Monitoring.Concurrency
	}
	m.Thresholds = normalizeThresholds(m.Thresholds)
	if len(m.Thresholds) == 0 {
		m.Thresholds = d.Monitoring.Thresholds
	}
	if m.HistoryRetentionDays < 30 {
		m.HistoryRetentionDays = d.Monitoring.HistoryRetentionDays
	}
	sec := &a.Security
	if sec.SessionLifetimeHours < 1 {
		sec.SessionLifetimeHours = d.Security.SessionLifetimeHours
	}
	if sec.InviteTTLHours < 1 {
		sec.InviteTTLHours = d.Security.InviteTTLHours
	}
	if sec.ResetTTLHours < 1 {
		sec.ResetTTLHours = d.Security.ResetTTLHours
	}
	if sec.LockoutMinutes < 1 {
		sec.LockoutMinutes = d.Security.LockoutMinutes
	}
	if a.General.AppName == "" {
		a.General.AppName = d.General.AppName
	}
	if !slices.Contains(EmailLanguages, a.General.EmailLanguage) {
		// e.g. a language no longer supported, stored by an older version
		a.General.EmailLanguage = d.General.EmailLanguage
	}
	if _, err := time.LoadLocation(a.General.Timezone); err != nil || a.General.Timezone == "" {
		a.General.Timezone = "UTC"
	}
	if a.SMTP.TimeoutSeconds < 1 {
		a.SMTP.TimeoutSeconds = d.SMTP.TimeoutSeconds
	}
}

func normalizeThresholds(in []int) []int {
	out := slices.DeleteFunc(slices.Clone(in), func(v int) bool { return v < 1 || v > 365 })
	slices.Sort(out)
	out = slices.Compact(out)
	slices.Reverse(out)
	return out
}

// ValidateSMTP checks and normalises SMTP settings.
func ValidateSMTP(in *SMTP) error {
	e := validate.Errors{}
	in.Host = validate.Text(e, "host", in.Host, 255, false, false)
	in.Username = validate.Text(e, "username", in.Username, 255, false, false)
	in.FromName = validate.Text(e, "from_name", in.FromName, 128, false, false)
	in.HELO = validate.Text(e, "helo", in.HELO, 255, false, false)
	in.Security = strings.ToLower(strings.TrimSpace(in.Security))
	in.AuthMethod = strings.ToLower(strings.TrimSpace(in.AuthMethod))
	if in.AuthMethod == "" {
		in.AuthMethod = "auto"
	}
	if !slices.Contains([]string{SecurityNone, SecuritySTARTTLS, SecurityTLS}, in.Security) {
		e.Add("security", validate.Invalid)
	}
	if !slices.Contains([]string{"auto", "plain", "login", "cram-md5", "none"}, in.AuthMethod) {
		e.Add("auth_method", validate.Invalid)
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 20
	}
	validate.Range(e, "timeout_seconds", in.TimeoutSeconds, 5, 120)
	if in.Host != "" {
		validate.Range(e, "port", in.Port, 1, 65535)
		if strings.ContainsAny(in.Host, "/: ") {
			e.Add("host", validate.Invalid)
		}
		if strings.TrimSpace(in.FromEmail) == "" {
			e.Add("from_email", validate.Required)
		}
	}
	if strings.TrimSpace(in.FromEmail) != "" {
		if addr, ok := validate.Email(in.FromEmail); ok {
			in.FromEmail = addr
		} else {
			e.Add("from_email", validate.InvalidEmail)
		}
	}
	if len(in.Password) > 512 {
		e.Add("password", validate.TooLong)
	}
	return e.Err()
}

// ValidateMonitoring checks and normalises monitoring settings.
func ValidateMonitoring(in *Monitoring) error {
	e := validate.Errors{}
	validate.Range(e, "check_interval_minutes", in.CheckIntervalMinutes, 5, 10080)
	validate.Range(e, "check_timeout_seconds", in.CheckTimeoutSeconds, 2, 120)
	validate.Range(e, "concurrency", in.Concurrency, 1, 64)
	validate.Range(e, "critical_days", in.CriticalDays, 1, 365)
	validate.Range(e, "history_retention_days", in.HistoryRetentionDays, 30, 3650)
	for _, t := range in.Thresholds {
		if t < 1 || t > 365 {
			e.Add("thresholds", validate.OutOfRange)
		}
	}
	in.Thresholds = normalizeThresholds(in.Thresholds)
	if len(in.Thresholds) == 0 {
		e.Add("thresholds", validate.Required)
	} else if len(in.Thresholds) > 10 {
		e.Add("thresholds", validate.TooLong)
	}
	return e.Err()
}

// ValidateSecurity checks security settings.
func ValidateSecurity(in *Security) error {
	e := validate.Errors{}
	validate.Range(e, "password.min_length", in.Password.MinLength, 6, auth.MaxPasswordLength)
	validate.Range(e, "password.history_count", in.Password.HistoryCount, 0, 24)
	validate.Range(e, "password.max_age_days", in.Password.MaxAgeDays, 0, 3650)
	validate.Range(e, "max_failed_logins", in.MaxFailedLogins, 0, 100)
	validate.Range(e, "lockout_minutes", in.LockoutMinutes, 1, 1440)
	validate.Range(e, "session_lifetime_hours", in.SessionLifetimeHours, 1, 720)
	validate.Range(e, "session_idle_minutes", in.SessionIdleMinutes, 0, 10080)
	validate.Range(e, "invite_ttl_hours", in.InviteTTLHours, 1, 720)
	validate.Range(e, "reset_ttl_hours", in.ResetTTLHours, 1, 72)
	return e.Err()
}

// ValidateGeneral checks and normalises general settings.
func ValidateGeneral(in *General) error {
	e := validate.Errors{}
	in.AppName = validate.Text(e, "app_name", in.AppName, 64, true, false)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if in.BaseURL != "" {
		u, err := url.Parse(in.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			e.Add("base_url", validate.Invalid)
		}
	}
	if !slices.Contains(EmailLanguages, in.EmailLanguage) {
		e.Add("email_language", validate.Invalid)
	}
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		e.Add("timezone", validate.Invalid)
	}
	return e.Err()
}

// String implements fmt.Stringer without leaking secrets.
func (s SMTP) String() string {
	return fmt.Sprintf("smtp://%s@%s:%d (%s)", s.Username, s.Host, s.Port, s.Security)
}
