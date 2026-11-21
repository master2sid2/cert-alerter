package secret_test

import (
	"bytes"
	"testing"

	"github.com/master2sid2/cert-alerter/internal/secret"
)

func TestBoxRoundTripAndKeyFile(t *testing.T) {
	dir := t.TempDir()
	key, src, err := secret.LoadKey("", dir)
	if err != nil || len(key) != 32 {
		t.Fatalf("generate key: %v %d", err, len(key))
	}
	again, _, err := secret.LoadKey("", dir)
	if err != nil || !bytes.Equal(key, again) {
		t.Fatalf("key file not reused (%s): %v", src, err)
	}
	box, _ := secret.NewBox(key)
	enc, err := box.Encrypt("smtp-password")
	if err != nil || enc == "smtp-password" {
		t.Fatalf("encrypt: %q %v", enc, err)
	}
	if dec, err := box.Decrypt(enc); err != nil || dec != "smtp-password" {
		t.Fatalf("decrypt: %q %v", dec, err)
	}
	other, _, _ := secret.LoadKey("another passphrase", dir)
	box2, _ := secret.NewBox(other)
	if _, err := box2.Decrypt(enc); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
	if v, _ := box.Encrypt(""); v != "" {
		t.Fatal("empty secrets stay empty")
	}
}
