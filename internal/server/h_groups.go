package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

// statusRank orders statuses from worst to best.
var statusRank = map[string]int{
	store.StatusExpired: 0, store.StatusCritical: 1, store.StatusError: 2, store.StatusWarning: 3,
	store.StatusPending: 4, store.StatusOK: 5,
}

type groupHealth struct {
	Worst       string         `json:"worst"`
	Counts      map[string]int `json:"counts"`
	MinDaysLeft *int           `json:"min_days_left"`
}

type groupView struct {
	store.Group
	Health groupHealth `json:"health"`
}

func healthOf(certs []certView) groupHealth {
	h := groupHealth{Counts: map[string]int{}}
	for _, c := range certs {
		if !c.Enabled {
			h.Counts["disabled"]++
			continue
		}
		h.Counts[c.Status]++
		if h.Worst == "" || statusRank[c.Status] < statusRank[h.Worst] {
			h.Worst = c.Status
		}
		if c.DaysLeft != nil && (h.MinDaysLeft == nil || *c.DaysLeft < *h.MinDaysLeft) {
			h.MinDaysLeft = new(*c.DaysLeft)
		}
	}
	return h
}

// groupViews computes group health from all certificates.
func (s *Server) groupViews(r *http.Request, cfg settings.All) ([]groupView, map[int64]certView, error) {
	ctx := r.Context()
	groups, err := s.Store.ListGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	certs, err := s.Store.ListCertificates(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	byID := make(map[int64]certView, len(certs))
	byGroup := map[int64][]certView{}
	for i := range certs {
		v := viewCert(&certs[i], cfg, now)
		byID[v.ID] = v
		for _, g := range v.GroupIDs {
			byGroup[g] = append(byGroup[g], v)
		}
	}
	out := make([]groupView, len(groups))
	for i, g := range groups {
		out[i] = groupView{Group: g, Health: healthOf(byGroup[g.ID])}
	}
	return out, byID, nil
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) error {
	cfg, err := s.Settings.Get(r.Context())
	if err != nil {
		return err
	}
	groups, _, err := s.groupViews(r, cfg)
	if err != nil {
		return err
	}
	if groups == nil {
		groups = []groupView{}
	}
	writeJSON(w, http.StatusOK, groups)
	return nil
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	g, err := s.Store.GetGroup(ctx, id)
	if err != nil {
		return err
	}
	certIDs, err := s.Store.GroupCertificateIDs(ctx, id)
	if err != nil {
		return err
	}
	certs := make([]certView, 0, len(certIDs))
	now := time.Now()
	for _, cid := range certIDs {
		c, err := s.Store.GetCertificate(ctx, cid)
		if err != nil {
			continue
		}
		certs = append(certs, viewCert(c, cfg, now))
	}
	slices.SortFunc(certs, func(a, b certView) int { return compareExpiry(a, b) })
	recipients, err := s.Store.GroupRecipients(ctx, id)
	if err != nil {
		return err
	}
	if recipients == nil {
		recipients = []store.Recipient{}
	}
	notifs, _, err := s.Store.ListNotifications(ctx, store.NotificationFilter{GroupID: id, Limit: 25})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group":         groupView{Group: *g, Health: healthOf(certs)},
		"certificates":  certs,
		"recipients":    recipients,
		"notifications": notifs,
	})
	return nil
}

// compareExpiry sorts certificates by expiry, unknown expiry last.
func compareExpiry(a, b certView) int {
	switch {
	case a.NotAfter == nil && b.NotAfter == nil:
		return strings.Compare(a.URL, b.URL)
	case a.NotAfter == nil:
		return 1
	case b.NotAfter == nil:
		return -1
	}
	if c := a.NotAfter.Compare(*b.NotAfter); c != 0 {
		return c
	}
	return strings.Compare(a.URL, b.URL)
}

type groupInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (in groupInput) validate() (string, string, error) {
	fe := validate.Errors{}
	name := validate.Text(fe, "name", in.Name, 100, true, false)
	desc := validate.Text(fe, "description", in.Description, 2000, false, true)
	return name, desc, fe.Err()
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) error {
	var in groupInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	name, desc, err := in.validate()
	if err != nil {
		return err
	}
	g := &store.Group{Name: name, Description: desc}
	if err := s.Store.CreateGroup(r.Context(), g); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errValidation(validate.Errors{"name": validate.Duplicate})
		}
		return err
	}
	s.audit(r, "group.created", "group", g.ID, map[string]any{"name": g.Name})
	writeJSON(w, http.StatusCreated, groupView{Group: *g, Health: healthOf(nil)})
	return nil
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in groupInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	name, desc, err := in.validate()
	if err != nil {
		return err
	}
	g := &store.Group{ID: id, Name: name, Description: desc}
	if err := s.Store.UpdateGroup(r.Context(), g); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errValidation(validate.Errors{"name": validate.Duplicate})
		}
		return err
	}
	s.audit(r, "group.updated", "group", id, map[string]any{"name": name})
	g, err = s.Store.GetGroup(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, g)
	return nil
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	g, err := s.Store.GetGroup(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteGroup(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "group.deleted", "group", id, map[string]any{"name": g.Name})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleGroupAddCertificates(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		CertificateIDs []int64 `json:"certificate_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if len(in.CertificateIDs) == 0 {
		return errValidation(validate.Errors{"certificate_ids": validate.Required})
	}
	if len(in.CertificateIDs) > 5000 {
		return errValidation(validate.Errors{"certificate_ids": validate.TooLong})
	}
	p := principalFrom(r)
	n, err := s.Store.AddCertificatesToGroup(r.Context(), id, in.CertificateIDs, &p.User.ID)
	if errors.Is(err, store.ErrNotFound) {
		if _, gerr := s.Store.GetGroup(r.Context(), id); gerr != nil {
			return gerr
		}
		return errValidation(validate.Errors{"certificate_ids": validate.Invalid})
	}
	if err != nil {
		return err
	}
	if n > 0 {
		s.audit(r, "group.certificates_added", "group", id, map[string]any{"certificate_ids": in.CertificateIDs})
		s.Monitor.KickAlerts()
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": n})
	return nil
}

func (s *Server) handleGroupRemoveCertificate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	certID, err := pathID(r, "certID")
	if err != nil {
		return err
	}
	if err := s.Store.RemoveCertificateFromGroup(r.Context(), id, certID); err != nil {
		return err
	}
	s.audit(r, "group.certificate_removed", "group", id, map[string]any{"certificate_id": certID})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleGroupAddRecipients(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		RecipientIDs []int64  `json:"recipient_ids"`
		Emails       []string `json:"emails"`
		Text         string   `json:"text"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if _, err := s.Store.GetGroup(ctx, id); err != nil {
		return err
	}
	emails := slices.Clone(in.Emails)
	emails = append(emails, strings.FieldsFunc(in.Text, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})...)
	var invalid []string
	var valid []string
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		addr, ok := validate.Email(e)
		if !ok {
			invalid = append(invalid, e)
			continue
		}
		valid = append(valid, addr)
	}
	if len(invalid) > 0 {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "validation", Message: "invalid e-mail addresses",
			Fields: map[string]string{"emails": validate.InvalidEmail}, Details: map[string]any{"invalid": invalid}}
	}
	if len(valid)+len(in.RecipientIDs) == 0 {
		return errValidation(validate.Errors{"emails": validate.Required})
	}
	ids := slices.Clone(in.RecipientIDs)
	createdCount := 0
	for _, addr := range valid {
		rcpt, created, err := s.Store.EnsureRecipient(ctx, addr, "")
		if err != nil {
			return err
		}
		if created {
			createdCount++
			s.audit(r, "recipient.created", "recipient", rcpt.ID, map[string]any{"email": rcpt.Email})
		}
		ids = append(ids, rcpt.ID)
	}
	n, err := s.Store.AddRecipientsToGroup(ctx, id, ids)
	if errors.Is(err, store.ErrNotFound) {
		return errValidation(validate.Errors{"recipient_ids": validate.Invalid})
	}
	if err != nil {
		return err
	}
	if n > 0 {
		s.audit(r, "group.recipients_added", "group", id, map[string]any{"recipient_ids": ids})
		s.Monitor.KickAlerts()
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": n, "created": createdCount})
	return nil
}

func (s *Server) handleGroupRemoveRecipient(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	rid, err := pathID(r, "recipientID")
	if err != nil {
		return err
	}
	if err := s.Store.RemoveRecipientFromGroup(r.Context(), id, rid); err != nil {
		return err
	}
	s.audit(r, "group.recipient_removed", "group", id, map[string]any{"recipient_id": rid})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleGroupTest(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	results, err := s.Alerts.SendGroupTest(r.Context(), id)
	switch {
	case errors.Is(err, mailer.ErrNotConfigured):
		return errConflict("smtp_not_configured", "SMTP is not configured")
	case errors.Is(err, alerts.ErrNoRecipients):
		return errConflict("no_recipients", "the group has no recipients")
	case err != nil:
		return err
	}
	failed := 0
	for _, res := range results {
		if res.Error != "" {
			failed++
		}
	}
	s.audit(r, "group.test_sent", "group", id, map[string]any{"recipients": len(results), "failed": failed})
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
	return nil
}

// ---- recipients ------------------------------------------------------------

func (s *Server) handleListRecipients(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Store.ListRecipients(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

type recipientInput struct {
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	GroupIDs *[]int64 `json:"group_ids"`
}

func (in recipientInput) validate() (string, string, error) {
	fe := validate.Errors{}
	email, ok := validate.Email(in.Email)
	if !ok {
		fe.Add("email", validate.InvalidEmail)
	}
	name := validate.Text(fe, "name", in.Name, 128, false, false)
	return email, name, fe.Err()
}

func (s *Server) handleCreateRecipient(w http.ResponseWriter, r *http.Request) error {
	var in recipientInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	email, name, err := in.validate()
	if err != nil {
		return err
	}
	ctx := r.Context()
	rc := &store.Recipient{Email: email, Name: name}
	if err := s.Store.CreateRecipient(ctx, rc); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errValidation(validate.Errors{"email": validate.Duplicate})
		}
		return err
	}
	if in.GroupIDs != nil {
		if err := s.Store.SetRecipientGroups(ctx, rc.ID, *in.GroupIDs); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errValidation(validate.Errors{"group_ids": validate.Invalid})
			}
			return err
		}
		s.Monitor.KickAlerts()
	}
	s.audit(r, "recipient.created", "recipient", rc.ID, map[string]any{"email": rc.Email})
	rc, err = s.Store.GetRecipient(ctx, rc.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, rc)
	return nil
}

func (s *Server) handleUpdateRecipient(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in recipientInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	email, name, err := in.validate()
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.Store.UpdateRecipient(ctx, &store.Recipient{ID: id, Email: email, Name: name}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errValidation(validate.Errors{"email": validate.Duplicate})
		}
		return err
	}
	if in.GroupIDs != nil {
		if err := s.Store.SetRecipientGroups(ctx, id, *in.GroupIDs); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errValidation(validate.Errors{"group_ids": validate.Invalid})
			}
			return err
		}
	}
	s.Monitor.KickAlerts()
	s.audit(r, "recipient.updated", "recipient", id, map[string]any{"email": email})
	rc, err := s.Store.GetRecipient(ctx, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rc)
	return nil
}

func (s *Server) handleDeleteRecipient(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	rc, err := s.Store.GetRecipient(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteRecipient(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "recipient.deleted", "recipient", id, map[string]any{"email": rc.Email})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
