package capability

// sealed.go: sealed inputs (ADR-0022 D4). A secret value an admin types at approval is sealed by the api to actd's
// X25519 public key (actions/actd.pub, pinned in the api's own folder): X25519 ECDH with a new ephemeral key,
// HKDF-SHA256, AES-256-GCM, additional data = action id + step + name. Only actd's private key (argus-act) opens it;
// the worker, board.db and the model see ciphertext only. Standard library only (HKDF is written out: RFC 5869).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SealedPrefix marks a sealed input.
const SealedPrefix = "argus-sealed-v1:"

const sealDomain = "argus-sealed-input-v1"

// MaxInputBytes is the longest secret input value.
const MaxInputBytes = 8192

// MaxSealedInputs is the most sealed inputs one approval carries.
const MaxSealedInputs = 20

var inputKeyRe = regexp.MustCompile(`^([1-9][0-9]{0,3})/([A-Z][A-Z0-9_]{0,31})$`)

// InputKey is the key of a sealed input in a request: "<step>/<NAME>" (one ciphertext per step that refers to it).
func InputKey(step int, name string) string { return strconv.Itoa(step) + "/" + name }

// ParseInputKey reads "<step>/<NAME>".
func ParseInputKey(k string) (int, string, bool) {
	m := inputKeyRe.FindStringSubmatch(k)
	if m == nil {
		return 0, "", false
	}
	n, _ := strconv.Atoi(m[1])
	return n, m[2], true
}

// ValidSealed reports whether s has the form of a sealed input (it does not open it).
func ValidSealed(s string) bool {
	if !strings.HasPrefix(s, SealedPrefix) || len(s) > 2*MaxInputBytes {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, SealedPrefix))
	return err == nil && len(b) >= 32+12+16
}

func sealAD(actionID, planSHA string, step int, name string) []byte {
	return []byte(strings.Join([]string{sealDomain, actionID, planSHA, strconv.Itoa(step), name}, "\n"))
}

// hkdfSHA256 is HKDF (RFC 5869) with SHA-256: extract, then expand to n bytes (n <= 32 here).
func hkdfSHA256(secret, salt, info []byte, n int) []byte {
	ex := hmac.New(sha256.New, salt)
	ex.Write(secret)
	prk := ex.Sum(nil)
	var out, t []byte
	for i := byte(1); len(out) < n; i++ {
		m := hmac.New(sha256.New, prk)
		m.Write(t)
		m.Write(info)
		m.Write([]byte{i})
		t = m.Sum(nil)
		out = append(out, t...)
	}
	return out[:n]
}

func sealAEAD(shared, ephPub, recipPub []byte) (cipher.AEAD, error) {
	salt := append(append([]byte{}, ephPub...), recipPub...)
	k := hkdfSHA256(shared, salt, []byte(sealDomain), 32)
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// SealInput seals value to actd's public key for one step of one action of one plan (its full sha256).
func SealInput(pub *ecdh.PublicKey, actionID, planSHA string, step int, name, value string) (string, error) {
	switch {
	case pub == nil:
		return "", errors.New("no executor sealing key")
	case !ValidActionID(actionID) || !hex64Re.MatchString(planSHA) || !strings.HasSuffix(actionID, "."+planSHA[:12]) || step <= 0 || !InputRefRe.MatchString("input:"+name):
		return "", errors.New("bad action, step or input name")
	case value == "" || len(value) > MaxInputBytes:
		return "", fmt.Errorf("the value is empty or longer than %d bytes", MaxInputBytes)
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return "", err
	}
	a, err := sealAEAD(shared, eph.PublicKey().Bytes(), pub.Bytes())
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(append(eph.PublicKey().Bytes(), nonce...), a.Seal(nil, nonce, []byte(value), sealAD(actionID, planSHA, step, name))...)
	return SealedPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// OpenInput opens a sealed input with actd's private key; the action, plan, step and name must be the sealed ones.
func OpenInput(priv *ecdh.PrivateKey, actionID, planSHA string, step int, name, sealed string) (string, error) {
	if priv == nil {
		return "", errors.New("no executor sealing key")
	}
	if !ValidSealed(sealed) {
		return "", errors.New("not a sealed input")
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, SealedPrefix))
	ephPub, err := ecdh.X25519().NewPublicKey(raw[:32])
	if err != nil {
		return "", errors.New("the sealed input is damaged")
	}
	shared, err := priv.ECDH(ephPub)
	if err != nil {
		return "", errors.New("the sealed input is damaged")
	}
	a, err := sealAEAD(shared, raw[:32], priv.PublicKey().Bytes())
	if err != nil {
		return "", err
	}
	plain, err := a.Open(nil, raw[32:32+a.NonceSize()], raw[32+a.NonceSize():], sealAD(actionID, planSHA, step, name))
	if err != nil {
		return "", errors.New("the sealed input does not open for this action, plan, step and name")
	}
	return string(plain), nil
}

// SealPublicPEM is the PKIX PEM of an X25519 public key.
func SealPublicPEM(p *ecdh.PublicKey) ([]byte, error) {
	b, err := x509.MarshalPKIXPublicKey(p)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}), nil
}

// ParseSealPublic reads one PKIX PEM X25519 public key.
func ParseSealPublic(b []byte) (*ecdh.PublicKey, error) {
	blk, rest := pem.Decode(b)
	if blk == nil || blk.Type != "PUBLIC KEY" || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("the sealing public key is not one PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the sealing public key: %v", err)
	}
	p, ok := k.(*ecdh.PublicKey)
	if !ok || p.Curve() != ecdh.X25519() {
		return nil, errors.New("the sealing public key is not an X25519 key")
	}
	return p, nil
}

// SealPrivatePEM is the PKCS#8 PEM of an X25519 private key.
func SealPrivatePEM(k *ecdh.PrivateKey) ([]byte, error) {
	b, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}), nil
}

// ParseSealPrivate reads a PKCS#8 PEM X25519 private key.
func ParseSealPrivate(b []byte) (*ecdh.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, errors.New("the sealing key is not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the sealing key: %v", err)
	}
	p, ok := k.(*ecdh.PrivateKey)
	if !ok || p.Curve() != ecdh.X25519() {
		return nil, errors.New("the sealing key is not an X25519 key")
	}
	return p, nil
}

// SealFingerprint is the first 16 hex characters of SHA-256 of the public key, to compare the api's pin with actd.
func SealFingerprint(p *ecdh.PublicKey) string {
	if p == nil {
		return ""
	}
	sum := sha256.Sum256(p.Bytes())
	return hex.EncodeToString(sum[:8])
}

// PlanInputs are the sealed inputs a stored plan's capability steps refer to: step number -> the names (sorted) of
// every string argument of the form input:<NAME>. The api shows one write-only field per name and seals the value
// once per step; actd checks the same set against each capability's schema and refuses any difference.
func PlanInputs(plan any) map[int][]string {
	out := map[int][]string{}
	steps, _ := plan.([]any)
	for _, x := range steps {
		s, _ := x.(map[string]any)
		if c, _ := s["capability"].(string); c == "" {
			continue
		}
		n, ok := AsInt(s["n"])
		args, _ := s["args"].(map[string]any)
		if !ok || n <= 0 {
			continue
		}
		seen := map[string]bool{}
		for _, v := range args {
			if r, ok := v.(string); ok && InputRefRe.MatchString(r) && !seen[r] {
				seen[r] = true
				out[n] = append(out[n], strings.TrimPrefix(r, "input:"))
			}
		}
		sort.Strings(out[n])
	}
	return out
}
