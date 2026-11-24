package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/master2sid2/cert-alerter/internal/version"
)

// Client performs the HTTP requests of the providers. Errors never contain
// request URLs: webhook URLs and bot API URLs carry secrets.
type Client struct {
	secure   *http.Client
	insecure *http.Client
}

// NewClient creates a client. roots replaces the system trust store when
// not nil (the -ca-file option); proxies come from the environment
// (HTTPS_PROXY, NO_PROXY).
func NewClient(roots *x509.CertPool, timeout time.Duration) *Client {
	mk := func(skipVerify bool) *http.Client {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, InsecureSkipVerify: skipVerify} //nolint:gosec // opt-in per channel
		return &http.Client{Transport: tr, Timeout: timeout}
	}
	return &Client{secure: mk(false), insecure: mk(true)}
}

// Response is a received HTTP response.
type Response struct {
	Status int
	Body   []byte // at most 64 KiB
}

// OK reports a 2xx status.
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Excerpt returns the start of the body on one line, for error messages.
func (r *Response) Excerpt() string {
	s := strings.Join(strings.Fields(string(r.Body)), " ")
	if len(s) > 200 {
		s = s[:200]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// PostJSON posts body as JSON. headers may be nil.
func (c *Client) PostJSON(ctx context.Context, rawURL string, body any, headers map[string]string, skipVerify bool) (*Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cert-alerter/"+version.String())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hc := c.secure
	if skipVerify {
		hc = c.insecure
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, sanitize(err, req.URL)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return &Response{Status: resp.StatusCode, Body: b}, nil
}

// sanitize drops the URL from transport errors, keeping only the host.
func sanitize(err error, u *url.URL) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: timeout", u.Host)
	}
	msg := err.Error()
	// Redirect errors and the like may still quote the URL.
	if s := u.String(); s != "" {
		msg = strings.ReplaceAll(msg, s, u.Host)
	}
	if strings.Contains(msg, u.Host) {
		return errors.New(msg)
	}
	return fmt.Errorf("%s: %s", u.Host, msg)
}

// HTTPError builds the error for an unexpected response.
func HTTPError(r *Response, detail string) error {
	if detail == "" {
		detail = r.Excerpt()
	}
	if detail == "" {
		return fmt.Errorf("HTTP %d", r.Status)
	}
	return fmt.Errorf("HTTP %d: %s", r.Status, detail)
}

// ValidURL reports whether s is an absolute http(s) URL.
func ValidURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

// HostOf returns the host of a URL ("" when invalid).
func HostOf(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Host
}
