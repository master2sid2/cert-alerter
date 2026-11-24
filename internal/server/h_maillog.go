package server

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/store"
)

var allMailKinds = []string{store.MailAlert, store.MailGroupTest, store.MailTest, store.MailInvite, store.MailReset}

// mailKindsFor returns the journal kinds the user may see: invite and
// password reset e-mails concern application accounts and are admin only.
func mailKindsFor(r *http.Request) []string {
	if principalFrom(r).User.IsAdmin() {
		return allMailKinds
	}
	return []string{store.MailAlert, store.MailGroupTest, store.MailTest}
}

func (s *Server) mailFilter(r *http.Request) store.MailLogFilter {
	q := r.URL.Query()
	allowed := mailKindsFor(r)
	f := store.MailLogFilter{
		Channel: q.Get("channel"),
		Kinds:   allowed,
		Status:  q.Get("status"),
		Query:   q.Get("q"),
		Limit:   queryInt(r, "limit", 50),
		Offset:  queryInt(r, "offset", 0),
	}
	if k := q.Get("kind"); k != "" {
		if slices.Contains(allowed, k) {
			f.Kinds = []string{k}
		} else {
			f.Kinds = []string{"-"} // nothing visible
		}
	}
	if v, err := strconv.ParseInt(q.Get("group_id"), 10, 64); err == nil {
		f.GroupID = v
	}
	// from / to accept RFC 3339 timestamps or YYYY-MM-DD dates (to is inclusive).
	if t, ok := parseTimeParam(q.Get("from")); ok {
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		if t, ok := parseTimeParam(v); ok {
			if len(v) == len("2006-01-02") {
				t = t.AddDate(0, 0, 1)
			}
			f.To = t
		}
	}
	return f
}

func parseTimeParam(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (s *Server) handleListMailLog(w http.ResponseWriter, r *http.Request) error {
	f := s.mailFilter(r)
	items, total, err := s.Store.ListMailLog(r.Context(), f)
	if err != nil {
		return err
	}
	counts, err := s.Store.MailLogCounts(r.Context(), time.Now().AddDate(0, 0, -30), mailKindsFor(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "counts_30d": counts})
	return nil
}

func (s *Server) handleExportMailLog(w http.ResponseWriter, r *http.Request) error {
	f := s.mailFilter(r)
	f.Limit, f.Offset = 10000, 0
	items, _, err := s.Store.ListMailLog(r.Context(), f)
	if err != nil {
		return err
	}
	cfg, err := s.Settings.Get(r.Context())
	if err != nil {
		return err
	}
	loc := cfg.General.Location()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="delivery-log-%s.csv"`, time.Now().In(loc).Format("20060102-1504")))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM: lets spreadsheet apps detect UTF-8
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time", "channel", "kind", "recipient", "subject", "status", "error", "group", "certificates", "server", "duration_ms"})
	for _, e := range items {
		_ = cw.Write([]string{
			e.CreatedAt.In(loc).Format("2006-01-02 15:04:05 MST"), e.Channel, e.Kind, csvSafe(e.Recipient), csvSafe(e.Subject),
			e.Status, csvSafe(e.Error), csvSafe(e.GroupName), csvSafe(strings.Join(e.Certificates, " ")), e.SMTPServer,
			strconv.Itoa(e.DurationMS),
		})
	}
	cw.Flush()
	s.audit(r, "maillog.exported", "mail_log", 0, map[string]any{"rows": len(items)})
	return cw.Error()
}

// csvSafe prevents spreadsheet formula injection: cells starting with a
// formula character are prefixed with an apostrophe.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
