package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

type adminUserView struct {
	userView
	FailedLogins    int        `json:"failed_logins"`
	LockedUntil     *time.Time `json:"locked_until"`
	InviteExpiresAt *time.Time `json:"invite_expires_at"`
	Sessions        int        `json:"sessions"`
	InvitedBy       *int64     `json:"invited_by"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return err
	}
	invites, err := s.Store.InviteExpiries(ctx)
	if err != nil {
		return err
	}
	sessions, err := s.Store.CountUserSessions(ctx, s.now())
	if err != nil {
		return err
	}
	out := make([]adminUserView, len(users))
	for i := range users {
		u := &users[i]
		v := adminUserView{userView: viewUser(u), FailedLogins: u.FailedLogins, Sessions: sessions[u.ID], InvitedBy: u.InvitedBy}
		if u.IsLocked(s.now()) {
			v.LockedUntil = u.LockedUntil
		}
		if exp, ok := invites[u.ID]; ok && u.Status == store.UserInvited {
			v.InviteExpiresAt = &exp
		}
		out[i] = v
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type linkResult struct {
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expires_at"`
	EmailSent  bool      `json:"email_sent"`
	EmailError string    `json:"email_error,omitempty"`
}

// sendLink issues an invite or reset token for u and e-mails the link.
// The link is also returned so admins can pass it on manually when e-mail
// is not configured.
func (s *Server) sendLink(r *http.Request, u *store.User, purpose string) (*linkResult, error) {
	ctx := r.Context()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	p := principalFrom(r)
	link, expires, err := s.issueToken(ctx, u, purpose, &p.User.ID, cfg, s.baseURL(r, cfg))
	if err != nil {
		return nil, err
	}
	res := &linkResult{URL: link, ExpiresAt: expires}
	if !cfg.SMTP.Configured() {
		res.EmailError = mailer.ErrNotConfigured.Error()
		return res, nil
	}
	brand := alerts.Branding(cfg)
	brand.BaseURL = s.baseURL(r, cfg)
	var msg *mailer.Message
	if purpose == store.TokenInvite {
		inviter := p.User.Name
		if inviter == "" {
			inviter = p.User.Email
		}
		msg, err = brand.Invite(u.Email, u.Name, inviter, u.Role, link, expires)
	} else {
		msg, err = brand.Reset(u.Email, u.Name, link, expires)
	}
	if err != nil {
		return nil, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.SMTP.TimeoutSeconds+10)*time.Second)
	defer cancel()
	if err := s.Sender.Send(sendCtx, cfg.SMTP, []*mailer.Message{msg})[0]; err != nil {
		res.EmailError = err.Error()
	} else {
		res.EmailSent = true
	}
	return res, nil
}

func validRole(role string) bool { return role == store.RoleAdmin || role == store.RoleUser }

func (s *Server) handleInviteUser(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	fe := validate.Errors{}
	email, ok := validate.Email(in.Email)
	if !ok {
		fe.Add("email", validate.InvalidEmail)
	}
	name := validate.Text(fe, "name", in.Name, 128, false, false)
	if in.Role == "" {
		in.Role = store.RoleUser
	}
	if !validRole(in.Role) {
		fe.Add("role", validate.Invalid)
	}
	if err := fe.Err(); err != nil {
		return err
	}
	ctx := r.Context()
	p := principalFrom(r)
	u := &store.User{Email: email, Name: name, Role: in.Role, Status: store.UserInvited, InvitedBy: &p.User.ID}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errValidation(validate.Errors{"email": validate.Duplicate})
		}
		return err
	}
	res, err := s.sendLink(r, u, store.TokenInvite)
	if err != nil {
		_ = s.Store.DeleteUser(ctx, u.ID)
		return err
	}
	s.audit(r, "user.invited", "user", u.ID, map[string]any{"email": u.Email, "role": u.Role, "email_sent": res.EmailSent})
	writeJSON(w, http.StatusCreated, map[string]any{"user": viewUser(u), "invite": res})
	return nil
}

func (s *Server) handleResendInvite(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if u.Status != store.UserInvited {
		return errConflict("not_invited", "the user has already activated the account")
	}
	res, err := s.sendLink(r, u, store.TokenInvite)
	if err != nil {
		return err
	}
	s.audit(r, "user.invite_resent", "user", u.ID, map[string]any{"email": u.Email, "email_sent": res.EmailSent})
	writeJSON(w, http.StatusOK, map[string]any{"invite": res})
	return nil
}

func (s *Server) handleSendReset(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if u.Status != store.UserActive {
		return errConflict("not_active", "password reset is only possible for active accounts")
	}
	res, err := s.sendLink(r, u, store.TokenReset)
	if err != nil {
		return err
	}
	s.audit(r, "user.reset_sent", "user", u.ID, map[string]any{"email": u.Email, "email_sent": res.EmailSent})
	writeJSON(w, http.StatusOK, map[string]any{"reset": res})
	return nil
}

// guardAdminChange prevents removing the last active administrator.
func (s *Server) guardAdminChange(r *http.Request, target *store.User, newRole, newStatus string) error {
	if target.Role != store.RoleAdmin || target.Status != store.UserActive {
		return nil
	}
	if newRole == store.RoleAdmin && newStatus == store.UserActive {
		return nil
	}
	n, err := s.Store.CountActiveAdmins(r.Context())
	if err != nil {
		return err
	}
	if n <= 1 {
		return errConflict("last_admin", "at least one active administrator is required")
	}
	return nil
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Name   *string `json:"name"`
		Role   *string `json:"role"`
		Status *string `json:"status"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	p := principalFrom(r)
	name, role, status := u.Name, u.Role, u.Status
	fe := validate.Errors{}
	if in.Name != nil {
		name = validate.Text(fe, "name", *in.Name, 128, false, false)
	}
	if in.Role != nil {
		role = *in.Role
		if !validRole(role) {
			fe.Add("role", validate.Invalid)
		}
	}
	if in.Status != nil {
		status = *in.Status
		switch {
		case status == u.Status:
		case u.Status == store.UserInvited:
			fe.Add("status", validate.Invalid) // invited users activate themselves
		case status != store.UserActive && status != store.UserDisabled:
			fe.Add("status", validate.Invalid)
		}
	}
	if err := fe.Err(); err != nil {
		return err
	}
	if u.ID == p.User.ID && (role != u.Role || status != u.Status) {
		return errConflict("self_change", "you cannot change your own role or status")
	}
	if err := s.guardAdminChange(r, u, role, status); err != nil {
		return err
	}
	if err := s.Store.UpdateUserAccess(ctx, id, name, role, status); err != nil {
		return err
	}
	if status == store.UserDisabled && u.Status != store.UserDisabled {
		_ = s.Store.DeleteUserSessions(ctx, id, "")
		_ = s.Store.DeleteUserTokens(ctx, id, "")
	}
	s.audit(r, "user.updated", "user", id, map[string]any{"email": u.Email, "role": role, "status": status})
	u, err = s.Store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewUser(u))
	return nil
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.ID == principalFrom(r).User.ID {
		return errConflict("self_delete", "you cannot delete your own account")
	}
	if err := s.guardAdminChange(r, u, "", ""); err != nil {
		return err
	}
	if err := s.Store.DeleteUser(ctx, id); err != nil {
		return err
	}
	s.audit(r, "user.deleted", "user", id, map[string]any{"email": u.Email})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleUnlockUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.Store.UnlockUser(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "user.unlocked", "user", id, map[string]any{"email": u.Email})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- settings ---------------------------------------------------------------

type smtpView struct {
	settings.SMTP
	HasPassword bool `json:"has_password"`
}

func (s *Server) settingsPayload(cfg settings.All) map[string]any {
	sm := cfg.SMTP
	sm.PasswordEnc = ""
	return map[string]any{
		"smtp":            smtpView{SMTP: sm, HasPassword: cfg.SMTP.Password != ""},
		"monitoring":      cfg.Monitoring,
		"security":        cfg.Security,
		"general":         cfg.General,
		"base_url_locked": s.Settings.BaseURLLocked(),
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) error {
	cfg, err := s.Settings.Get(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.settingsPayload(cfg))
	return nil
}

type smtpInput struct {
	Host           string  `json:"host"`
	Port           int     `json:"port"`
	Security       string  `json:"security"`
	AuthMethod     string  `json:"auth_method"`
	Username       string  `json:"username"`
	Password       *string `json:"password"`
	FromEmail      string  `json:"from_email"`
	FromName       string  `json:"from_name"`
	SkipVerify     bool    `json:"skip_verify"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	HELO           string  `json:"helo"`
}

func (in smtpInput) toSettings() settings.SMTP {
	return settings.SMTP{Host: in.Host, Port: in.Port, Security: in.Security, AuthMethod: in.AuthMethod,
		Username: in.Username, FromEmail: in.FromEmail, FromName: in.FromName, SkipVerify: in.SkipVerify,
		TimeoutSeconds: in.TimeoutSeconds, HELO: in.HELO}
}

func (s *Server) handleSaveSMTP(w http.ResponseWriter, r *http.Request) error {
	var in smtpInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.Settings.SaveSMTP(ctx, in.toSettings(), in.Password); err != nil {
		return err
	}
	s.audit(r, "settings.smtp_updated", "settings", 0, map[string]any{
		"host": in.Host, "port": in.Port, "security": in.Security, "password_changed": in.Password != nil})
	s.Monitor.KickAlerts()
	return s.handleGetSettings(w, r)
}

func (s *Server) handleTestSMTP(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		To   string     `json:"to"`
		SMTP *smtpInput `json:"smtp"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	to, ok := validate.Email(in.To)
	if !ok {
		return errValidation(validate.Errors{"to": validate.InvalidEmail})
	}
	ctx := r.Context()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	smtp := cfg.SMTP
	if in.SMTP != nil { // test unsaved form values
		smtp = in.SMTP.toSettings()
		if in.SMTP.Password != nil {
			smtp.Password = *in.SMTP.Password
		} else {
			smtp.Password = cfg.SMTP.Password
		}
		if err := settings.ValidateSMTP(&smtp); err != nil {
			return err
		}
	}
	if !smtp.Configured() {
		return errConflict("smtp_not_configured", "SMTP is not configured")
	}
	msg, err := alerts.Branding(cfg).Test(to)
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, time.Duration(smtp.TimeoutSeconds+10)*time.Second)
	defer cancel()
	start := time.Now()
	sendErr := s.Sender.Send(sendCtx, smtp, []*mailer.Message{msg})[0]
	res := map[string]any{"ok": sendErr == nil, "duration_ms": time.Since(start).Milliseconds()}
	if sendErr != nil {
		res["error"] = sendErr.Error()
	}
	s.audit(r, "settings.smtp_tested", "settings", 0, map[string]any{"to": to, "ok": sendErr == nil})
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) handleSaveMonitoring(w http.ResponseWriter, r *http.Request) error {
	var in settings.Monitoring
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := s.Settings.SaveMonitoring(r.Context(), in); err != nil {
		return err
	}
	s.audit(r, "settings.monitoring_updated", "settings", 0, map[string]any{"thresholds": in.Thresholds,
		"interval": in.CheckIntervalMinutes})
	s.Monitor.KickAlerts()
	return s.handleGetSettings(w, r)
}

func (s *Server) handleSaveSecurity(w http.ResponseWriter, r *http.Request) error {
	var in settings.Security
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := s.Settings.SaveSecurity(r.Context(), in); err != nil {
		return err
	}
	s.audit(r, "settings.security_updated", "settings", 0, nil)
	return s.handleGetSettings(w, r)
}

func (s *Server) handleSaveGeneral(w http.ResponseWriter, r *http.Request) error {
	var in settings.General
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := s.Settings.SaveGeneral(r.Context(), in); err != nil {
		return err
	}
	s.audit(r, "settings.general_updated", "settings", 0, map[string]any{"base_url": in.BaseURL,
		"email_language": in.EmailLanguage, "timezone": in.Timezone})
	return s.handleGetSettings(w, r)
}
