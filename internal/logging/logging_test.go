package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func entry() *AccessEntry {
	return &AccessEntry{
		Time:     time.Date(2026, 10, 10, 13, 55, 36, 0, time.FixedZone("", 3*3600)),
		RemoteIP: "10.0.0.7", Method: "GET", URI: "/api/me?x=1", Proto: "HTTP/1.1",
		Status: 200, Bytes: 2326, Referer: "", UserAgent: "curl/8.5",
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewAccessLog(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	l.Log(entry())
	want := `10.0.0.7 [10/Oct/2026:13:55:36 +0300] "GET /api/me?x=1 HTTP/1.1" 200 2326 "-" "curl/8.5"` + "\n"
	if buf.String() != want {
		t.Fatalf("got  %q\nwant %q", buf.String(), want)
	}
}

func TestTextEscapesClientValues(t *testing.T) {
	var buf bytes.Buffer
	l, _ := NewAccessLog(&buf, FormatText)
	e := entry()
	e.UserAgent = "evil\"\n10.0.0.1 - - [x] \"GET / HTTP/1.1\" 200"
	e.Bytes = 0
	l.Log(e)
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("log line split by client input: %q", out)
	}
	if !strings.Contains(out, `"evil\"\x0a10.0.0.1`) || !strings.Contains(out, `" 200 0 "-" "`) {
		t.Fatalf("unexpected line %q", out)
	}
}

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewAccessLog(&buf, "JSON")
	if err != nil {
		t.Fatal(err)
	}
	l.Log(entry())
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"time": "2026-10-10T13:55:36+03:00", "remote_ip": "10.0.0.7", "method": "GET",
		"uri": "/api/me?x=1", "proto": "HTTP/1.1", "status": 200.0, "bytes": 2326.0, "referer": "", "user_agent": "curl/8.5"}
	if len(got) != len(want) {
		t.Fatalf("fields: got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestInvalidOptions(t *testing.T) {
	if _, err := NewAccessLog(&bytes.Buffer{}, "combined"); err == nil {
		t.Fatal("expected error for unknown access log format")
	}
	if _, err := NewAppLogger(&bytes.Buffer{}, "loud", "text"); err == nil {
		t.Fatal("expected error for unknown level")
	}
	if _, err := NewAppLogger(&bytes.Buffer{}, "info", "xml"); err == nil {
		t.Fatal("expected error for unknown format")
	}
	var nilLog *AccessLog
	nilLog.Log(entry()) // must not panic
}

func TestOutputsFilesSharedAndReopened(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "app.log")
	outs := NewOutputs()
	defer outs.Close()

	app, err := outs.Open(path, Stderr)
	if err != nil {
		t.Fatal(err)
	}
	access, err := outs.Open(filepath.Join(dir, "logs", ".", "app.log"), Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if app != access || !app.IsFile() {
		t.Fatal("the same file must share one output")
	}
	log, err := NewAppLogger(app, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("first")

	// Simulate logrotate: move the file away, then reopen.
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	log.Info("second") // still goes to the moved file
	if err := outs.Reopen(); err != nil {
		t.Fatal(err)
	}
	log.Info("third")

	old, _ := os.ReadFile(rotated)
	cur, _ := os.ReadFile(path)
	if !strings.Contains(string(old), "first") || !strings.Contains(string(old), "second") || strings.Contains(string(old), "third") {
		t.Fatalf("rotated file: %q", old)
	}
	if !strings.Contains(string(cur), "third") || strings.Contains(string(cur), "first") {
		t.Fatalf("new file: %q", cur)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Fatalf("log file mode %v", fi.Mode().Perm())
	}
}

func TestOutputsStdAndOff(t *testing.T) {
	outs := NewOutputs()
	for dest, want := range map[string]string{"": Stderr, "stdout": Stdout, "-": Stdout, "STDERR": Stderr, "off": Off, "none": Off} {
		o, err := outs.Open(dest, Stderr)
		if err != nil {
			t.Fatal(err)
		}
		if o.Name() != want || o.IsFile() {
			t.Fatalf("Open(%q) = %s", dest, o.Name())
		}
		if o.Enabled() != (want != Off) {
			t.Fatalf("Open(%q).Enabled() = %v", dest, o.Enabled())
		}
	}
	off, _ := outs.Open("off", Stderr)
	if n, err := off.Write([]byte("x")); n != 1 || err != nil {
		t.Fatal("off output must discard writes successfully")
	}
}
