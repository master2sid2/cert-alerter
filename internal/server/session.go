package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/master2sid2/cert-alerter/internal/auth"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
)

const sessionCookie = "cert_alerter_session"

type ctxKey int

const principalKey ctxKey = 1

// principal is the authenticated user of a request.
type principal struct {
	User               *store.User
	Session            *store.Session
	MustChangePassword bool
}

func principalFrom(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey).(*principal)
	return p
}

func (s *Server) cookieSecure(r *http.Request) bool {
	switch s.Config.CookieSecure {
	case "always":
		return true
	case "never":
		return false
	}
	return s.isHTTPS(r)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cookieSecure(r), SameSite: http.SameSiteLaxMode,
	})
}

// startSession creates a session for user and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, cfg settings.All) (*store.Session, error) {
	token, hash := auth.NewToken()
	now := s.now()
	sess := &store.Session{
		ID: hash, UserID: u.ID, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(time.Duration(cfg.Security.SessionLifetimeHours) * time.Hour),
		IP:        s.clientIP(r), UserAgent: r.UserAgent(),
	}
	if err := s.Store.CreateSession(r.Context(), sess); err != nil {
		return nil, err
	}
	s.setSessionCookie(w, r, token, sess.ExpiresAt)
	return sess, nil
}

// loadPrincipal resolves the session cookie. It returns nil for anonymous
// requests and for expired, idle or disabled sessions.
func (s *Server) loadPrincipal(r *http.Request, cfg settings.All) (*principal, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, nil
	}
	ctx := r.Context()
	sess, err := s.Store.GetSession(ctx, auth.HashToken(c.Value))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	idle := time.Duration(cfg.Security.SessionIdleMinutes) * time.Minute
	if !now.Before(sess.ExpiresAt) || (idle > 0 && now.Sub(sess.LastSeenAt) > idle) {
		_ = s.Store.DeleteSession(ctx, sess.ID)
		return nil, nil
	}
	u, err := s.Store.GetUser(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if u.Status != store.UserActive {
		_ = s.Store.DeleteSession(ctx, sess.ID)
		return nil, nil
	}
	if now.Sub(sess.LastSeenAt) >= time.Minute {
		if err := s.Store.TouchSession(ctx, sess.ID, now); err == nil {
			sess.LastSeenAt = now
		}
	}
	return &principal{User: u, Session: sess, MustChangePassword: passwordExpired(u, cfg, now)}, nil
}

func passwordExpired(u *store.User, cfg settings.All, now time.Time) bool {
	maxAge := cfg.Security.Password.MaxAgeDays
	if maxAge <= 0 {
		return false
	}
	if u.PasswordChangedAt == nil {
		return true
	}
	return now.Sub(*u.PasswordChangedAt) > time.Duration(maxAge)*24*time.Hour
}

// authed requires a signed-in user. allowPending permits the request while
// the user still has to change an expired password.
func (s *Server) authed(fn handlerFunc, allowPending bool) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		cfg, err := s.Settings.Get(r.Context())
		if err != nil {
			return err
		}
		p, err := s.loadPrincipal(r, cfg)
		if err != nil {
			return err
		}
		if p == nil {
			return &apiError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "sign in required"}
		}
		if p.MustChangePassword && !allowPending {
			return errForbidden("password_change_required", "the password has expired and must be changed")
		}
		return fn(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	}
}

// admin requires a signed-in admin.
func (s *Server) admin(fn handlerFunc) handlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request) error {
		if !principalFrom(r).User.IsAdmin() {
			return errForbidden("forbidden", "administrator role required")
		}
		return fn(w, r)
	}, false)
}
