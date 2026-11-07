package auth

// Encryption at rest of the TOTP secrets: AES-256-GCM with a key read from a root-owned file mounted read-only
// into the container (TOTP_KEY_FILE, default /keys/totp.key; on the Argus host /etc/argus/totp.key). auth.db only holds
//
//	v1.<key id>.<base64url(nonce || ciphertext || tag)>
//
// with the username and the factor id (one per enrolment) as additional data, so a row copied to another user or
// another factor does not decrypt. A fresh random 96-bit nonce per encryption. The key id (8 hex of
// a SHA-256 of the key) lets a rotation keep the old key in TOTP_KEY_FILE_OLD until `argus-api mfa rekey` has
// re-encrypted every row with the new one. The key file holds 32 raw bytes, or the same as 64 hex characters or
// base64. A missing or short key turns 2FA off for enrolment and verification (fail closed: nothing decrypts).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
)

var (
	KeyFile    = envOr("TOTP_KEY_FILE", "/keys/totp.key")
	OldKeyFile = envOr("TOTP_KEY_FILE_OLD", "")
	// LegacyLabel is the AAD label of secrets written by an earlier install under another product name
	// ("<name>-totp"). Open accepts it as a fallback; Seal and `argus-api mfa rekey` always write the current label.
	LegacyLabel = envOr("TOTP_LEGACY_LABEL", "")
)

var (
	keyMu    sync.Mutex
	keyCache = map[string][]byte{}
)

// ErrNoKey is returned when the key file is missing or unusable.
var ErrNoKey = errors.New("the 2FA key file is missing or unreadable")

func parseKey(raw []byte) ([]byte, bool) {
	if len(raw) == 32 {
		return raw, true
	}
	s := strings.TrimSpace(string(raw))
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, true
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, true
		}
	}
	return nil, false
}

func loadKey(path string) ([]byte, error) {
	if path == "" {
		return nil, ErrNoKey
	}
	keyMu.Lock()
	defer keyMu.Unlock()
	if k, ok := keyCache[path]; ok {
		return k, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrNoKey
	}
	k, ok := parseKey(raw)
	if !ok {
		return nil, ErrNoKey
	}
	keyCache[path] = k
	return k, nil
}

// ResetKeys forgets the cached keys (tests, rekey).
func ResetKeys() {
	keyMu.Lock()
	keyCache = map[string][]byte{}
	keyMu.Unlock()
}

// KeyReady says whether secrets can be encrypted now.
func KeyReady() bool { _, err := loadKey(KeyFile); return err == nil }

// legacyKeyID is the key id an earlier install wrote under TOTP_LEGACY_LABEL ("" when none is set).
func legacyKeyID(k []byte) string {
	if LegacyLabel == "" {
		return ""
	}
	s := sha256.Sum256(append([]byte(LegacyLabel+"-key-id|"), k...))
	return hex.EncodeToString(s[:4])
}

func keyID(k []byte) string {
	s := sha256.Sum256(append([]byte("argus-totp-key-id|"), k...))
	return hex.EncodeToString(s[:4])
}

func gcm(k []byte) cipher.AEAD {
	b, err := aes.NewCipher(k)
	if err != nil {
		panic(err)
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		panic(err)
	}
	return a
}

// Seal encrypts a secret for one user with the current key.
func Seal(username, fid string, plain []byte) (string, error) {
	k, err := loadKey(KeyFile)
	if err != nil {
		return "", err
	}
	a := gcm(k)
	nonce := randBytes(a.NonceSize())
	ct := a.Seal(nil, nonce, plain, aad(username, fid))
	return "v1." + keyID(k) + "." + base64.RawURLEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Open decrypts what Seal wrote, with the current key or the old one (by key id).
func Open(username, fid, box string) ([]byte, error) {
	parts := strings.Split(box, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return nil, errors.New("unknown secret format")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("bad secret encoding")
	}
	for _, p := range []string{KeyFile, OldKeyFile} {
		k, err := loadKey(p)
		if err != nil || (keyID(k) != parts[1] && legacyKeyID(k) != parts[1]) {
			continue
		}
		a := gcm(k)
		if len(raw) < a.NonceSize()+a.Overhead() {
			return nil, errors.New("short secret")
		}
		plain, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], aad(username, fid))
		if err != nil && LegacyLabel != "" {
			plain, err = a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(LegacyLabel+"|"+username+"|"+fid))
		}
		if err != nil {
			return nil, errors.New("secret does not decrypt")
		}
		return plain, nil
	}
	return nil, ErrNoKey
}

func aad(username, fid string) []byte { return []byte("argus-totp|" + username + "|" + fid) }

// NeedsRekey says whether a box was sealed with a key other than the current one.
func NeedsRekey(box string) bool {
	k, err := loadKey(KeyFile)
	if err != nil {
		return false
	}
	parts := strings.Split(box, ".")
	return len(parts) == 3 && parts[1] != keyID(k)
}
