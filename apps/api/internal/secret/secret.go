// Package secret encrypts the secrets kept in the one-volume layout (ADR-0021 D4): data-source credentials, the Claude
// token and the Matrix token. Each value is sealed with AES-256-GCM under the master key, bound to its name (the
// additional data), and stored as "argus-enc:v1:<base64 nonce|ciphertext>". A value without the prefix is plaintext
// (the legacy layout, or a volume written before encryption); `argus secrets rekey` seals those too.
//
// The master key is ARGUS_MASTER_KEY (32 bytes, base64), else the file ARGUS_MASTER_KEY_FILE (the same text; default
// keys/master.key in the volume). With neither, the first start creates the key file (0600) when the volume holds no
// secret yet, and refuses to start when it does: a new key could not read them.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prefix marks a sealed value.
const Prefix = "argus-enc:v1:"

// Key is a 32-byte master key; nil means "no encryption" (the legacy layout).
type Key []byte

// ParseKey reads a base64 (standard or URL, padded or not) 32-byte key.
func ParseKey(text string) (Key, error) {
	text = strings.TrimSpace(text)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(text); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("the master key must be 32 bytes, got %d", len(b))
			}
			return Key(b), nil
		}
	}
	return nil, errors.New("the master key is not base64")
}

// NewKeyText is a fresh key as base64 text.
func NewKeyText() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func (k Key) aead() (cipher.AEAD, error) {
	if len(k) != 32 {
		return nil, errors.New("no master key")
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Sealed says whether a stored value is sealed.
func Sealed(v string) bool { return strings.HasPrefix(strings.TrimSpace(v), Prefix) }

// Seal encrypts plain under name. With a nil key it returns plain unchanged (legacy layout).
func (k Key) Seal(name, plain string) (string, error) {
	if k == nil {
		return plain, nil
	}
	a, err := k.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := a.Seal(nonce, nonce, []byte(plain), []byte(name))
	return Prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a stored value; a value without the prefix is returned as it is (plaintext).
func (k Key) Open(name, stored string) (string, error) {
	stored = strings.TrimSpace(stored)
	if !Sealed(stored) {
		return stored, nil
	}
	a, err := k.aead()
	if err != nil {
		return "", errors.New("the value is encrypted and no master key is set")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil || len(raw) < a.NonceSize() {
		return "", errors.New("the encrypted value is damaged")
	}
	plain, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(name))
	if err != nil {
		return "", errors.New("the encrypted value does not open with this master key")
	}
	return string(plain), nil
}

// Source is where the master key comes from.
type Source struct {
	Env      string // ARGUS_MASTER_KEY
	File     string // ARGUS_MASTER_KEY_FILE
	Secrets  func() []string
	Generate bool // create File when nothing is set and no secret exists
}

// Load returns the master key. It creates the key file at the first start (Generate, no secret yet) and refuses when
// secrets exist without a key.
func Load(s Source) (Key, string, error) {
	if strings.TrimSpace(s.Env) != "" {
		k, err := ParseKey(s.Env)
		if err != nil {
			return nil, "", fmt.Errorf("ARGUS_MASTER_KEY: %w", err)
		}
		return k, "ARGUS_MASTER_KEY", nil
	}
	if s.File != "" {
		b, err := os.ReadFile(s.File)
		if err == nil {
			k, err := ParseKey(string(b))
			if err != nil {
				return nil, "", fmt.Errorf("%s: %w", s.File, err)
			}
			return k, s.File, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("master key file: %w", err)
		}
	}
	var have []string
	if s.Secrets != nil {
		have = s.Secrets()
	}
	if len(have) > 0 {
		return nil, "", fmt.Errorf("the volume holds %d secret(s) (%s) but no master key is set: set ARGUS_MASTER_KEY or ARGUS_MASTER_KEY_FILE to the key they were written with", len(have), strings.Join(first(have, 3), ", "))
	}
	if !s.Generate || s.File == "" {
		return nil, "", errors.New("no master key: set ARGUS_MASTER_KEY or ARGUS_MASTER_KEY_FILE")
	}
	if err := os.MkdirAll(filepath.Dir(s.File), 0o700); err != nil {
		return nil, "", err
	}
	text := NewKeyText()
	f, err := os.OpenFile(s.File, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) { // another process created it first
		return Load(Source{File: s.File})
	}
	if err != nil {
		return nil, "", err
	}
	_, err = f.WriteString(text + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(s.File)
		return nil, "", err
	}
	k, _ := ParseKey(text)
	return k, s.File + " (created)", nil
}

func first(xs []string, n int) []string {
	if len(xs) > n {
		return append(xs[:n:n], "...")
	}
	return xs
}

// WriteFile writes data atomically with mode 0600.
func WriteFile(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
