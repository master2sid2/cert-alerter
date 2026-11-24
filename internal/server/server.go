// Package server implements the HTTP API and serves the embedded web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/auth"
	"github.com/master2sid2/cert-alerter/internal/config"
	"github.com/master2sid2/cert-alerter/internal/logging"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/monitor"
	"github.com/master2sid2/cert-alerter/internal/notify"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

// Deps are the collaborators of the HTTP server.
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Settings *settings.Service
	Monitor  *monitor.Monitor
	Alerts   *alerts.Engine
	Channels *notify.Service
	Sender   mailer.Sender
	Log      *slog.Logger
	// AccessLog receives one line per HTTP request; nil disables it.
	AccessLog *logging.AccessLog
	Static    fs.FS
	Version   string
}

// Server handles HTTP requests.
type Server struct {
	Deps

	setupMu    sync.Mutex
	setupToken string

	loginLimiter     *limiter
	sensitiveLimiter *limiter
	assets           *assetStore
	devStatic        bool // re-read assets from disk on every request
	now              func() time.Time
}

// New creates the server. When the database has no users yet a one-time
// setup token is generated (see SetupToken).
func New(ctx context.Context, d Deps) (*Server, error) {
	s := &Server{
		Deps:             d,
		loginLimiter:     newLimiter(10, time.Minute),
		sensitiveLimiter: newLimiter(10, 10*time.Minute),
		now:              func() time.Time { return time.Now().UTC().Truncate(time.Second) },
	}
	if d.Config.StaticDir != "" {
		d.Static = os.DirFS(d.Config.StaticDir)
		s.Static = d.Static
		s.devStatic = true
	}
	assets, err := loadAssets(d.Static)
	if err != nil {
		return nil, fmt.Errorf("load web assets: %w", err)
	}
	s.assets = assets
	n, err := d.Store.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		token, _ := auth.NewToken()
		s.setupToken = token
	}
	return s, nil
}

// SetupToken returns the first-run token, or "" when setup is complete.
func (s *Server) SetupToken() string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupToken
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn handlerFunc) { mux.Handle(pattern, s.wrap(fn)) }

	// Public endpoints.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	h("GET /api/health", func(w http.ResponseWriter, r *http.Request) error { s.handleHealth(w, r); return nil })
	h("GET /api/setup", s.handleSetupStatus)
	h("POST /api/setup", s.handleSetup)
	h("POST /api/auth/login", s.handleLogin)
	h("POST /api/auth/logout", s.handleLogout)
	h("POST /api/auth/forgot", s.handleForgot)
	h("GET /api/auth/token/{token}", s.handleTokenInfo)
	h("POST /api/auth/token/{token}", s.handleTokenComplete)

	// Own account (allowed while a password change is pending).
	h("GET /api/me", s.authed(s.handleMe, true))
	h("PUT /api/me", s.authed(s.handleUpdateMe, true))
	h("POST /api/me/password", s.authed(s.handleChangePassword, true))

	// Read access and certificate intake for every user.
	h("GET /api/dashboard", s.authed(s.handleDashboard, false))
	h("GET /api/certificates", s.authed(s.handleListCertificates, false))
	h("POST /api/certificates", s.authed(s.handleCreateCertificate, false))
	h("POST /api/certificates/bulk", s.authed(s.handleBulkCreateCertificates, false))
	h("POST /api/certificates/check", s.authed(s.handleCheckAll, false))
	h("GET /api/certificates/{id}", s.authed(s.handleGetCertificate, false))
	h("POST /api/certificates/{id}/check", s.authed(s.handleCheckCertificate, false))
	h("POST /api/certificates/{id}/groups", s.authed(s.handleCertificateAddGroups, false))
	h("GET /api/groups", s.authed(s.handleListGroups, false))
	h("GET /api/groups/{id}", s.authed(s.handleGetGroup, false))
	h("POST /api/groups/{id}/certificates", s.authed(s.handleGroupAddCertificates, false))
	h("GET /api/recipients", s.authed(s.handleListRecipients, false))
	h("GET /api/notifications", s.authed(s.handleListNotifications, false))
	h("GET /api/mail-log", s.authed(s.handleListMailLog, false))
	h("GET /api/mail-log/export", s.authed(s.handleExportMailLog, false))

	// Admin only.
	h("PUT /api/certificates/{id}", s.admin(s.handleUpdateCertificate))
	h("DELETE /api/certificates/{id}", s.admin(s.handleDeleteCertificate))
	h("POST /api/groups", s.admin(s.handleCreateGroup))
	h("PUT /api/groups/{id}", s.admin(s.handleUpdateGroup))
	h("DELETE /api/groups/{id}", s.admin(s.handleDeleteGroup))
	h("DELETE /api/groups/{id}/certificates/{certID}", s.admin(s.handleGroupRemoveCertificate))
	h("POST /api/groups/{id}/recipients", s.admin(s.handleGroupAddRecipients))
	h("DELETE /api/groups/{id}/recipients/{recipientID}", s.admin(s.handleGroupRemoveRecipient))
	h("POST /api/groups/{id}/channels", s.admin(s.handleGroupAddChannels))
	h("DELETE /api/groups/{id}/channels/{channelID}", s.admin(s.handleGroupRemoveChannel))
	h("POST /api/groups/{id}/test", s.admin(s.handleGroupTest))
	h("POST /api/recipients", s.admin(s.handleCreateRecipient))
	h("PUT /api/recipients/{id}", s.admin(s.handleUpdateRecipient))
	h("DELETE /api/recipients/{id}", s.admin(s.handleDeleteRecipient))
	h("POST /api/notifications/{id}/retry", s.admin(s.handleRetryNotification))
	h("GET /api/users", s.admin(s.handleListUsers))
	h("POST /api/users", s.admin(s.handleInviteUser))
	h("PUT /api/users/{id}", s.admin(s.handleUpdateUser))
	h("DELETE /api/users/{id}", s.admin(s.handleDeleteUser))
	h("POST /api/users/{id}/invite", s.admin(s.handleResendInvite))
	h("POST /api/users/{id}/reset", s.admin(s.handleSendReset))
	h("POST /api/users/{id}/unlock", s.admin(s.handleUnlockUser))
	h("GET /api/settings", s.admin(s.handleGetSettings))
	h("PUT /api/settings/smtp", s.admin(s.handleSaveSMTP))
	h("POST /api/settings/smtp/test", s.admin(s.handleTestSMTP))
	h("PUT /api/settings/monitoring", s.admin(s.handleSaveMonitoring))
	h("PUT /api/settings/security", s.admin(s.handleSaveSecurity))
	h("PUT /api/settings/general", s.admin(s.handleSaveGeneral))
	h("GET /api/channel-providers", s.admin(s.handleListProviders))
	h("PUT /api/channel-providers/{type}", s.admin(s.handleSaveProvider))
	h("GET /api/channels", s.admin(s.handleListChannels))
	h("POST /api/channels", s.admin(s.handleCreateChannel))
	h("POST /api/channels/test", s.admin(s.handleTestChannel))
	h("GET /api/channels/{id}", s.admin(s.handleGetChannel))
	h("PUT /api/channels/{id}", s.admin(s.handleUpdateChannel))
	h("DELETE /api/channels/{id}", s.admin(s.handleDeleteChannel))
	h("GET /api/audit", s.admin(s.handleListAudit))

	h("/api/", func(http.ResponseWriter, *http.Request) error {
		return &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "unknown API endpoint"}
	})
	mux.Handle("/", s.staticHandler())

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": errForbidden("cross_origin", "cross-origin request rejected")})
	}))
	var root http.Handler = mux
	root = cop.Handler(root)
	root = s.securityHeaders(root)
	// The access log is outermost so that it also records the 500 written by
	// the recoverer after a panic.
	root = s.recoverer(root)
	return s.logRequests(root)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.Store.DB.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "error": "database unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
}

// ---- errors -------------------------------------------------------------

type apiError struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
	Details any               `json:"details,omitempty"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errBadRequest(code, msg string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: code, Message: msg}
}

func errForbidden(code, msg string) *apiError {
	return &apiError{Status: http.StatusForbidden, Code: code, Message: msg}
}

func errNotFound(msg string) *apiError {
	return &apiError{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

func errConflict(code, msg string) *apiError {
	return &apiError{Status: http.StatusConflict, Code: code, Message: msg}
}

func errValidation(fields validate.Errors) *apiError {
	return &apiError{Status: http.StatusUnprocessableEntity, Code: "validation", Message: "validation failed", Fields: fields}
}

func (s *Server) wrap(fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if err := fn(w, r); err != nil {
			s.writeError(w, r, err)
		}
	})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	ae, isAPI := errors.AsType[*apiError](err)
	fields, isValidation := errors.AsType[validate.Errors](err)
	switch {
	case isAPI:
	case isValidation:
		ae = errValidation(fields)
	case errors.Is(err, store.ErrNotFound):
		ae = errNotFound("not found")
	case errors.Is(err, store.ErrConflict):
		ae = errConflict("conflict", "already exists")
	case errors.Is(err, context.Canceled):
		return
	default:
		s.Log.Error("request failed", "method", r.Method, "path", redactURI(r.URL), "err", err)
		ae = &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error"}
	}
	writeJSON(w, ae.Status, map[string]any{"error": ae})
}

// ---- JSON helpers -------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return &apiError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "expected application/json"}
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errBadRequest("invalid_json", "invalid JSON body: "+err.Error())
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errNotFound("not found")
	}
	return id, nil
}

func queryInt(r *http.Request, name string, def int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return v
}

// ---- middleware ---------------------------------------------------------

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests writes the access log entry once the response is complete.
func (s *Server) logRequests(next http.Handler) http.Handler {
	if s.AccessLog == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			s.AccessLog.Log(&logging.AccessEntry{
				Time: start, RemoteIP: s.clientIP(r), Method: r.Method, URI: redactURI(r.URL), Proto: r.Proto,
				Status: status, Bytes: int64(sw.bytes), Referer: r.Referer(), UserAgent: r.UserAgent(),
			})
		}()
		next.ServeHTTP(sw, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.Log.Error("panic", "path", redactURI(r.URL), "panic", v, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": &apiError{Code: "internal", Message: "internal server error"},
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.Config.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientIP returns the address of the client, honouring proxy headers only
// when the proxy is trusted.
func (s *Server) clientIP(r *http.Request) string {
	if s.Config.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip.String()
			}
		}
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// baseURL returns the public URL of the application for links in e-mails.
// The configured value is preferred; the request is only used as fallback
// for actions of authenticated admins.
func (s *Server) baseURL(r *http.Request, cfg settings.All) string {
	if cfg.General.BaseURL != "" {
		return cfg.General.BaseURL
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- audit --------------------------------------------------------------

func (s *Server) audit(r *http.Request, action, entityType string, entityID int64, details map[string]any) {
	e := &store.AuditEntry{Action: action, EntityType: entityType, IP: s.clientIP(r)}
	if p := principalFrom(r); p != nil {
		e.UserID = &p.User.ID
		e.UserEmail = p.User.Email
	}
	if entityID > 0 {
		e.EntityID = &entityID
	}
	if len(details) > 0 {
		if b, err := json.Marshal(details); err == nil {
			e.Details = string(b)
		}
	}
	if err := s.Store.AddAudit(context.WithoutCancel(r.Context()), e); err != nil {
		s.Log.Error("audit log", "action", action, "err", err)
	}
}

// ---- rate limiting ------------------------------------------------------

type limiter struct {
	mu     sync.Mutex
	limit  int
	period time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	count int
}

func newLimiter(limit int, period time.Duration) *limiter {
	return &limiter{limit: limit, period: period, hits: map[string]*window{}}
}

// allow records a hit for key and reports whether it is within the limit.
func (l *limiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10000 {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.period {
				delete(l.hits, k)
			}
		}
	}
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) > l.period {
		l.hits[key] = &window{start: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.limit
}

func errTooManyRequests() *apiError {
	return &apiError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many requests, try again later"}
}
