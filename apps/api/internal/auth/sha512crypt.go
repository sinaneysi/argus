package auth

import (
	"crypto/sha512"
	"strconv"
	"strings"
)

// SHA512Crypt is crypt(3) with a "$6$" setting (Ulrich Drepper's SHA-crypt, the glibc algorithm Python's
// crypt.crypt used for the imported htpasswd hashes). setting is "$6$[rounds=N$]salt[$hash]"; the result is
// the full hash string. ok=false for a malformed setting.
func SHA512Crypt(pw, setting string) (string, bool) {
	if !strings.HasPrefix(setting, "$6$") {
		return "", false
	}
	rest := setting[3:]
	rounds, custom := 5000, false
	if strings.HasPrefix(rest, "rounds=") {
		end := strings.IndexByte(rest, '$')
		if end < 0 {
			return "", false
		}
		n, err := strconv.ParseUint(rest[7:end], 10, 64)
		if err != nil {
			return "", false
		}
		if n < 1000 {
			n = 1000
		}
		if n > 999999999 {
			n = 999999999
		}
		rounds, custom, rest = int(n), true, rest[end+1:]
	}
	salt := rest
	if i := strings.IndexByte(salt, '$'); i >= 0 {
		salt = salt[:i]
	}
	if len(salt) > 16 {
		salt = salt[:16]
	}
	if rounds > 50000 { // a crafted hash must not cost the server minutes; real htpasswd hashes use 5000
		return "", false
	}
	key, s := []byte(pw), []byte(salt)

	b := sha512.New()
	b.Write(key)
	b.Write(s)
	b.Write(key)
	bsum := b.Sum(nil)

	a := sha512.New()
	a.Write(key)
	a.Write(s)
	for i := len(key); i > 0; i -= 64 {
		if i > 64 {
			a.Write(bsum)
		} else {
			a.Write(bsum[:i])
		}
	}
	for i := len(key); i > 0; i >>= 1 {
		if i&1 != 0 {
			a.Write(bsum)
		} else {
			a.Write(key)
		}
	}
	asum := a.Sum(nil)

	dp := sha512.New()
	for i := 0; i < len(key); i++ {
		dp.Write(key)
	}
	dpsum := dp.Sum(nil)
	p := make([]byte, 0, len(key))
	for i := len(key); i > 0; i -= 64 {
		if i > 64 {
			p = append(p, dpsum...)
		} else {
			p = append(p, dpsum[:i]...)
		}
	}

	ds := sha512.New()
	for i := 0; i < 16+int(asum[0]); i++ {
		ds.Write(s)
	}
	dssum := ds.Sum(nil)
	sb := make([]byte, 0, len(s))
	for i := len(s); i > 0; i -= 64 {
		if i > 64 {
			sb = append(sb, dssum...)
		} else {
			sb = append(sb, dssum[:i]...)
		}
	}

	c := asum
	for i := 0; i < rounds; i++ {
		h := sha512.New()
		if i&1 != 0 {
			h.Write(p)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(sb)
		}
		if i%7 != 0 {
			h.Write(p)
		}
		if i&1 != 0 {
			h.Write(c)
		} else {
			h.Write(p)
		}
		c = h.Sum(nil)
	}

	const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var out strings.Builder
	enc := func(b2, b1, b0 byte, n int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for ; n > 0; n-- {
			out.WriteByte(itoa64[w&0x3f])
			w >>= 6
		}
	}
	order := [][3]int{{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35},
		{15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41}}
	for _, o := range order {
		enc(c[o[0]], c[o[1]], c[o[2]], 4)
	}
	enc(0, 0, c[63], 2)

	prefix := "$6$"
	if custom {
		prefix += "rounds=" + strconv.Itoa(rounds) + "$"
	}
	return prefix + salt + "$" + out.String(), true
}
