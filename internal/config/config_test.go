package config

import (
	"flag"
	"testing"
)

// Commands such as "user" and "migrate" register only the shared flags;
// Finalize must accept that.
func TestFinalizeWithoutServeFlags(t *testing.T) {
	fs := flag.NewFlagSet("user list", flag.ContinueOnError)
	c := Register(fs)
	if err := fs.Parse([]string{"-data-dir=/var/lib/cert-alerter"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if c.DBDSN != "/var/lib/cert-alerter/cert-alerter.db" {
		t.Fatalf("DBDSN = %q", c.DBDSN)
	}
}

func TestFinalizeServeFlags(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	c := Register(fs)
	c.RegisterServe(fs)
	if err := fs.Parse([]string{"-cookie-secure=sometimes"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Finalize(); err == nil {
		t.Fatal("invalid cookie-secure accepted")
	}
}
