package py

import (
	"strings"
	"unicode/utf8"
)

// QS is the dict urllib.parse.parse_qs returns: keys in first-seen order, every value list kept.
type QS struct {
	keys []string
	m    map[string][]string
}

// NewQS builds a QS from alternating key, value pairs.
func NewQS(kv ...string) QS {
	q := QS{m: map[string][]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Add(kv[i], kv[i+1])
	}
	return q
}

// ParseQS is urllib.parse.parse_qs(s) (keep_blank_values=False, "&" only, errors="replace").
func ParseQS(s string) QS {
	q := QS{m: map[string][]string{}}
	if s == "" {
		return q
	}
	for _, part := range strings.Split(s, "&") {
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		if !ok { // no "=": skipped unless keep_blank_values
			continue
		}
		if val == "" {
			continue
		}
		q.Add(Unquote(strings.ReplaceAll(name, "+", " ")), Unquote(strings.ReplaceAll(val, "+", " ")))
	}
	return q
}

// Add appends a value.
func (q *QS) Add(k, v string) {
	if q.m == nil {
		q.m = map[string][]string{}
	}
	if _, ok := q.m[k]; !ok {
		q.keys = append(q.keys, k)
	}
	q.m[k] = append(q.m[k], v)
}

// Set replaces the values of k (keeps its position).
func (q *QS) Set(k string, vs ...string) {
	if q.m == nil {
		q.m = map[string][]string{}
	}
	if _, ok := q.m[k]; !ok {
		q.keys = append(q.keys, k)
	}
	q.m[k] = vs
}

// Get is qs.get(k, [""])[0].
func (q QS) Get(k string) string { return q.GetD(k, "") }

// GetD is qs.get(k, [def])[0].
func (q QS) GetD(k, def string) string {
	if vs, ok := q.m[k]; ok && len(vs) > 0 {
		return vs[0]
	}
	return def
}

// Has is `k in qs`.
func (q QS) Has(k string) bool { _, ok := q.m[k]; return ok }

// List is qs.get(k, []).
func (q QS) List(k string) []string { return q.m[k] }

// Keys in first-seen order.
func (q QS) Keys() []string { return q.keys }

// Len is len(qs).
func (q QS) Len() int { return len(q.keys) }

// Pairs flattens to (k, v) pairs in order, skipping the given keys.
func (q QS) Pairs(skip ...string) [][2]string {
	var out [][2]string
outer:
	for _, k := range q.keys {
		for _, s := range skip {
			if k == s {
				continue outer
			}
		}
		for _, v := range q.m[k] {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

// Urlencode is urllib.parse.urlencode(pairs) (quote_plus on keys and values).
func Urlencode(pairs [][2]string) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(QuotePlus(p[0]))
		b.WriteByte('=')
		b.WriteString(QuotePlus(p[1]))
	}
	return b.String()
}

// UrlencodeKV is urlencode({k: v, ...}) from alternating pairs.
func UrlencodeKV(kv ...string) string {
	var pairs [][2]string
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, [2]string{kv[i], kv[i+1]})
	}
	return Urlencode(pairs)
}

const hexUpper = "0123456789ABCDEF"

func unreserved(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == '~'
}

// Quote is urllib.parse.quote(s, safe) on the UTF-8 bytes; the default safe of Python is "/".
func Quote(s string, safe ...string) string {
	sf := "/"
	if len(safe) > 0 {
		sf = safe[0]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if unreserved(c) || (c < 0x80 && strings.IndexByte(sf, c) >= 0) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&15])
		}
	}
	return b.String()
}

// QuotePlus is urllib.parse.quote_plus(s): safe "", spaces as "+".
func QuotePlus(s string) string {
	if !strings.Contains(s, " ") {
		return Quote(s, "")
	}
	return strings.ReplaceAll(Quote(s, " "), " ", "+")
}

// Unquote is urllib.parse.unquote(s) (UTF-8, errors="replace").
func Unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var raw []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s)+0 && i+2 <= len(s)-1 && ishex(s[i+1]) && ishex(s[i+2]) {
			raw = append(raw, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		raw = append(raw, s[i])
	}
	return ToValidUTF8(raw)
}

// ToValidUTF8 decodes bytes as UTF-8 with errors="replace" (one U+FFFD per invalid byte, like Python).
func ToValidUTF8(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var b strings.Builder
	for len(raw) > 0 {
		r, w := utf8.DecodeRune(raw)
		if r == utf8.RuneError && w <= 1 {
			b.WriteRune(0xfffd)
			raw = raw[1:]
			continue
		}
		b.WriteRune(r)
		raw = raw[w:]
	}
	return b.String()
}

func ishex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// URL is the result of urllib.parse.urlsplit.
type URL struct {
	Scheme, Netloc, Path, Query, Fragment string
}

// URLSplit is urllib.parse.urlsplit(s) for the forms this app sees (request targets, Origin headers, links).
func URLSplit(s string) URL {
	var u URL
	// Python strips leading C0 control and space characters and removes tab/newline everywhere.
	s = strings.TrimLeft(s, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\x0c\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f ")
	s = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(s)
	if i := strings.IndexByte(s, ':'); i > 0 {
		sch := s[:i]
		ok := isAlpha(sch[0])
		for j := 0; j < len(sch) && ok; j++ {
			c := sch[j]
			ok = isAlpha(c) || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'
		}
		if ok {
			u.Scheme = strings.ToLower(sch)
			s = s[i+1:]
		}
	}
	if strings.HasPrefix(s, "//") {
		rest := s[2:]
		end := len(rest)
		for _, d := range "/?#" {
			if k := strings.IndexRune(rest, d); k >= 0 && k < end {
				end = k
			}
		}
		u.Netloc, s = rest[:end], rest[end:]
	}
	if i := strings.IndexByte(s, '#'); i >= 0 {
		u.Fragment, s = s[i+1:], s[:i]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		u.Query, s = s[i+1:], s[:i]
	}
	u.Path = s
	return u
}

// Hostname is urlsplit(...).hostname: the netloc without userinfo and port, lower-cased; "" when none.
func (u URL) Hostname() string {
	h := u.Netloc
	if i := strings.LastIndexByte(h, '@'); i >= 0 {
		h = h[i+1:]
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i >= 0 {
			return strings.ToLower(h[1:i])
		}
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
