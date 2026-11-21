package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/checker"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

type certView struct {
	store.Certificate
	Status      string `json:"status"`
	DaysLeft    *int   `json:"days_left"`
	CheckFailed bool   `json:"check_failed"`
}

func viewCert(c *store.Certificate, cfg settings.All, now time.Time) certView {
	v := certView{
		Certificate: *c,
		Status:      c.Status(now, cfg.Monitoring.WarningDays(), cfg.Monitoring.CriticalDays),
		CheckFailed: c.LastError != "",
	}
	if d, ok := c.DaysLeft(now); ok {
		v.DaysLeft = &d
	}
	return v
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// targetErrorCode maps checker.ParseTarget errors to validation codes.
func targetErrorCode(err error) string {
	switch {
	case errors.Is(err, checker.ErrEmpty):
		return validate.Required
	case errors.Is(err, checker.ErrInvalidPort):
		return "invalid_port"
	case errors.Is(err, checker.ErrUnknownScheme):
		return "unknown_scheme"
	}
	return "invalid_url"
}

func (s *Server) handleListCertificates(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	certs, err := s.Store.ListCertificates(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	out := make([]certView, len(certs))
	for i := range certs {
		out[i] = viewCert(&certs[i], cfg, now)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type certInput struct {
	URL      string  `json:"url"`
	Name     string  `json:"name"`
	Notes    string  `json:"notes"`
	Enabled  *bool   `json:"enabled"`
	GroupIDs []int64 `json:"group_ids"`
}

func (in *certInput) validate() (checker.Target, string, string, string, error) {
	fe := validate.Errors{}
	target, url, err := checker.ParseTarget(in.URL)
	if err != nil {
		fe.Add("url", targetErrorCode(err))
	} else if len(url) > 2000 {
		fe.Add("url", validate.TooLong)
	}
	name := validate.Text(fe, "name", in.Name, 255, false, false)
	notes := validate.Text(fe, "notes", in.Notes, 2000, false, true)
	return target, url, name, notes, fe.Err()
}

// ensureGroups verifies that all group ids exist.
func (s *Server) ensureGroups(r *http.Request, ids []int64) error {
	for _, id := range ids {
		if _, err := s.Store.GetGroup(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errValidation(validate.Errors{"group_ids": validate.Invalid})
			}
			return err
		}
	}
	return nil
}

func duplicateCert(id int64) error {
	return &apiError{Status: http.StatusConflict, Code: "duplicate",
		Message: "this endpoint is already monitored", Details: map[string]any{"id": id}}
}

func (s *Server) handleCreateCertificate(w http.ResponseWriter, r *http.Request) error {
	var in certInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	target, url, name, notes, err := in.validate()
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.ensureGroups(r, in.GroupIDs); err != nil {
		return err
	}
	if existing, err := s.Store.FindCertificateByEndpoint(ctx, target.Host, target.Port); err == nil {
		return duplicateCert(existing.ID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	p := principalFrom(r)
	c := &store.Certificate{URL: url, Host: target.Host, Port: target.Port, Name: name, Notes: notes,
		Enabled: true, CreatedBy: &p.User.ID}
	if err := s.Store.CreateCertificate(ctx, c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			if existing, err := s.Store.FindCertificateByEndpoint(ctx, target.Host, target.Port); err == nil {
				return duplicateCert(existing.ID)
			}
		}
		return err
	}
	for _, gid := range in.GroupIDs {
		if _, err := s.Store.AddCertificatesToGroup(ctx, gid, []int64{c.ID}, &p.User.ID); err != nil {
			return err
		}
	}
	s.audit(r, "certificate.created", "certificate", c.ID, map[string]any{"url": c.URL, "groups": in.GroupIDs})
	checked, err := s.Monitor.CheckCertificate(ctx, c.ID)
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, viewCert(checked, cfg, time.Now()))
	return nil
}

func (s *Server) handleBulkCreateCertificates(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Text     string   `json:"text"`
		URLs     []string `json:"urls"`
		GroupIDs []int64  `json:"group_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.ensureGroups(r, in.GroupIDs); err != nil {
		return err
	}
	entries := slices.Clone(in.URLs)
	for line := range strings.Lines(in.Text) {
		line, _, _ = strings.Cut(line, "#")
		for f := range strings.FieldsFuncSeq(line, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
			entries = append(entries, f)
		}
	}
	if len(entries) > 1000 {
		return errValidation(validate.Errors{"text": validate.TooLong})
	}
	type invalid struct {
		Input string `json:"input"`
		Error string `json:"error"`
	}
	var (
		created, existing []*store.Certificate
		bad               = []invalid{}
		seen              = map[string]bool{}
		p                 = principalFrom(r)
	)
	for _, raw := range entries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		target, url, err := checker.ParseTarget(raw)
		if err == nil && len(url) > 2000 {
			err = checker.ErrInvalidURL
		}
		if err != nil {
			bad = append(bad, invalid{Input: truncateRunes(raw, 200), Error: targetErrorCode(err)})
			continue
		}
		key := target.Address()
		if seen[key] {
			continue
		}
		seen[key] = true
		if c, err := s.Store.FindCertificateByEndpoint(ctx, target.Host, target.Port); err == nil {
			existing = append(existing, c)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		c := &store.Certificate{URL: url, Host: target.Host, Port: target.Port, Enabled: true, CreatedBy: &p.User.ID}
		if err := s.Store.CreateCertificate(ctx, c); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return err
		}
		created = append(created, c)
	}
	ids := make([]int64, 0, len(created)+len(existing))
	createdIDs := make([]int64, 0, len(created))
	for _, c := range created {
		ids = append(ids, c.ID)
		createdIDs = append(createdIDs, c.ID)
	}
	for _, c := range existing {
		ids = append(ids, c.ID)
	}
	for _, gid := range in.GroupIDs {
		if _, err := s.Store.AddCertificatesToGroup(ctx, gid, ids, &p.User.ID); err != nil {
			return err
		}
	}
	if len(created) > 0 || len(in.GroupIDs) > 0 {
		s.audit(r, "certificate.bulk_created", "certificate", 0, map[string]any{
			"created": len(created), "existing": len(existing), "invalid": len(bad), "groups": in.GroupIDs})
	}
	s.Monitor.CheckAsync(createdIDs)
	s.Monitor.KickAlerts()

	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	views := func(list []*store.Certificate) []certView {
		out := make([]certView, 0, len(list))
		for _, c := range list {
			if fresh, err := s.Store.GetCertificate(ctx, c.ID); err == nil {
				c = fresh
			}
			out = append(out, viewCert(c, cfg, now))
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": views(created), "existing": views(existing), "invalid": bad})
	return nil
}

func (s *Server) handleGetCertificate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	c, err := s.Store.GetCertificate(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	notifs, _, err := s.Store.ListNotifications(ctx, store.NotificationFilter{CertificateID: id, Limit: 25})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"certificate":   viewCert(c, cfg, time.Now()),
		"notifications": notifs,
	})
	return nil
}

func (s *Server) handleUpdateCertificate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in certInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	target, url, name, notes, err := in.validate()
	if err != nil {
		return err
	}
	ctx := r.Context()
	cur, err := s.Store.GetCertificate(ctx, id)
	if err != nil {
		return err
	}
	if other, err := s.Store.FindCertificateByEndpoint(ctx, target.Host, target.Port); err == nil && other.ID != id {
		return duplicateCert(other.ID)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	upd := *cur
	upd.URL, upd.Host, upd.Port, upd.Name, upd.Notes = url, target.Host, target.Port, name, notes
	if in.Enabled != nil {
		upd.Enabled = *in.Enabled
	}
	if err := s.Store.UpdateCertificate(ctx, &upd); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errConflict("duplicate", "this endpoint is already monitored")
		}
		return err
	}
	if cur.Host != upd.Host || cur.Port != upd.Port || (!cur.Enabled && upd.Enabled) {
		s.Monitor.CheckAsync([]int64{id})
	}
	s.Monitor.KickAlerts()
	s.audit(r, "certificate.updated", "certificate", id, map[string]any{"url": upd.URL, "enabled": upd.Enabled})
	c, err := s.Store.GetCertificate(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewCert(c, cfg, time.Now()))
	return nil
}

func (s *Server) handleDeleteCertificate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := s.Store.GetCertificate(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteCertificate(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "certificate.deleted", "certificate", id, map[string]any{"url": c.URL})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleCheckCertificate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	c, err := s.Monitor.CheckCertificate(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewCert(c, cfg, time.Now()))
	return nil
}

func (s *Server) handleCheckAll(w http.ResponseWriter, r *http.Request) error {
	started := s.Monitor.StartFullCheck()
	if started {
		s.audit(r, "certificate.check_all", "certificate", 0, nil)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started, "status": s.Monitor.Status(r.Context())})
	return nil
}

func (s *Server) handleCertificateAddGroups(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		GroupIDs []int64 `json:"group_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if _, err := s.Store.GetCertificate(ctx, id); err != nil {
		return err
	}
	if err := s.ensureGroups(r, in.GroupIDs); err != nil {
		return err
	}
	p := principalFrom(r)
	for _, gid := range in.GroupIDs {
		n, err := s.Store.AddCertificatesToGroup(ctx, gid, []int64{id}, &p.User.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			s.audit(r, "group.certificates_added", "group", gid, map[string]any{"certificate_ids": []int64{id}})
		}
	}
	s.Monitor.KickAlerts()
	c, err := s.Store.GetCertificate(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewCert(c, cfg, time.Now()))
	return nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
