// Package testutil provides helpers shared by tests: databases, TLS test
// servers and a fake mail sender.
package testutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/settings"
)

// MySQLEnv names the variable with a MySQL DSN (without database name, e.g.
// "root:secret@tcp(127.0.0.1:3306)/"). When set, store and API tests also
// run against MySQL; each test gets its own temporary database.
const MySQLEnv = "CERT_ALERTER_TEST_MYSQL_DSN"

// SQLite opens a migrated SQLite database in a temporary directory.
func SQLite(t testing.TB) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

// MySQL opens a fresh migrated MySQL database, or skips the test when
// MySQLEnv is not set.
func MySQL(t testing.TB) *db.DB {
	t.Helper()
	base := os.Getenv(MySQLEnv)
	if base == "" {
		t.Skipf("%s not set", MySQLEnv)
	}
	cfg, err := mysql.ParseDSN(base)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	name := "ca_test_" + hex.EncodeToString(buf)
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a, err := sql.Open("mysql", cfg.FormatDSN())
		if err == nil {
			_, _ = a.Exec("DROP DATABASE " + name)
			a.Close()
		}
	})
	cfg.DBName = name
	d, err := db.Open(context.Background(), "mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

// ForEachDB runs fn against SQLite and, when configured, MySQL.
func ForEachDB(t *testing.T, fn func(t *testing.T, d *db.DB)) {
	t.Run("sqlite", func(t *testing.T) { fn(t, SQLite(t)) })
	t.Run("mysql", func(t *testing.T) { fn(t, MySQL(t)) })
}

// CA is a throw-away certificate authority for TLS tests.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	Pool *x509.CertPool
}

// NewCA creates a self-signed CA.
func NewCA(t testing.TB) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(5, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{Cert: cert, Key: key, Pool: pool}
}

// Serve starts a TLS listener on 127.0.0.1 presenting a certificate for
// names that expires at notAfter. It returns the port.
func (ca *CA) Serve(t testing.TB, notAfter time.Time, names ...string) int {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	cn := "localhost"
	if len(names) > 0 {
		cn = names[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		DNSNames: append([]string{"localhost"}, names...), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: notAfter.AddDate(0, 0, -90), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// FakeSender records messages instead of sending them.
type FakeSender struct {
	mu   sync.Mutex
	Sent []*mailer.Message
	// Fail decides per message whether delivery fails.
	Fail func(m *mailer.Message) error
}

// Send implements mailer.Sender.
func (f *FakeSender) Send(_ context.Context, _ settings.SMTP, msgs []*mailer.Message) []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	errs := make([]error, len(msgs))
	for i, m := range msgs {
		if f.Fail != nil {
			if err := f.Fail(m); err != nil {
				errs[i] = err
				continue
			}
		}
		f.Sent = append(f.Sent, m)
	}
	return errs
}

// Messages returns a copy of the delivered messages.
func (f *FakeSender) Messages() []*mailer.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*mailer.Message(nil), f.Sent...)
}

// Reset forgets delivered messages.
func (f *FakeSender) Reset() {
	f.mu.Lock()
	f.Sent = nil
	f.mu.Unlock()
}

// To returns the messages delivered to addr.
func (f *FakeSender) To(addr string) []*mailer.Message {
	var out []*mailer.Message
	for _, m := range f.Messages() {
		if m.To == addr {
			out = append(out, m)
		}
	}
	return out
}

// Describe is a debugging helper.
func (f *FakeSender) Describe() string {
	var b strings.Builder
	for _, m := range f.Messages() {
		fmt.Fprintf(&b, "%s: %s %v\n", m.To, m.Subject, m.Certificates)
	}
	return b.String()
}
