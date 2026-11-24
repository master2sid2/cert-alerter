package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/secret"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

// settingsKey is the settings row with the provider settings.
const settingsKey = "channel_providers"

// SendTimeout bounds one delivery.
const SendTimeout = 20 * time.Second

// ErrUnknownType is returned for a channel type without provider.
var ErrUnknownType = errors.New("unknown channel type")

// Service manages channels and provider settings (secrets are encrypted
// at rest) and delivers messages, recording every attempt in the delivery
// journal.
type Service struct {
	store  *store.Store
	box    *secret.Box
	client *Client
	log    *slog.Logger
}

// NewService creates a Service.
func NewService(st *store.Store, box *secret.Box, client *Client, log *slog.Logger) *Service {
	return &Service{store: st, box: box, client: client, log: log}
}

type providerState struct {
	Enabled  bool   `json:"enabled"`
	Settings Config `json:"settings"` // secret values encrypted
}

func (s *Service) states(ctx context.Context) (map[string]providerState, error) {
	out := map[string]providerState{}
	raw, ok, err := s.store.GetSetting(ctx, settingsKey)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			s.log.Error("invalid stored channel provider settings", "err", err)
			out = map[string]providerState{}
		}
	}
	for _, p := range Providers() {
		typ := p.Info().Type
		if _, ok := out[typ]; !ok {
			out[typ] = providerState{Enabled: true, Settings: Config{}}
		}
	}
	return out, nil
}

// decrypt returns cfg with the secret fields decrypted.
func (s *Service) decrypt(fields []Field, cfg Config) Config {
	out := cfg.Clone()
	for _, name := range Secrets(fields) {
		if v := out[name]; v != "" {
			plain, err := s.box.Decrypt(v)
			if err != nil {
				s.log.Error("cannot decrypt channel secret", "field", name, "err", err)
			}
			out[name] = plain
		}
	}
	return out
}

// encrypt returns cfg with the secret fields encrypted.
func (s *Service) encrypt(fields []Field, cfg Config) (Config, error) {
	out := cfg.Clone()
	for _, name := range Secrets(fields) {
		enc, err := s.box.Encrypt(out[name])
		if err != nil {
			return nil, err
		}
		out[name] = enc
	}
	return out, nil
}

// public returns cfg without secret values and the names of the secrets
// that are set.
func public(fields []Field, cfg Config) (Config, []string) {
	out := Config{}
	set := []string{}
	for _, f := range fields {
		if f.Kind == KindSecret {
			if cfg[f.Name] != "" {
				set = append(set, f.Name)
			}
			continue
		}
		out[f.Name] = cfg[f.Name]
	}
	return out, set
}

func missingRequired(fields []Field, cfg Config) bool {
	return slices.ContainsFunc(fields, func(f Field) bool { return f.Required && cfg[f.Name] == "" })
}

// ProviderView describes a provider and its state for the web interface.
type ProviderView struct {
	Info
	Enabled bool `json:"enabled"`
	// Configured is false while required provider settings are missing.
	Configured bool     `json:"configured"`
	Values     Config   `json:"values"`      // settings without secrets
	SecretsSet []string `json:"secrets_set"` // secret settings that have a value
	Channels   int      `json:"channels"`
}

// Providers lists every provider with its settings.
func (s *Service) Providers(ctx context.Context) ([]ProviderView, error) {
	states, err := s.states(ctx)
	if err != nil {
		return nil, err
	}
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	var out []ProviderView
	for _, p := range Providers() {
		info := p.Info()
		if info.Settings == nil {
			info.Settings = []Field{}
		}
		if info.Target == nil {
			info.Target = []Field{}
		}
		st := states[info.Type]
		v := ProviderView{Info: info, Enabled: st.Enabled, Configured: !missingRequired(info.Settings, st.Settings)}
		v.Values, v.SecretsSet = public(info.Settings, st.Settings)
		for _, c := range channels {
			if c.Type == info.Type {
				v.Channels++
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// ProviderInput changes the state of a provider.
type ProviderInput struct {
	Enabled  bool     `json:"enabled"`
	Settings Config   `json:"settings"`
	Clear    []string `json:"clear"` // secret settings to remove
}

// SaveProvider validates and stores the settings of a provider. Empty
// secret values keep the stored secret unless listed in Clear.
func (s *Service) SaveProvider(ctx context.Context, typ string, in ProviderInput) error {
	p, ok := Lookup(typ)
	if !ok {
		return ErrUnknownType
	}
	info := p.Info()
	states, err := s.states(ctx)
	if err != nil {
		return err
	}
	cur := s.decrypt(info.Settings, states[typ].Settings)
	merged := mergeSecrets(info.Settings, in.Settings, cur, in.Clear)
	cfg, errs := Normalize(info.Settings, merged)
	if v, ok := p.(Validator); ok {
		for k, code := range v.Validate(Target{Settings: cfg, Config: Config{}}) {
			if _, dup := errs[k]; !dup {
				errs[k] = code
			}
		}
	}
	if !in.Enabled { // a disabled provider may stay incomplete
		for k, code := range errs {
			if code == validate.Required {
				delete(errs, k)
			}
		}
	}
	if len(errs) > 0 {
		return validate.Errors(errs)
	}
	enc, err := s.encrypt(info.Settings, cfg)
	if err != nil {
		return err
	}
	states[typ] = providerState{Enabled: in.Enabled, Settings: enc}
	data, err := json.Marshal(states)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, settingsKey, string(data))
}

// mergeSecrets fills empty secret values of in from cur (unless cleared).
func mergeSecrets(fields []Field, in, cur Config, clear []string) Config {
	out := in.Clone()
	for _, name := range Secrets(fields) {
		if out[name] == "" && !slices.Contains(clear, name) {
			out[name] = cur[name]
		}
	}
	return out
}

// Available returns the channel types that can deliver now: registered,
// enabled and configured.
func (s *Service) Available(ctx context.Context) (map[string]bool, error) {
	states, err := s.states(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, p := range Providers() {
		info := p.Info()
		st := states[info.Type]
		out[info.Type] = st.Enabled && !missingRequired(info.Settings, st.Settings)
	}
	return out, nil
}

// ChannelView is a channel as shown in the web interface.
type ChannelView struct {
	store.Channel
	Label      string   `json:"type_label"`
	Config     Config   `json:"config"`      // without secrets
	SecretsSet []string `json:"secrets_set"` // secret fields that have a value
	Summary    string   `json:"summary"`
	// Available is false when the provider is disabled, not configured or
	// unknown (for example after a downgrade).
	Available bool `json:"available"`
}

func (s *Service) view(c *store.Channel, states map[string]providerState) ChannelView {
	v := ChannelView{Channel: *c, Label: c.Type, Config: Config{}, SecretsSet: []string{}}
	p, ok := Lookup(c.Type)
	if !ok {
		return v
	}
	info := p.Info()
	st := states[c.Type]
	v.Label = info.Label
	v.Available = st.Enabled && !missingRequired(info.Settings, st.Settings)
	v.Config, v.SecretsSet = public(info.Target, c.Config)
	if sm, ok := p.(Summarizer); ok {
		t := Target{Settings: Config{}, Config: s.decrypt(info.Target, c.Config)}
		v.Summary = sm.Summary(t)
	}
	return v
}

// Channels lists all channels.
func (s *Service) Channels(ctx context.Context) ([]ChannelView, error) {
	states, err := s.states(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.store.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelView, len(list))
	for i := range list {
		out[i] = s.view(&list[i], states)
	}
	return out, nil
}

// GroupChannels lists the channels of a group.
func (s *Service) GroupChannels(ctx context.Context, groupID int64) ([]ChannelView, error) {
	states, err := s.states(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.store.GroupChannels(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelView, len(list))
	for i := range list {
		out[i] = s.view(&list[i], states)
	}
	return out, nil
}

// Channel returns one channel.
func (s *Service) Channel(ctx context.Context, id int64) (*ChannelView, error) {
	states, err := s.states(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	v := s.view(c, states)
	return &v, nil
}

// ChannelInput creates or changes a channel.
type ChannelInput struct {
	Type     string   `json:"type"` // ignored on update
	Name     string   `json:"name"`
	Enabled  *bool    `json:"enabled"` // nil = true on create, unchanged on update
	Config   Config   `json:"config"`
	Clear    []string `json:"clear"`     // secret fields to remove
	GroupIDs *[]int64 `json:"group_ids"` // nil = unchanged
}

// prepare validates the input against the stored channel (nil = new) and
// returns the channel with plain text configuration.
func (s *Service) prepare(ctx context.Context, cur *store.Channel, in ChannelInput) (*store.Channel, Provider, error) {
	e := validate.Errors{}
	c := &store.Channel{Enabled: true}
	if cur != nil {
		*c = *cur
	} else {
		c.Type = in.Type
	}
	p, ok := Lookup(c.Type)
	if !ok {
		e.Add("type", validate.Invalid)
		return nil, nil, e
	}
	info := p.Info()
	c.Name = validate.Text(e, "name", in.Name, 100, true, false)
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	old := Config{}
	if cur != nil {
		old = s.decrypt(info.Target, cur.Config)
		if in.Config == nil { // name or enabled flag only
			in.Config = old
		}
	}
	cfg, errs := Normalize(info.Target, mergeSecrets(info.Target, in.Config, old, in.Clear))
	for k, code := range errs {
		e.Add(k, code)
	}
	if v, ok := p.(Validator); ok {
		for k, code := range v.Validate(Target{Settings: Config{}, Config: cfg}) {
			e.Add(k, code)
		}
	}
	if c.Name != "" {
		except := int64(0)
		if cur != nil {
			except = cur.ID
		}
		taken, err := s.store.ChannelNameTaken(ctx, c.Name, except)
		if err != nil {
			return nil, nil, err
		}
		if taken {
			e.Add("name", validate.Duplicate)
		}
	}
	if err := e.Err(); err != nil {
		return nil, nil, err
	}
	c.Config = cfg
	return c, p, nil
}

func (s *Service) save(ctx context.Context, c *store.Channel, create bool, groupIDs *[]int64) error {
	p, _ := Lookup(c.Type)
	enc, err := s.encrypt(p.Info().Target, Config(c.Config))
	if err != nil {
		return err
	}
	stored := *c
	stored.Config = enc
	if create {
		err = s.store.CreateChannel(ctx, &stored)
	} else {
		err = s.store.UpdateChannel(ctx, &stored)
	}
	if errors.Is(err, store.ErrConflict) {
		return validate.Errors{"name": validate.Duplicate}
	}
	if err != nil {
		return err
	}
	c.ID = stored.ID
	if groupIDs != nil {
		if err := s.store.SetChannelGroups(ctx, c.ID, *groupIDs); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return validate.Errors{"group_ids": validate.Invalid}
			}
			return err
		}
	}
	return nil
}

// CreateChannel validates and stores a new channel.
func (s *Service) CreateChannel(ctx context.Context, in ChannelInput) (*ChannelView, error) {
	c, _, err := s.prepare(ctx, nil, in)
	if err != nil {
		return nil, err
	}
	if err := s.save(ctx, c, true, in.GroupIDs); err != nil {
		return nil, err
	}
	return s.Channel(ctx, c.ID)
}

// UpdateChannel validates and stores changes of a channel. Empty secret
// values keep the stored secrets unless listed in Clear.
func (s *Service) UpdateChannel(ctx context.Context, id int64, in ChannelInput) (*ChannelView, error) {
	cur, err := s.store.GetChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	c, _, err := s.prepare(ctx, cur, in)
	if err != nil {
		return nil, err
	}
	if err := s.save(ctx, c, false, in.GroupIDs); err != nil {
		return nil, err
	}
	return s.Channel(ctx, id)
}

// target resolves the provider and plain text configuration of a channel.
func (s *Service) target(ctx context.Context, c *store.Channel, plain bool) (Provider, Target, error) {
	p, ok := Lookup(c.Type)
	if !ok {
		return nil, Target{}, fmt.Errorf("%w %q", ErrUnknownType, c.Type)
	}
	states, err := s.states(ctx)
	if err != nil {
		return nil, Target{}, err
	}
	info := p.Info()
	cfg := Config(c.Config)
	if !plain {
		cfg = s.decrypt(info.Target, cfg)
	}
	return p, Target{Settings: s.decrypt(info.Settings, states[c.Type].Settings), Config: cfg}, nil
}

// Delivery describes the journal entry of one message.
type Delivery struct {
	Kind      string // store.MailAlert, store.MailGroupTest, store.MailTest
	GroupID   int64
	GroupName string
}

// Send delivers msg to a stored channel and journals the attempt.
func (s *Service) Send(ctx context.Context, c *store.Channel, msg *Message, d Delivery) error {
	p, t, err := s.target(ctx, c, false)
	if err != nil {
		return err
	}
	return s.deliver(ctx, p, t, c, msg, d)
}

// Test sends a test message through the channel described by in (merged
// with the stored channel id when id > 0) without saving anything.
func (s *Service) Test(ctx context.Context, id int64, in ChannelInput, msg *Message) error {
	var cur *store.Channel
	if id > 0 {
		var err error
		if cur, err = s.store.GetChannel(ctx, id); err != nil {
			return err
		}
	}
	if in.Name == "" && cur != nil {
		in.Name = cur.Name
	}
	c, _, err := s.prepare(ctx, cur, in)
	if err != nil {
		return err
	}
	p, t, err := s.target(ctx, c, true)
	if err != nil {
		return err
	}
	msg.Kind, msg.ChannelName = KindChannelTest, c.Name
	return s.deliver(ctx, p, t, c, msg, Delivery{Kind: store.MailTest})
}

func (s *Service) deliver(ctx context.Context, p Provider, t Target, c *store.Channel, msg *Message, d Delivery) error {
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	start := time.Now()
	err := p.Send(ctx, s.client, t, msg)
	e := &store.MailLogEntry{
		Channel: c.Type, Kind: d.Kind, Recipient: c.Name, Subject: msg.Title(), Status: store.MailSent,
		GroupName: d.GroupName, DurationMS: int(time.Since(start).Milliseconds()),
	}
	if sm, ok := p.(Summarizer); ok {
		e.SMTPServer = sm.Summary(t)
	}
	for _, it := range msg.Items {
		e.Certificates = append(e.Certificates, it.URL)
	}
	e.CertCount = len(e.Certificates)
	if d.GroupID > 0 {
		e.GroupID = new(d.GroupID)
	}
	if err != nil {
		e.Status, e.Error = store.MailFailed, err.Error()
	}
	logCtx := context.WithoutCancel(ctx)
	jerr := s.store.AddMailLog(logCtx, e)
	if jerr != nil && db.IsForeignKeyViolation(jerr) { // group deleted meanwhile
		e.GroupID = nil
		jerr = s.store.AddMailLog(logCtx, e)
	}
	if jerr != nil {
		s.log.Error("delivery journal", "channel", c.Name, "err", jerr)
	}
	return err
}
