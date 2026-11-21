package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/testutil"
)

type accessLine struct {
	RemoteIP  string `json:"remote_ip"`
	Method    string `json:"method"`
	URI       string `json:"uri"`
	Proto     string `json:"proto"`
	Status    int    `json:"status"`
	Bytes     int64  `json:"bytes"`
	Referer   string `json:"referer"`
	UserAgent string `json:"user_agent"`
}

func (e *env) accessLines() []accessLine {
	e.t.Helper()
	var out []accessLine
	for line := range strings.Lines(e.access.String()) {
		var l accessLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			e.t.Fatalf("access log line %q: %v", line, err)
		}
		out = append(out, l)
	}
	return out
}

func (e *env) findAccess(method, uri string) accessLine {
	e.t.Helper()
	for _, l := range e.accessLines() {
		if l.Method == method && l.URI == uri {
			return l
		}
	}
	e.t.Fatalf("no access log entry for %s %s in:\n%s", method, uri, e.access.String())
	return accessLine{}
}

func TestAccessLog(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		e := newEnv(t, d)
		admin := e.setupAdmin()

		req, _ := http.NewRequest("GET", e.url+"/api/me?x=1", nil)
		req.Header.Set("Referer", "https://certs.example.com/dashboard")
		req.Header.Set("User-Agent", "test-agent/1.0")
		res, err := admin.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		me := e.findAccess("GET", "/api/me?x=1")
		if me.Status != 200 || me.Bytes == 0 || me.RemoteIP != "127.0.0.1" || me.Proto != "HTTP/1.1" ||
			me.Referer != "https://certs.example.com/dashboard" || me.UserAgent != "test-agent/1.0" {
			t.Fatalf("entry for /api/me: %+v", me)
		}

		anon := e.client()
		anon.do("GET", "/api/certificates", nil, nil)
		if l := e.findAccess("GET", "/api/certificates"); l.Status != 401 {
			t.Fatalf("anonymous entry: %+v", l)
		}

		// Tokens never reach the log.
		anon.do("GET", "/api/auth/token/SECRET-TOKEN-1", nil, nil)
		anon.do("GET", "/invite/SECRET-TOKEN-2", nil, nil)
		anon.do("GET", "/setup?token=SECRET-TOKEN-3&lang=en", nil, nil)
		if strings.Contains(e.access.String(), "SECRET-TOKEN") {
			t.Fatalf("token leaked into the access log:\n%s", e.access.String())
		}
		e.findAccess("GET", "/api/auth/token/REDACTED")
		e.findAccess("GET", "/invite/REDACTED")
		e.findAccess("GET", "/setup?lang=en&token=REDACTED")

	})
}
