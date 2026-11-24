package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/master2sid2/cert-alerter/internal/notify"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/validate"
)

// ---- messenger providers ----------------------------------------------------

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Channels.Providers(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) handleSaveProvider(w http.ResponseWriter, r *http.Request) error {
	typ := r.PathValue("type")
	var in notify.ProviderInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	err := s.Channels.SaveProvider(r.Context(), typ, in)
	if errors.Is(err, notify.ErrUnknownType) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	s.audit(r, "channel_provider.updated", "channel_provider", 0, map[string]any{"type": typ, "enabled": in.Enabled})
	s.Monitor.KickAlerts()
	return s.handleListProviders(w, r)
}

// ---- channels ---------------------------------------------------------------

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Channels.Channels(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := s.Channels.Channel(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) error {
	var in notify.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Channels.CreateChannel(r.Context(), in)
	if err != nil {
		return err
	}
	s.audit(r, "channel.created", "channel", c.ID, map[string]any{"name": c.Name, "type": c.Type})
	s.Monitor.KickAlerts()
	writeJSON(w, http.StatusCreated, c)
	return nil
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in notify.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Channels.UpdateChannel(r.Context(), id, in)
	if err != nil {
		return err
	}
	s.audit(r, "channel.updated", "channel", id, map[string]any{"name": c.Name, "type": c.Type, "enabled": c.Enabled})
	s.Monitor.KickAlerts()
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := s.Store.GetChannel(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteChannel(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "channel.deleted", "channel", id, map[string]any{"name": c.Name, "type": c.Type})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// handleTestChannel sends a test message through a channel described by the
// request (an unsaved form, optionally based on a stored channel "id")
// without storing anything.
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ID int64 `json:"id"`
		notify.ChannelInput
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	msg, err := s.Alerts.TestMessage(r.Context())
	if err != nil {
		return err
	}
	start := time.Now()
	err = s.Channels.Test(r.Context(), in.ID, in.ChannelInput, msg)
	if _, ok := errors.AsType[validate.Errors](err); ok || errors.Is(err, store.ErrNotFound) {
		return err
	}
	res := map[string]any{"ok": err == nil, "duration_ms": time.Since(start).Milliseconds()}
	if err != nil {
		res["error"] = err.Error()
	}
	s.audit(r, "channel.test_sent", "channel", in.ID, map[string]any{"type": in.Type, "ok": err == nil})
	writeJSON(w, http.StatusOK, res)
	return nil
}

// ---- group channels ---------------------------------------------------------

func (s *Server) handleGroupAddChannels(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		ChannelIDs []int64 `json:"channel_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if len(in.ChannelIDs) == 0 {
		return errValidation(validate.Errors{"channel_ids": validate.Required})
	}
	ctx := r.Context()
	if _, err := s.Store.GetGroup(ctx, id); err != nil {
		return err
	}
	n, err := s.Store.AddChannelsToGroup(ctx, id, in.ChannelIDs)
	if errors.Is(err, store.ErrNotFound) {
		return errValidation(validate.Errors{"channel_ids": validate.Invalid})
	}
	if err != nil {
		return err
	}
	if n > 0 {
		s.audit(r, "group.channels_added", "group", id, map[string]any{"channel_ids": in.ChannelIDs})
		s.Monitor.KickAlerts()
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": n})
	return nil
}

func (s *Server) handleGroupRemoveChannel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	cid, err := pathID(r, "channelID")
	if err != nil {
		return err
	}
	if err := s.Store.RemoveChannelFromGroup(r.Context(), id, cid); err != nil {
		return err
	}
	s.audit(r, "group.channel_removed", "group", id, map[string]any{"channel_id": cid})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
