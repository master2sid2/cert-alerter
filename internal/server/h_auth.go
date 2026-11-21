package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/auth"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

type userView struct {
	ID                int64      `json:"id"`
	Email             string     `json:"email"`
	Name              string     `json:"name"`
	Role              string     `json:"role"`
	Status            string     `json:"status"`
	LastLoginAt       *time.Time `json:"last_login_at"`
	PasswordChangedAt *time.Time `json:"password_changed_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

func viewUser(u *store.User) userView {
	return userView{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, Status: u.Status,
		LastLoginAt: u.LastLoginAt, PasswordChangedAt: u.PasswordChangedAt, CreatedAt: u.CreatedAt}
}

type appInfo struct {
	Name           string              `json:"name"`
	Version        string              `json:"version"`
	Thresholds     []int               `json:"thresholds"`
	WarningDays    int                 `json:"warning_days"`
	CriticalDays   int                 `json:"critical_days"`
	NotifyExpired  bool                `json:"notify_expired"`
	SMTPConfigured bool                `json:"smtp_configured"`
	PasswordPolicy auth.PasswordPolicy `json:"password_policy"`
	Timezone       string              `json:"timezone"`
}

func (s *Server) appInfo(cfg settings.All) appInfo {
	return appInfo{
		Name: cfg.General.AppName, Version: s.Version,
		Thresholds: cfg.Monitoring.Thresholds, WarningDays: cfg.Monitoring.WarningDays(),
		CriticalDays: cfg.Monitoring.CriticalDays, NotifyExpired: cfg.Monitoring.NotifyExpired,
		SMTPConfigured: cfg.SMTP.Configured(), PasswordPolicy: cfg.Security.Password, Timezone: cfg.General.Timezone,
	}
}

func (s *Server) mePayload(u *store.User, mustChange bool, cfg settings.All) map[string]any {
	return map[string]any{"user": viewUser(u), "must_change_password": mustChange, "app": s.appInfo(cfg)}
}

// weakPassword returns a validation error listing policy violations.
func weakPassword(violations []string) error {
	return &apiError{
		Status: http.StatusUnprocessableEntity, Code: "weak_password",
		Message: "the password does not satisfy the password policy",
		Fields:  map[string]string{"password": "weak_password"},
		Details: map[string]any{"violations": violations},
	}
}

func (s *Server) auditUser(r *http.Request, u *store.User, action string, details map[string]any) {
	e := &store.AuditEntry{Action: action, EntityType: "user", IP: s.clientIP(r)}
	if u != nil {
		e.UserID, e.UserEmail, e.EntityID = &u.ID, u.Email, &u.ID
	}
	if email, ok := details["email"].(string); ok && u == nil {
		e.UserEmail = email
	}
	if len(details) > 0 {
		e.Details = jsonString(details)
	}
	if err := s.Store.AddAudit(context.WithoutCancel(r.Context()), e); err != nil {
		s.Log.Error("audit log", "action", action, "err", err)
	}
}

// ---- first-run setup ------------------------------------------------------

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) error {
	cfg, err := s.Settings.Get(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required":  s.SetupToken() != "",
		"app_name":        cfg.General.AppName,
		"password_policy": cfg.Security.Password,
	})
	return nil
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token    string `json:"token"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		BaseURL  string `json:"base_url"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if !s.sensitiveLimiter.allow("setup:" + s.clientIP(r)) {
		return errTooManyRequests()
	}
	ctx := r.Context()
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.setupToken == "" {
		return errConflict("setup_done", "the application is already set up")
	}
	if subtle.ConstantTimeCompare([]byte(in.Token), []byte(s.setupToken)) != 1 {
		return errForbidden("invalid_setup_token", "invalid setup token; see the server log")
	}
	if n, err := s.Store.CountUsers(ctx); err != nil {
		return err
	} else if n > 0 {
		s.setupToken = ""
		return errConflict("setup_done", "the application is already set up")
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	fe := validate.Errors{}
	email, ok := validate.Email(in.Email)
	if !ok {
		fe.Add("email", validate.InvalidEmail)
	}
	name := validate.Text(fe, "name", in.Name, 128, false, false)
	if err := fe.Err(); err != nil {
		return err
	}
	if v := cfg.Security.Password.Check(in.Password, email); len(v) > 0 {
		return weakPassword(v)
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	now := s.now()
	u := &store.User{Email: email, Name: name, Role: store.RoleAdmin, Status: store.UserActive,
		PasswordHash: hash, PasswordChangedAt: &now}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		return err
	}
	s.setupToken = ""
	_ = s.Store.RecordLoginSuccess(ctx, u.ID)
	u.LastLoginAt = &now
	if in.BaseURL != "" {
		if err := s.Settings.SetBaseURLIfEmpty(ctx, in.BaseURL); err != nil {
			s.Log.Warn("could not store base URL from setup", "err", err)
		}
		cfg, _ = s.Settings.Get(ctx)
	}
	if _, err := s.startSession(w, r, u, cfg); err != nil {
		return err
	}
	s.auditUser(r, u, "setup.completed", nil)
	s.Log.Info("first-run setup completed", "admin", u.Email)
	writeJSON(w, http.StatusCreated, s.mePayload(u, false, cfg))
	return nil
}

// ---- login / logout -------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ip := s.clientIP(r)
	if !s.loginLimiter.allow("login:" + ip) {
		return errTooManyRequests()
	}
	ctx := r.Context()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	invalid := &apiError{Status: http.StatusUnauthorized, Code: "invalid_credentials", Message: "invalid e-mail or password"}
	if len(in.Password) > auth.MaxPasswordLength*4 {
		return invalid
	}
	email := store.NormalizeEmail(in.Email)
	u, err := s.Store.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if u == nil || u.Status != store.UserActive || u.PasswordHash == "" {
		auth.DummyVerify(in.Password)
		s.auditUser(r, nil, "auth.login_failed", map[string]any{"email": email, "reason": "unknown_or_inactive"})
		return invalid
	}
	now := s.now()
	locked := func(until time.Time) error {
		return &apiError{Status: http.StatusUnauthorized, Code: "account_locked",
			Message: "too many failed attempts, the account is temporarily locked",
			Details: map[string]any{"locked_until": until}}
	}
	if u.IsLocked(now) {
		auth.DummyVerify(in.Password)
		return locked(*u.LockedUntil)
	}
	if !auth.VerifyPassword(u.PasswordHash, in.Password) {
		if max := cfg.Security.MaxFailedLogins; max > 0 {
			n, err := s.Store.IncrementFailedLogins(ctx, u.ID)
			if err != nil {
				return err
			}
			if n >= max {
				until := now.Add(time.Duration(cfg.Security.LockoutMinutes) * time.Minute)
				if err := s.Store.LockUser(ctx, u.ID, until); err != nil {
					return err
				}
				s.auditUser(r, u, "auth.locked", map[string]any{"until": until})
				return locked(until)
			}
		}
		s.auditUser(r, u, "auth.login_failed", map[string]any{"reason": "bad_password"})
		return invalid
	}
	if err := s.Store.RecordLoginSuccess(ctx, u.ID); err != nil {
		return err
	}
	u.LastLoginAt = &now
	if _, err := s.startSession(w, r, u, cfg); err != nil {
		return err
	}
	s.auditUser(r, u, "auth.login", nil)
	writeJSON(w, http.StatusOK, s.mePayload(u, passwordExpired(u, cfg, now), cfg))
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.Store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- password reset (self service) ---------------------------------------

func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if !s.sensitiveLimiter.allow("forgot:" + s.clientIP(r)) {
		return errTooManyRequests()
	}
	email, ok := validate.Email(in.Email)
	if !ok {
		return errValidation(validate.Errors{"email": validate.InvalidEmail})
	}
	// The response never reveals whether the account exists; the work is
	// done in the background so timing does not reveal it either.
	if s.sensitiveLimiter.allow("forgot-mail:" + email) {
		ip := s.clientIP(r)
		ua := r.UserAgent()
		go s.sendSelfServiceReset(email, ip, ua)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func (s *Server) sendSelfServiceReset(email, ip, ua string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	u, err := s.Store.GetUserByEmail(ctx, email)
	if err != nil || u.Status != store.UserActive {
		return
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return
	}
	if !cfg.SMTP.Configured() || cfg.General.BaseURL == "" {
		s.Log.Warn("password reset requested but e-mail or base URL is not configured", "email", email)
		return
	}
	link, expires, err := s.issueToken(ctx, u, store.TokenReset, nil, cfg, cfg.General.BaseURL)
	if err != nil {
		s.Log.Error("create reset token", "err", err)
		return
	}
	msg, err := alerts.Branding(cfg).Reset(u.Email, u.Name, link, expires)
	if err == nil {
		err = s.Sender.Send(ctx, cfg.SMTP, []*mailer.Message{msg})[0]
	}
	if err != nil {
		s.Log.Error("send reset e-mail", "email", email, "err", err)
	}
	_ = s.Store.AddAudit(ctx, &store.AuditEntry{UserID: &u.ID, UserEmail: u.Email, Action: "auth.reset_requested",
		EntityType: "user", EntityID: &u.ID, IP: ip, Details: jsonString(map[string]any{"user_agent": ua})})
}

// issueToken creates an invite or reset token and returns the link.
func (s *Server) issueToken(ctx context.Context, u *store.User, purpose string, by *int64, cfg settings.All, base string) (string, time.Time, error) {
	token, hash := auth.NewToken()
	now := s.now()
	ttl := time.Duration(cfg.Security.ResetTTLHours) * time.Hour
	path := "/reset/"
	if purpose == store.TokenInvite {
		ttl = time.Duration(cfg.Security.InviteTTLHours) * time.Hour
		path = "/invite/"
	}
	t := &store.UserToken{UserID: u.ID, Purpose: purpose, CreatedBy: by, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.Store.CreateUserToken(ctx, t, hash); err != nil {
		return "", time.Time{}, err
	}
	return base + path + token, t.ExpiresAt, nil
}

// resolveToken validates an invite/reset token and returns it with its user.
func (s *Server) resolveToken(ctx context.Context, token string) (*store.UserToken, *store.User, error) {
	invalid := &apiError{Status: http.StatusNotFound, Code: "invalid_token", Message: "the link is invalid or has expired"}
	if token == "" || len(token) > 128 {
		return nil, nil, invalid
	}
	t, err := s.Store.GetUserTokenByHash(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, invalid
	}
	if err != nil {
		return nil, nil, err
	}
	if t.UsedAt != nil || !s.now().Before(t.ExpiresAt) {
		return nil, nil, invalid
	}
	u, err := s.Store.GetUser(ctx, t.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, invalid
	}
	if err != nil {
		return nil, nil, err
	}
	switch {
	case t.Purpose == store.TokenInvite && u.Status == store.UserInvited:
	case t.Purpose == store.TokenReset && u.Status == store.UserActive:
	default:
		return nil, nil, invalid
	}
	return t, u, nil
}

func (s *Server) handleTokenInfo(w http.ResponseWriter, r *http.Request) error {
	if !s.sensitiveLimiter.allow("token:" + s.clientIP(r)) {
		return errTooManyRequests()
	}
	ctx := r.Context()
	t, u, err := s.resolveToken(ctx, r.PathValue("token"))
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purpose": t.Purpose, "email": u.Email, "name": u.Name, "role": u.Role, "expires_at": t.ExpiresAt,
		"password_policy": cfg.Security.Password, "app_name": cfg.General.AppName,
	})
	return nil
}

func (s *Server) handleTokenComplete(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if !s.sensitiveLimiter.allow("token:" + s.clientIP(r)) {
		return errTooManyRequests()
	}
	ctx := r.Context()
	t, u, err := s.resolveToken(ctx, r.PathValue("token"))
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	fe := validate.Errors{}
	name := validate.Text(fe, "name", in.Name, 128, false, false)
	if err := fe.Err(); err != nil {
		return err
	}
	pol := cfg.Security.Password
	violations := pol.Check(in.Password, u.Email)
	if len(violations) == 0 && t.Purpose == store.TokenReset && pol.HistoryCount > 0 {
		hist, err := s.Store.PasswordHistory(ctx, u.ID, pol.HistoryCount)
		if err != nil {
			return err
		}
		if auth.Reused(in.Password, hist) {
			violations = append(violations, auth.ViolationReused)
		}
	}
	if len(violations) > 0 {
		return weakPassword(violations)
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	if ok, err := s.Store.ConsumeUserToken(ctx, t.ID); err != nil {
		return err
	} else if !ok {
		return &apiError{Status: http.StatusNotFound, Code: "invalid_token", Message: "the link is invalid or has expired"}
	}
	invite := t.Purpose == store.TokenInvite
	if err := s.Store.SetUserPassword(ctx, u.ID, hash, invite, max(pol.HistoryCount, 1)); err != nil {
		return err
	}
	if invite && name != "" {
		if err := s.Store.UpdateUserProfile(ctx, u.ID, name); err != nil {
			return err
		}
	}
	_ = s.Store.DeleteUserSessions(ctx, u.ID, "")
	_ = s.Store.DeleteUserTokens(ctx, u.ID, "")
	if u, err = s.Store.GetUser(ctx, u.ID); err != nil {
		return err
	}
	if _, err := s.startSession(w, r, u, cfg); err != nil {
		return err
	}
	action := "auth.password_reset"
	if invite {
		action = "auth.invite_accepted"
	}
	s.auditUser(r, u, action, nil)
	writeJSON(w, http.StatusOK, s.mePayload(u, false, cfg))
	return nil
}

// ---- own account ----------------------------------------------------------

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) error {
	p := principalFrom(r)
	cfg, err := s.Settings.Get(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.mePayload(p.User, p.MustChangePassword, cfg))
	return nil
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p := principalFrom(r)
	fe := validate.Errors{}
	name := validate.Text(fe, "name", in.Name, 128, false, false)
	if err := fe.Err(); err != nil {
		return err
	}
	if err := s.Store.UpdateUserProfile(r.Context(), p.User.ID, name); err != nil {
		return err
	}
	p.User.Name = name
	return s.handleMe(w, r)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p := principalFrom(r)
	if !s.loginLimiter.allow("password:" + strconv.FormatInt(p.User.ID, 10)) {
		return errTooManyRequests()
	}
	ctx := r.Context()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	if !auth.VerifyPassword(p.User.PasswordHash, in.CurrentPassword) {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "validation", Message: "current password is wrong",
			Fields: map[string]string{"current_password": "wrong_password"}}
	}
	pol := cfg.Security.Password
	violations := pol.Check(in.NewPassword, p.User.Email)
	if len(violations) == 0 {
		hist := []string{p.User.PasswordHash}
		if pol.HistoryCount > 0 {
			if hist, err = s.Store.PasswordHistory(ctx, p.User.ID, pol.HistoryCount); err != nil {
				return err
			}
			hist = append(hist, p.User.PasswordHash)
		}
		if auth.Reused(in.NewPassword, hist) {
			violations = append(violations, auth.ViolationReused)
		}
	}
	if len(violations) > 0 {
		return weakPassword(violations)
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		return err
	}
	if err := s.Store.SetUserPassword(ctx, p.User.ID, hash, false, max(pol.HistoryCount, 1)); err != nil {
		return err
	}
	_ = s.Store.DeleteUserSessions(ctx, p.User.ID, p.Session.ID)
	s.audit(r, "auth.password_changed", "user", p.User.ID, nil)
	u, err := s.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.mePayload(u, false, cfg))
	return nil
}
