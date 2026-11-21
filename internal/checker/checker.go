// Package checker connects to TLS endpoints and inspects the certificates
// they present.
package checker

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/idna"
)

// Target is a TLS endpoint.
type Target struct {
	Host string // ASCII host name (punycode) or IP literal
	Port int
}

// Address returns host:port suitable for net.Dial.
func (t Target) Address() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// DefaultPorts maps URL schemes to their implicit TLS ports.
var DefaultPorts = map[string]int{
	"https": 443, "wss": 443, "tls": 443, "ssl": 443,
	"smtps": 465, "imaps": 993, "pop3s": 995, "ldaps": 636, "ftps": 990,
}

// Errors returned by ParseTarget.
var (
	ErrEmpty         = errors.New("address is empty")
	ErrInvalidURL    = errors.New("invalid URL or host name")
	ErrInvalidPort   = errors.New("invalid port")
	ErrUnknownScheme = errors.New("unknown scheme: specify the port explicitly")
)

// ParseTarget parses user input such as "example.com", "example.com:8443",
// "https://bücher.example/path" or "ldaps://dc1.corp:636". It returns the target
// and a normalised URL for display.
func ParseTarget(raw string) (Target, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Target{}, "", ErrEmpty
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || strings.ContainsAny(u.Host, " \t") {
		return Target{}, "", ErrInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" {
		return Target{}, "", ErrInvalidURL
	}

	port := 0
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return Target{}, "", ErrInvalidPort
		}
	} else {
		var ok bool
		if port, ok = DefaultPorts[scheme]; !ok {
			return Target{}, "", ErrUnknownScheme
		}
	}

	display := host
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
		display = host
	} else {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil || ascii == "" || len(ascii) > 253 {
			return Target{}, "", ErrInvalidURL
		}
		host = strings.ToLower(ascii)
		if uni, err := idna.Display.ToUnicode(host); err == nil {
			display = uni
		}
	}

	hostPort := display
	if strings.Contains(display, ":") { // IPv6 literal
		hostPort = "[" + display + "]"
	}
	if def, ok := DefaultPorts[scheme]; !ok || def != port {
		hostPort = net.JoinHostPort(display, strconv.Itoa(port))
	}
	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	return Target{Host: host, Port: port}, scheme + "://" + hostPort + path, nil
}

// Result describes the certificate presented by an endpoint.
type Result struct {
	Leaf        *x509.Certificate
	Chain       []*x509.Certificate // as presented, leaf first
	Trusted     bool                // chain verifies against the roots and matches the host name
	VerifyError string
	TLSVersion  string
	ResolvedIP  string
	Duration    time.Duration
}

// Checker fetches certificates.
type Checker struct {
	// Timeout per connection attempt (default 10s).
	Timeout time.Duration
	// Roots used for trust verification (nil = system roots).
	Roots *x509.CertPool
	// Retries after a failed attempt (default 0).
	Retries int
	// RetryDelay between attempts (default 2s).
	RetryDelay time.Duration
}

// Check connects to the target and returns the presented certificate.
// Expired, self-signed or otherwise invalid certificates are returned
// with Trusted=false; only connection level problems produce an error.
func (c *Checker) Check(ctx context.Context, t Target) (*Result, error) {
	var lastErr error
	for attempt := range c.Retries + 1 {
		if attempt > 0 {
			delay := c.RetryDelay
			if delay <= 0 {
				delay = 2 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, lastErr
			case <-time.After(delay):
			}
		}
		res, err := c.checkOnce(ctx, t)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func (c *Checker) checkOnce(ctx context.Context, t Target) (*Result, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cfg := &tls.Config{
		// Verification is performed manually below so that details of
		// expired, self-signed or mismatched certificates can be reported.
		InsecureSkipVerify: true, //nolint:gosec // intentional, see above
		MinVersion:         tls.VersionTLS10,
		CipherSuites:       allCipherSuites(),
	}
	if net.ParseIP(t.Host) == nil {
		cfg.ServerName = t.Host
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout}, Config: cfg}

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", t.Address())
	if err != nil {
		return nil, describeError(err, timeout)
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil, errors.New("unexpected connection type")
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, errors.New("server presented no certificate")
	}

	res := &Result{
		Leaf:       state.PeerCertificates[0],
		Chain:      state.PeerCertificates,
		TLSVersion: tls.VersionName(state.Version),
		Duration:   time.Since(start),
	}
	if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		res.ResolvedIP = addr.IP.String()
	}

	intermediates := x509.NewCertPool()
	for _, ic := range state.PeerCertificates[1:] {
		intermediates.AddCert(ic)
	}
	_, verr := res.Leaf.Verify(x509.VerifyOptions{
		DNSName:       t.Host,
		Intermediates: intermediates,
		Roots:         c.Roots,
	})
	res.Trusted = verr == nil
	if verr != nil {
		res.VerifyError = verr.Error()
	}
	return res, nil
}

func allCipherSuites() []uint16 {
	var ids []uint16
	for _, cs := range tls.CipherSuites() {
		ids = append(ids, cs.ID)
	}
	for _, cs := range tls.InsecureCipherSuites() {
		ids = append(ids, cs.ID)
	}
	return ids
}

// describeError turns low level network errors into short messages.
func describeError(err error, timeout time.Duration) error {
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		if dnsErr.IsNotFound {
			return fmt.Errorf("DNS: host %s not found", dnsErr.Name)
		}
		return fmt.Errorf("DNS lookup failed: %s", dnsErr.Err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("connection timed out after %s", timeout)
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return fmt.Errorf("connection timed out after %s", timeout)
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New("connection refused")
	case errors.Is(err, syscall.ECONNRESET):
		return errors.New("connection reset by peer")
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return errors.New("host unreachable")
	}
	if re, ok := errors.AsType[tls.AlertError](err); ok {
		return fmt.Errorf("TLS handshake failed: %s", re.Error())
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return errors.New("TLS handshake failed: the endpoint does not speak TLS")
	}
	return err
}

// Fingerprint returns the SHA-256 fingerprint of a certificate (hex).
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// SerialHex formats a serial number as upper case hex pairs.
func SerialHex(c *x509.Certificate) string {
	if c.SerialNumber == nil {
		return ""
	}
	h := strings.ToUpper(c.SerialNumber.Text(16))
	if len(h)%2 == 1 {
		h = "0" + h
	}
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// KeyInfo returns the public key algorithm name and size in bits.
func KeyInfo(c *x509.Certificate) (string, int) {
	switch k := c.PublicKey.(type) {
	case *rsa.PublicKey:
		return "RSA", k.N.BitLen()
	case *ecdsa.PublicKey:
		return "ECDSA", k.Curve.Params().BitSize
	case ed25519.PublicKey:
		return "Ed25519", 256
	}
	return c.PublicKeyAlgorithm.String(), 0
}

// SANs lists DNS names, IP addresses and e-mail addresses of a certificate.
func SANs(c *x509.Certificate) []string {
	out := make([]string, 0, len(c.DNSNames)+len(c.IPAddresses))
	out = append(out, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	out = append(out, c.EmailAddresses...)
	for _, u := range c.URIs {
		out = append(out, u.String())
	}
	return out
}

// Org returns the first organisation of a name.
func Org(n pkix.Name) string {
	if len(n.Organization) > 0 {
		return n.Organization[0]
	}
	return ""
}

// CommonName returns the CN, falling back to the organisation or the full DN.
func CommonName(n pkix.Name) string {
	if n.CommonName != "" {
		return n.CommonName
	}
	if o := Org(n); o != "" {
		return o
	}
	return n.String()
}
