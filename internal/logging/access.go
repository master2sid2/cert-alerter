package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Access log formats.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// AccessEntry describes one served HTTP request.
type AccessEntry struct {
	Time      time.Time // when the request started
	RemoteIP  string
	Method    string
	URI       string // path and query, secrets already redacted
	Proto     string
	Status    int
	Bytes     int64
	Referer   string
	UserAgent string
}

// AccessLog writes access log lines. A nil *AccessLog discards entries.
type AccessLog struct {
	w      io.Writer
	format string
	pool   sync.Pool
}

// NewAccessLog creates an access log writing to w in the given format
// (text or json).
func NewAccessLog(w io.Writer, format string) (*AccessLog, error) {
	f := strings.ToLower(strings.TrimSpace(format))
	switch f {
	case "":
		f = FormatText
	case FormatText, FormatJSON:
	default:
		return nil, fmt.Errorf("invalid access log format %q (want text or json)", format)
	}
	return &AccessLog{w: w, format: f, pool: sync.Pool{New: func() any { return new(bytes.Buffer) }}}, nil
}

// Log writes one entry. Write errors are ignored: logging must never fail a
// request.
func (l *AccessLog) Log(e *AccessEntry) {
	if l == nil {
		return
	}
	buf := l.pool.Get().(*bytes.Buffer)
	buf.Reset()
	defer l.pool.Put(buf)
	if l.format == FormatJSON {
		writeJSON(buf, e)
	} else {
		writeText(buf, e)
	}
	_, _ = l.w.Write(buf.Bytes())
}

// writeText renders one line:
//
//	ip [21/Nov/2026:13:55:36 +0000] "GET /path HTTP/1.1" 200 2326 "referer" "user-agent"
//
// Missing values are written as "-".
func writeText(b *bytes.Buffer, e *AccessEntry) {
	b.WriteString(dash(e.RemoteIP))
	b.WriteString(" [")
	b.WriteString(e.Time.Format("02/Jan/2006:15:04:05 -0700"))
	b.WriteString(`] "`)
	b.WriteString(escape(e.Method))
	b.WriteByte(' ')
	b.WriteString(escape(e.URI))
	b.WriteByte(' ')
	b.WriteString(escape(e.Proto))
	b.WriteString(`" `)
	b.WriteString(strconv.Itoa(e.Status))
	b.WriteByte(' ')
	b.WriteString(strconv.FormatInt(e.Bytes, 10))
	b.WriteString(` "`)
	b.WriteString(dash(escape(e.Referer)))
	b.WriteString(`" "`)
	b.WriteString(dash(escape(e.UserAgent)))
	b.WriteString("\"\n")
}

type jsonEntry struct {
	Time      string `json:"time"`
	RemoteIP  string `json:"remote_ip"`
	Method    string `json:"method"`
	URI       string `json:"uri"`
	Proto     string `json:"proto"`
	Status    int    `json:"status"`
	Bytes     int64  `json:"bytes"`
	Referer   string `json:"referer"`
	UserAgent string `json:"user_agent"`
}

func writeJSON(b *bytes.Buffer, e *AccessEntry) {
	_ = json.NewEncoder(b).Encode(jsonEntry{
		Time: e.Time.Format(time.RFC3339), RemoteIP: e.RemoteIP, Method: e.Method, URI: e.URI, Proto: e.Proto,
		Status: e.Status, Bytes: e.Bytes, Referer: e.Referer, UserAgent: e.UserAgent,
	})
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// escape makes a client supplied value safe for a quoted field: quotes,
// backslashes and control characters are escaped so that a request cannot
// forge log lines.
func escape(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '"' || c == '\\' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
