// Package secret encrypts small secrets (such as the SMTP password) before
// they are written to the database.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "v1:"

// Box seals and opens values with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// NewBox creates a Box from a 32 byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secret key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt seals plaintext. Empty input yields empty output.
func (b *Box) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt opens a value produced by Encrypt.
func (b *Box) Decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(value, prefix)
	if !ok {
		return "", errors.New("unsupported secret format")
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(data) < ns {
		return "", errors.New("secret too short")
	}
	plain, err := b.aead.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", errors.New("cannot decrypt secret (was the secret key changed?)")
	}
	return string(plain), nil
}

// LoadKey derives the encryption key. A configured passphrase is hashed
// with SHA-256; otherwise a random key is read from (or created in)
// dataDir/secret.key. The returned string describes the source.
func LoadKey(passphrase, dataDir string) ([]byte, string, error) {
	if passphrase != "" {
		sum := sha256.Sum256([]byte(passphrase))
		return sum[:], "environment", nil
	}
	path := filepath.Join(dataDir, "secret.key")
	if data, err := os.ReadFile(path); err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(key) != 32 {
			return nil, "", fmt.Errorf("invalid key file %s", path)
		}
		return key, path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, "", fmt.Errorf("write key file: %w", err)
	}
	return key, path + " (generated)", nil
}
