// Package mailer renders and sends e-mails over SMTP.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/wneessen/go-mail"

	"github.com/master2sid2/cert-alerter/internal/settings"
)

// Message is a rendered e-mail for a single recipient.
type Message struct {
	To      string
	ToName  string
	Subject string
	Text    string
	HTML    string

	// Metadata recorded in the mail journal.
	Kind         string
	GroupID      int64
	GroupName    string
	Certificates []string
}

// Sender delivers messages. Send returns one error slot per message (nil
// on success) so callers can track delivery per recipient.
type Sender interface {
	Send(ctx context.Context, cfg settings.SMTP, msgs []*Message) []error
}

// ErrNotConfigured is returned when no SMTP server is configured.
var ErrNotConfigured = errors.New("SMTP is not configured")

// SMTPSender sends mail with github.com/wneessen/go-mail.
type SMTPSender struct{}

// Send delivers all messages over one SMTP connection.
func (SMTPSender) Send(ctx context.Context, cfg settings.SMTP, msgs []*Message) []error {
	errs := make([]error, len(msgs))
	fail := func(idx []int, err error) []error {
		for _, i := range idx {
			errs[i] = err
		}
		return errs
	}
	all := make([]int, len(msgs))
	for i := range msgs {
		all[i] = i
	}
	if !cfg.Configured() {
		return fail(all, ErrNotConfigured)
	}
	client, err := newClient(cfg)
	if err != nil {
		return fail(all, err)
	}

	var (
		built []*mail.Msg
		idx   []int
	)
	for i, m := range msgs {
		gm, err := buildMsg(cfg, m)
		if err != nil {
			errs[i] = err
			continue
		}
		built = append(built, gm)
		idx = append(idx, i)
	}
	if len(built) == 0 {
		return errs
	}

	if err := client.DialWithContext(ctx); err != nil {
		return fail(idx, fmt.Errorf("connect to %s:%d: %w", cfg.Host, cfg.Port, err))
	}
	defer client.Close()

	sendErr := client.Send(built...)
	anyTagged := false
	for j, gm := range built {
		if gm.HasSendError() {
			errs[idx[j]] = gm.SendError()
			anyTagged = true
		}
	}
	if sendErr != nil && !anyTagged {
		return fail(idx, sendErr)
	}
	return errs
}

func newClient(cfg settings.SMTP) (*mail.Client, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	opts := []mail.Option{
		mail.WithPort(cfg.Port),
		mail.WithTimeout(timeout),
		mail.WithTLSConfig(&tls.Config{
			ServerName:         cfg.Host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.SkipVerify, //nolint:gosec // explicit admin choice for internal relays
		}),
	}
	switch cfg.Security {
	case settings.SecurityTLS:
		opts = append(opts, mail.WithSSL())
	case settings.SecuritySTARTTLS:
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	default:
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	}
	if cfg.HELO != "" {
		opts = append(opts, mail.WithHELO(cfg.HELO))
	}
	if cfg.Username != "" && cfg.AuthMethod != "none" {
		plaintext := cfg.Security == settings.SecurityNone
		var at mail.SMTPAuthType
		switch cfg.AuthMethod {
		case "plain":
			at = mail.SMTPAuthPlain
			if plaintext {
				at = mail.SMTPAuthPlainNoEnc
			}
		case "login":
			at = mail.SMTPAuthLogin
			if plaintext {
				at = mail.SMTPAuthLoginNoEnc
			}
		case "cram-md5":
			at = mail.SMTPAuthCramMD5
		default:
			at = mail.SMTPAuthAutoDiscover
		}
		opts = append(opts, mail.WithSMTPAuth(at), mail.WithUsername(cfg.Username), mail.WithPassword(cfg.Password))
	}
	client, err := mail.NewClient(cfg.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("smtp client: %w", err)
	}
	return client, nil
}

func buildMsg(cfg settings.SMTP, m *Message) (*mail.Msg, error) {
	gm := mail.NewMsg()
	if err := gm.FromFormat(cfg.FromName, cfg.FromEmail); err != nil {
		return nil, fmt.Errorf("invalid sender address: %w", err)
	}
	var err error
	if m.ToName != "" {
		err = gm.AddToFormat(m.ToName, m.To)
	} else {
		err = gm.To(m.To)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid recipient address %q: %w", m.To, err)
	}
	gm.Subject(m.Subject)
	gm.SetMessageID()
	gm.SetDate()
	gm.SetUserAgent("Cert Alerter")
	gm.SetGenHeader(mail.Header("Auto-Submitted"), "auto-generated")
	gm.SetGenHeader(mail.HeaderXAutoResponseSuppress, "All")
	gm.SetBodyString(mail.TypeTextPlain, m.Text)
	if m.HTML != "" {
		gm.AddAlternativeString(mail.TypeTextHTML, m.HTML)
	}
	return gm, nil
}
