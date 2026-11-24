package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/testutil"
)

func TestChannelsAPI(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		var (
			mu   sync.Mutex
			hits []map[string]any
			auth []string
		)
		hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &body)
			mu.Lock()
			hits = append(hits, body)
			auth = append(auth, r.Header.Get("Authorization"))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}))
		defer hook.Close()

		e := newEnv(t, d)
		if code := e.client().do("GET", "/api/channels", nil, nil); code != 401 {
			t.Fatalf("anonymous list: %d", code)
		}
		admin := e.setupAdmin()

		var providers []struct {
			Type       string `json:"type"`
			Enabled    bool   `json:"enabled"`
			Configured bool   `json:"configured"`
		}
		if code := admin.do("GET", "/api/channel-providers", nil, &providers); code != 200 || len(providers) != 3 {
			t.Fatalf("providers: %d %+v", code, providers)
		}
		for _, p := range providers {
			if !p.Enabled || p.Configured != (p.Type != "telegram") {
				t.Errorf("provider %+v", p)
			}
		}
		var er apiErr
		if code := admin.do("PUT", "/api/channel-providers/telegram", map[string]any{"enabled": true, "settings": map[string]string{"bot_token": "nope"}}, &er); code != 422 || er.Error.Fields["bot_token"] != "invalid" {
			t.Fatalf("bad bot token: %d %+v", code, er)
		}

		var g struct{ ID int64 }
		if code := admin.do("POST", "/api/groups", map[string]string{"name": "Prod"}, &g); code != 201 {
			t.Fatalf("create group: %d", code)
		}

		// Validation: required and malformed fields.
		if code := admin.do("POST", "/api/channels", map[string]any{"type": "webhook", "name": "Hook", "config": map[string]string{"url": "ftp://x"}}, &er); code != 422 || er.Error.Fields["url"] != "invalid" {
			t.Fatalf("bad url: %d %+v", code, er)
		}
		if code := admin.do("POST", "/api/channels", map[string]any{"type": "carrier-pigeon", "name": "x"}, &er); code != 422 || er.Error.Fields["type"] != "invalid" {
			t.Fatalf("bad type: %d %+v", code, er)
		}

		// Test an unsaved form, then create the channel attached to the group.
		form := map[string]any{"type": "webhook", "name": "Automation",
			"config": map[string]string{"url": hook.URL + "/hook?token=abc", "authorization": "Bearer s3cret"}}
		var tr struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if code := admin.do("POST", "/api/channels/test", form, &tr); code != 200 || !tr.OK {
			t.Fatalf("test: %d %+v", code, tr)
		}
		form["group_ids"] = []int64{g.ID}
		var ch struct {
			ID         int64             `json:"id"`
			Config     map[string]string `json:"config"`
			SecretsSet []string          `json:"secrets_set"`
			Summary    string            `json:"summary"`
			Available  bool              `json:"available"`
			GroupIDs   []int64           `json:"group_ids"`
		}
		if code := admin.do("POST", "/api/channels", form, &ch); code != 201 {
			t.Fatalf("create: %d", code)
		}
		if ch.Config["url"] != "" || ch.Config["authorization"] != "" || len(ch.SecretsSet) != 2 || !ch.Available ||
			len(ch.GroupIDs) != 1 || ch.Summary != strings.TrimPrefix(hook.URL, "http://") {
			t.Fatalf("channel view leaks or lacks data: %+v", ch)
		}
		if code := admin.do("POST", "/api/channels", form, &er); code != 422 || er.Error.Fields["name"] != "duplicate" {
			t.Fatalf("duplicate name: %d %+v", code, er)
		}

		// A stored channel is tested without resending its secrets.
		if code := admin.do("POST", "/api/channels/test", map[string]any{"id": ch.ID}, &tr); code != 200 || !tr.OK {
			t.Fatalf("test stored: %d %+v", code, tr)
		}
		mu.Lock()
		if len(hits) != 2 || auth[1] != "Bearer s3cret" || hits[1]["event"] != "test" {
			t.Fatalf("webhook calls: %v %v", hits, auth)
		}
		mu.Unlock()

		// The group shows its channel; the group test reaches it.
		var detail struct {
			Channels []struct {
				Name string `json:"name"`
			} `json:"channels"`
		}
		if code := admin.do("GET", "/api/groups/"+itoa(g.ID), nil, &detail); code != 200 || len(detail.Channels) != 1 || detail.Channels[0].Name != "Automation" {
			t.Fatalf("group detail: %d %+v", code, detail)
		}
		var gt struct {
			Results []struct {
				Channel, Target, Error string
			} `json:"results"`
		}
		if code := admin.do("POST", "/api/groups/"+itoa(g.ID)+"/test", nil, &gt); code != 200 || len(gt.Results) != 1 ||
			gt.Results[0].Channel != "webhook" || gt.Results[0].Error != "" {
			t.Fatalf("group test: %d %+v", code, gt)
		}

		// Detach and attach again through the group endpoints.
		if code := admin.do("DELETE", "/api/groups/"+itoa(g.ID)+"/channels/"+itoa(ch.ID), nil, nil); code != 204 {
			t.Fatalf("detach: %d", code)
		}
		if code := admin.do("POST", "/api/groups/"+itoa(g.ID)+"/channels", map[string]any{"channel_ids": []int64{ch.ID}}, nil); code != 200 {
			t.Fatalf("attach: %d", code)
		}

		// Deliveries are journaled with their channel type.
		var log struct {
			Items []struct {
				Channel   string `json:"channel"`
				Recipient string `json:"recipient"`
			} `json:"items"`
		}
		if code := admin.do("GET", "/api/mail-log?channel=webhook", nil, &log); code != 200 || len(log.Items) != 3 ||
			log.Items[0].Channel != "webhook" || log.Items[0].Recipient != "Automation" {
			t.Fatalf("journal: %d %+v", code, log)
		}

		// Turning the messenger off makes the channel unavailable.
		if code := admin.do("PUT", "/api/channel-providers/webhook", map[string]any{"enabled": false}, nil); code != 200 {
			t.Fatalf("disable provider: %d", code)
		}
		if code := admin.do("GET", "/api/channels/"+itoa(ch.ID), nil, &ch); code != 200 || ch.Available {
			t.Fatalf("channel still available: %+v", ch)
		}
		if code := admin.do("DELETE", "/api/channels/"+itoa(ch.ID), nil, nil); code != 204 {
			t.Fatalf("delete: %d", code)
		}
	})
}
