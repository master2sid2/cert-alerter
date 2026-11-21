package mailer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
)

// Journal wraps a Sender and records every delivery attempt in the mail
// log: kind, recipient, subject, time, SMTP result.
type Journal struct {
	Next  Sender
	Store *store.Store
	Log   *slog.Logger
}

// Send delivers the messages via the wrapped sender and journals the results.
func (j *Journal) Send(ctx context.Context, cfg settings.SMTP, msgs []*Message) []error {
	start := time.Now()
	errs := j.Next.Send(ctx, cfg, msgs)
	perMessage := int(time.Since(start).Milliseconds()) / max(len(msgs), 1)
	server := ""
	if cfg.Host != "" {
		server = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	}
	logCtx := context.WithoutCancel(ctx)
	for i, m := range msgs {
		e := &store.MailLogEntry{
			Kind: m.Kind, Recipient: m.To, Subject: m.Subject, Status: store.MailSent,
			GroupName: m.GroupName, Certificates: m.Certificates, CertCount: len(m.Certificates),
			SMTPServer: server, DurationMS: perMessage,
		}
		if e.Kind == "" {
			e.Kind = "other"
		}
		if m.GroupID > 0 {
			e.GroupID = new(m.GroupID)
		}
		if errs[i] != nil {
			e.Status, e.Error = store.MailFailed, errs[i].Error()
		}
		err := j.Store.AddMailLog(logCtx, e)
		if err != nil && db.IsForeignKeyViolation(err) { // group deleted meanwhile
			e.GroupID = nil
			err = j.Store.AddMailLog(logCtx, e)
		}
		if err != nil {
			j.Log.Error("mail journal", "to", m.To, "err", err)
		}
	}
	return errs
}
