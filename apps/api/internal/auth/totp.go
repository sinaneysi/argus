package auth

// TOTP (RFC 6238 over RFC 4226 HOTP), compatible with Google Authenticator: SHA-1, 6 digits, 30-second steps,
// a 160-bit secret shown as unpadded base32. The secret is stored encrypted (secretbox.go); codes are checked
// within one step either side, and a step once used is never accepted again for that user (replay protection).

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strings"

	"github.com/sinaneysi/argus/internal/brand"
)

const (
	TOTP_PERIOD = 30
	TOTP_DIGITS = 6
	TOTP_WINDOW = 1 // steps accepted either side of now
	TOTP_BYTES  = 20
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// B32 is the base32 form of a secret (what the authenticator app takes when typed in).
func B32(secret []byte) string { return b32.EncodeToString(secret) }

// B32Dec reads a base32 secret, ignoring spaces, dashes, case and padding.
func B32Dec(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(s))
	return b32.DecodeString(s)
}

// HOTP is RFC 4226 with a chosen hash and number of digits.
func HOTP(secret []byte, counter uint64, digits int, h func() hash.Hash) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(h, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// HashByName maps the otpauth algorithm names to hashes (the RFC 6238 vectors use all three).
func HashByName(name string) func() hash.Hash {
	switch name {
	case "SHA256":
		return sha256.New
	case "SHA512":
		return sha512.New
	}
	return sha1.New
}

// TOTPAt is the code of a step (unix time / 30).
func TOTPAt(secret []byte, step int64) string {
	return HOTP(secret, uint64(step), TOTP_DIGITS, sha1.New)
}

// Step is the TOTP step of a unix time.
func Step(t int64) int64 { return t / TOTP_PERIOD }

// normCode keeps the digits of a typed code ("123 456" -> "123456"); "" when it is not 6 digits.
func normCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r != ' ' && r != '-' {
			return ""
		}
	}
	if b.Len() != TOTP_DIGITS {
		return ""
	}
	return b.String()
}

// MatchStep is the step in now±window whose code equals code and is newer than after, or -1. Every candidate is
// compared in constant time.
func MatchStep(secret []byte, code string, now int64, after int64) int64 {
	code = normCode(code)
	if code == "" {
		return -1
	}
	found := int64(-1)
	cur := Step(now)
	for d := int64(-TOTP_WINDOW); d <= TOTP_WINDOW; d++ {
		s := cur + d
		if hmac.Equal([]byte(TOTPAt(secret, s)), []byte(code)) && s > after && found < 0 {
			found = s
		}
	}
	return found
}

// OTPAuthURI is the otpauth:// URI the QR code carries:
// otpauth://totp/<brand.Product>:<user>?secret=...&issuer=<issuer>&algorithm=SHA1&digits=6&period=30
func OTPAuthURI(issuer, user string, secret []byte) string {
	label := url.PathEscape(brand.Product) + ":" + url.PathEscape(user)
	q := "secret=" + B32(secret) + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=" +
		fmt.Sprint(TOTP_DIGITS) + "&period=" + fmt.Sprint(TOTP_PERIOD)
	return "otpauth://totp/" + label + "?" + strings.ReplaceAll(q, "+", "%20")
}
