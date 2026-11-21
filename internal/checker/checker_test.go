package checker_test

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/master2sid2/cert-alerter/internal/checker"
	"github.com/master2sid2/cert-alerter/internal/testutil"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in      string
		host    string
		port    int
		url     string
		wantErr error
	}{
		{in: "example.com", host: "example.com", port: 443, url: "https://example.com"},
		{in: "  Example.COM.  ", host: "example.com", port: 443, url: "https://example.com"},
		{in: "example.com:8443", host: "example.com", port: 8443, url: "https://example.com:8443"},
		{in: "https://example.com/login?x=1", host: "example.com", port: 443, url: "https://example.com/login"},
		{in: "https://bücher.example/", host: "xn--bcher-kva.example", port: 443, url: "https://bücher.example"},
		{in: "ldaps://dc1.corp", host: "dc1.corp", port: 636, url: "ldaps://dc1.corp"},
		{in: "smtps://mail.example.com", host: "mail.example.com", port: 465, url: "smtps://mail.example.com"},
		{in: "imaps://127.0.0.1:19006", host: "127.0.0.1", port: 19006, url: "imaps://127.0.0.1:19006"},
		{in: "[2001:db8::1]:9443", host: "2001:db8::1", port: 9443, url: "https://[2001:db8::1]:9443"},
		{in: "https://[2001:db8::1]", host: "2001:db8::1", port: 443, url: "https://[2001:db8::1]"},
		{in: "", wantErr: checker.ErrEmpty},
		{in: "ftp://files.example.com", wantErr: checker.ErrUnknownScheme},
		{in: "example.com:70000", wantErr: checker.ErrInvalidPort},
		{in: "https://user:pw@example.com", wantErr: checker.ErrInvalidURL},
		{in: "exa mple.com", wantErr: checker.ErrInvalidURL},
		{in: "*.example.com", wantErr: checker.ErrInvalidURL},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			target, url, err := checker.ParseTarget(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if target.Host != tt.host || target.Port != tt.port || url != tt.url {
				t.Fatalf("got %s:%d %q, want %s:%d %q", target.Host, target.Port, url, tt.host, tt.port, tt.url)
			}
		})
	}
}

func TestCheckTrustedAndExpiry(t *testing.T) {
	ca := testutil.NewCA(t)
	notAfter := time.Now().Add(5*24*time.Hour + time.Hour).Truncate(time.Second)
	port := ca.Serve(t, notAfter, "svc.test")

	c := &checker.Checker{Timeout: 3 * time.Second, Roots: ca.Pool}
	res, err := c.Check(t.Context(), checker.Target{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Trusted {
		t.Fatalf("expected trusted chain, verify error: %s", res.VerifyError)
	}
	if !res.Leaf.NotAfter.Equal(notAfter) {
		t.Fatalf("NotAfter = %v, want %v", res.Leaf.NotAfter, notAfter)
	}
	if len(res.Chain) != 2 || res.TLSVersion == "" || res.ResolvedIP != "127.0.0.1" {
		t.Fatalf("unexpected result: chain=%d tls=%q ip=%q", len(res.Chain), res.TLSVersion, res.ResolvedIP)
	}
	if got := checker.SANs(res.Leaf); !strings.Contains(strings.Join(got, ","), "svc.test") {
		t.Fatalf("SANs = %v", got)
	}

	// Without the CA the same endpoint is reported, but as untrusted.
	c.Roots = nil
	res, err = c.Check(t.Context(), checker.Target{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trusted || res.VerifyError == "" {
		t.Fatal("expected untrusted result without the test CA")
	}
}

func TestCheckExpiredCertificateIsStillReported(t *testing.T) {
	ca := testutil.NewCA(t)
	notAfter := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	port := ca.Serve(t, notAfter)
	res, err := (&checker.Checker{Timeout: 3 * time.Second, Roots: ca.Pool}).
		Check(t.Context(), checker.Target{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trusted || !strings.Contains(res.VerifyError, "expired") {
		t.Fatalf("trusted=%v verify=%q", res.Trusted, res.VerifyError)
	}
	if !res.Leaf.NotAfter.Equal(notAfter) {
		t.Fatalf("NotAfter = %v", res.Leaf.NotAfter)
	}
}

func TestCheckConnectionErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens any more

	c := &checker.Checker{Timeout: 2 * time.Second}
	if _, err := c.Check(t.Context(), checker.Target{Host: "127.0.0.1", Port: port}); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want connection refused", err)
	}

	// A plain TCP server that is not TLS.
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	go func() {
		for {
			conn, err := plain.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("220 not tls here\r\n"))
			conn.Close()
		}
	}()
	_, err = c.Check(t.Context(), checker.Target{Host: "127.0.0.1", Port: plain.Addr().(*net.TCPAddr).Port})
	if err == nil {
		t.Fatal("expected an error for a non-TLS endpoint")
	}
}
