// Package notify delivers alerts to messengers (Telegram, Slack, generic
// webhooks).
//
// A messenger is a Provider. Each provider describes its configuration as
// a list of fields: provider settings shared by all channels of the type
// (for example the Telegram bot token) and target fields of one channel
// (for example the chat id). The web interface renders its forms from these
// descriptions, so adding a messenger takes one file in this package that
// implements Provider and registers it in init:
//
//	func init() { Register(myProvider{}) }
package notify

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Field kinds understood by the web interface.
const (
	KindText     = "text"
	KindTextarea = "textarea"
	KindSecret   = "secret" // stored encrypted, never sent back to the browser
	KindURL      = "url"
	KindBool     = "bool"
	KindSelect   = "select"
)

// Option is one choice of a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field describes one configuration value.
type Field struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"`
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []Option `json:"options,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"` // 0 = 2000
}

func (f Field) maxLength() int { return cmp.Or(f.MaxLength, 2000) }

// Info describes a provider.
type Info struct {
	Type        string  `json:"type"` // stable identifier stored in the database
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Settings    []Field `json:"settings"` // shared by all channels of this type
	Target      []Field `json:"target"`   // per channel
}

// Config holds field values by name. Booleans are "true" or "false".
type Config map[string]string

// Bool reports whether a bool field is set.
func (c Config) Bool(name string) bool { return c[name] == "true" }

// Clone returns a copy.
func (c Config) Clone() Config {
	out := make(Config, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

// Target is everything a provider needs to deliver to one channel.
type Target struct {
	Settings Config // provider settings
	Config   Config // channel fields
}

// Provider delivers messages to one kind of messenger.
type Provider interface {
	Info() Info
	// Send delivers msg. Errors must not contain secrets (tokens in URLs):
	// they are stored and shown in the web interface.
	Send(ctx context.Context, c *Client, t Target, msg *Message) error
}

// Validator is implemented by providers that check more than the generic
// field rules (required values, URL syntax, select options). The returned
// map holds field name -> error code (see package validate).
type Validator interface {
	Validate(t Target) map[string]string
}

// Summarizer is implemented by providers that describe a channel in one
// short line for lists, without secrets (for example "chat -1001234").
type Summarizer interface {
	Summary(t Target) string
}

var (
	regMu     sync.RWMutex
	providers = map[string]Provider{}
)

// Register adds a provider; it panics on a duplicate type.
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	typ := p.Info().Type
	if _, dup := providers[typ]; dup || typ == "" {
		panic("notify: invalid or duplicate provider type " + strconv.Quote(typ))
	}
	providers[typ] = p
}

// Lookup returns the provider of a type.
func Lookup(typ string) (Provider, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := providers[typ]
	return p, ok
}

// Providers returns all providers ordered by label.
func Providers() []Provider {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Provider, 0, len(providers))
	for _, p := range providers {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Provider) int { return strings.Compare(a.Info().Label, b.Info().Label) })
	return out
}

// Message kinds.
const (
	KindAlert       = "alert"
	KindTest        = "group_test"   // test notification of a group
	KindChannelTest = "channel_test" // test message of a channel
)

// Item is one certificate in a message.
type Item struct {
	Name      string
	URL       string
	SubjectCN string
	IssuerCN  string
	NotAfter  time.Time // zero when unknown (test messages)
	DaysLeft  int
	Link      string // page of the certificate in the web interface, may be empty
}

// Message is a provider independent alert or test notification about the
// certificates of one group.
type Message struct {
	Kind         string
	AppName      string
	AppURL       string // may be empty
	GroupName    string
	GroupURL     string // may be empty
	ChannelName  string // channel test messages
	Items        []Item // most urgent first
	CriticalDays int
	Thresholds   []int // test messages: the configured alert stages
	Location     *time.Location
}

// MaxItems is the number of certificates listed in one messenger message;
// the rest is summarised as "and N more".
const MaxItems = 25

// Severity of an item: "expired", "critical" or "warning".
func (m *Message) Severity(it Item) string {
	switch {
	case it.NotAfter.IsZero():
		return ""
	case it.DaysLeft < 0:
		return "expired"
	case it.DaysLeft <= m.CriticalDays:
		return "critical"
	}
	return "warning"
}

// WorstSeverity returns the severity of the most urgent item.
func (m *Message) WorstSeverity() string {
	worst := ""
	rank := map[string]int{"": 0, "warning": 1, "critical": 2, "expired": 3}
	for _, it := range m.Items {
		if s := m.Severity(it); rank[s] > rank[worst] {
			worst = s
		}
	}
	return worst
}

// Title is the headline of the message.
func (m *Message) Title() string {
	if m.Kind == KindChannelTest {
		return fmt.Sprintf("Test message from %s", m.app())
	}
	if m.Kind == KindTest {
		return fmt.Sprintf("[%s] Test notification from %s", m.GroupName, m.app())
	}
	if len(m.Items) == 1 {
		it := m.Items[0]
		if it.DaysLeft < 0 {
			return fmt.Sprintf("[%s] SSL certificate %s has EXPIRED", m.GroupName, it.Host())
		}
		return fmt.Sprintf("[%s] SSL certificate %s expires %s", m.GroupName, it.Host(), When(it.DaysLeft))
	}
	return fmt.Sprintf("[%s] SSL certificates requiring attention: %d", m.GroupName, len(m.Items))
}

// Intro is the sentence below the title.
func (m *Message) Intro() string {
	if m.Kind == KindChannelTest {
		return fmt.Sprintf("If you can read this, the channel “%s” is configured correctly.", m.ChannelName)
	}
	if m.Kind != KindTest {
		return fmt.Sprintf("The following certificates of group “%s” require attention:", m.GroupName)
	}
	ts := make([]string, len(m.Thresholds))
	for i, t := range m.Thresholds {
		ts[i] = strconv.Itoa(t)
	}
	s := fmt.Sprintf("This is a test notification for group “%s”. Real alerts are sent when a certificate of this group expires in %s days.",
		m.GroupName, strings.Join(ts, ", "))
	if len(m.Items) == 0 {
		return s + " The group has no certificates yet."
	}
	return s + " Certificates currently in the group:"
}

// Shown returns the items to list and how many were left out.
func (m *Message) Shown() ([]Item, int) {
	if len(m.Items) <= MaxItems {
		return m.Items, 0
	}
	return m.Items[:MaxItems], len(m.Items) - MaxItems
}

// Expires formats the expiry of an item ("Mar 5, 2026 09:00 UTC (in 4 days)").
func (m *Message) Expires(it Item) string {
	if it.NotAfter.IsZero() {
		return "not checked yet"
	}
	loc := m.Location
	if loc == nil {
		loc = time.UTC
	}
	return it.NotAfter.In(loc).Format("Jan 2, 2006 15:04 MST") + " (" + When(it.DaysLeft) + ")"
}

func (m *Message) app() string { return cmp.Or(m.AppName, "Cert Alerter") }

// Title of an item: its name, or the endpoint.
func (it Item) Title() string { return cmp.Or(it.Name, it.URL) }

// Host returns the host[:port] of the endpoint, quoted with the name if any.
func (it Item) Host() string {
	h := it.URL
	if _, rest, ok := strings.Cut(h, "://"); ok {
		h = rest
	}
	h, _, _ = strings.Cut(h, "/")
	if it.Name != "" {
		return "“" + it.Name + "” (" + h + ")"
	}
	return h
}

// When renders "in 5 days" or "expired 2 days ago" from whole days left
// (negative once expired, -1 = less than a day ago).
func When(daysLeft int) string {
	if daysLeft >= 0 {
		return "in " + days(daysLeft)
	}
	ago := -daysLeft - 1
	if ago == 0 {
		return "expired less than a day ago"
	}
	return "expired " + days(ago) + " ago"
}

func days(n int) string {
	switch {
	case n <= 0:
		return "less than a day"
	case n == 1:
		return "1 day"
	}
	return strconv.Itoa(n) + " days"
}

// PlainText renders the message as plain text (webhooks, fallbacks).
func (m *Message) PlainText() string {
	var b strings.Builder
	b.WriteString(m.Title() + "\n\n" + m.Intro() + "\n")
	items, more := m.Shown()
	for _, it := range items {
		fmt.Fprintf(&b, "\n• %s\n", it.Title())
		if it.Name != "" {
			fmt.Fprintf(&b, "  %s\n", it.URL)
		}
		fmt.Fprintf(&b, "  Expires: %s\n", m.Expires(it))
		if it.Link != "" {
			fmt.Fprintf(&b, "  %s\n", it.Link)
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n…and %d more\n", more)
	}
	if m.Kind == KindAlert {
		b.WriteString("\nPlease renew the certificates before they expire to avoid service outages.\n")
	}
	return b.String()
}
