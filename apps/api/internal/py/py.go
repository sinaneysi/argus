// Package py reproduces the few Python behaviours the argus-api pages depend on, byte for byte:
// html.escape, str() of numbers and None, % / // on negatives, str slicing by code point, splitlines,
// urllib.parse quoting and parse_qs, and the float formats the pages use. The Go app must render the same
// HTML as the Python app it replaces (tests/parity compares them), so these helpers are the only place
// where the two languages' defaults differ.
package py

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// E is Python's html.escape(s, quote=True): & < > " ' become &amp; &lt; &gt; &quot; &#x27;.
func E(s string) string {
	if !strings.ContainsAny(s, "&<>\"'") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#x27;")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// EV is E(Str(v)).
func EV(v any) string { return E(Str(v)) }

// Str is Python's str() of a value from SQLite or JSON: None, True/False, int, float repr, str.
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case []byte:
		return string(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return FloatRepr(x)
	case *OMap:
		return x.PyRepr()
	case []any:
		return ListRepr(x)
	}
	return ""
}

// FloatRepr is Python's repr(float): shortest round-trip digits, fixed notation for 1e-4 <= |x| < 1e16
// (always with a decimal point), else d.ddde+XX.
func FloatRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, expS, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expS)
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	sign := ""
	if neg {
		sign = "-"
	}
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es = "-"
			exp = -exp
		}
		e := strconv.Itoa(exp)
		if len(e) < 2 {
			e = "0" + e
		}
		return sign + m + "e" + es + e
	}
	if exp < 0 {
		return sign + "0." + strings.Repeat("0", -exp-1) + digits
	}
	if len(digits) <= exp+1 {
		return sign + digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	}
	return sign + digits[:exp+1] + "." + digits[exp+1:]
}

// F is Python's f"{x:.Nf}".
func F(x float64, n int) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	return strconv.FormatFloat(x, 'f', n, 64)
}

// G is Python's f"{x:.Ng}" (general format, trailing zeros removed, exponent at least two digits).
func G(x float64, p int) string {
	if p == 0 {
		p = 1
	}
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return FloatRepr(x)
	}
	if x == 0 {
		if math.Signbit(x) {
			return "-0"
		}
		return "0"
	}
	s := strconv.FormatFloat(x, 'e', p-1, 64)
	mant, expS, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expS)
	if exp >= -4 && exp < p {
		out := strconv.FormatFloat(x, 'f', p-1-exp, 64)
		if strings.Contains(out, ".") {
			out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
		}
		return out
	}
	if strings.Contains(mant, ".") {
		mant = strings.TrimRight(strings.TrimRight(mant, "0"), ".")
	}
	es := "+"
	if exp < 0 {
		es, exp = "-", -exp
	}
	e := strconv.Itoa(exp)
	if len(e) < 2 {
		e = "0" + e
	}
	return mant + "e" + es + e
}

// Exp is Python's f"{x:.Ne}".
func Exp(x float64, n int) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	return strconv.FormatFloat(x, 'e', n, 64) // Go writes at least two exponent digits, like Python
}

// Comma is Python's f"{n:,}" for an integer.
func Comma(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// FloorDiv is Python's a // b on integers.
func FloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Mod is Python's a % b on integers (the sign of b).
func Mod(a, b int64) int64 {
	m := a % b
	if m != 0 && ((m < 0) != (b < 0)) {
		m += b
	}
	return m
}

// Truthy is Python's bool(v) for SQLite/JSON values.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []byte:
		return len(x) > 0
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case *OMap:
		return x != nil && x.Len() > 0
	case []any:
		return len(x) > 0
	}
	return true
}

// Or is Python's `a or b`.
func Or(a, b any) any {
	if Truthy(a) {
		return a
	}
	return b
}

// OrS is str(a or b) with b a string default.
func OrS(a any, b string) string {
	if Truthy(a) {
		return Str(a)
	}
	return b
}

// Int is Python's int(v) for a number or a numeric string; ok=false where Python would raise.
func Int(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		return ParseInt(x)
	case []byte:
		return ParseInt(string(x))
	}
	return 0, false
}

// I is Int(v) with 0 where Python would raise (callers guard first where Python would).
func I(v any) int64 { n, _ := Int(v); return n }

// ParseInt is Python's int(str): surrounding whitespace, a sign, digits (any Unicode decimal digit), underscores
// between digits.
func ParseInt(s string) (int64, bool) {
	s = Strip(s)
	if s == "" {
		return 0, false
	}
	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	var n int64
	prevUnderscore := true
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		d := digitValue(r)
		if d < 0 {
			return 0, false
		}
		prevUnderscore = false
		n = n*10 + int64(d)
	}
	if prevUnderscore {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// Float is Python's float(v); ok=false where Python would raise.
func Float(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.ReplaceAll(Strip(x), "_", "")
		switch strings.ToLower(s) {
		case "inf", "+inf", "infinity", "+infinity":
			return math.Inf(1), true
		case "-inf", "-infinity":
			return math.Inf(-1), true
		case "nan", "+nan", "-nan":
			return math.NaN(), true
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil && !strings.Contains(err.Error(), "range") {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// Num is float(v) with 0 for anything Python would refuse.
func Num(v any) float64 { f, _ := Float(v); return f }

func digitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	if unicode.Is(unicode.Nd, r) {
		// Every Unicode decimal-digit block is ten code points starting at its zero; the zeros of the blocks
		// are found by walking down to a code point whose value mod the block start is consistent.
		for _, z := range ndZeros {
			if r >= z && r < z+10 {
				return int(r - z)
			}
		}
		for _, z := range ndZeros2 {
			if r >= z && r < z+50 { // the mathematical digit runs: five blocks of ten
				return int((r - z) % 10)
			}
		}
	}
	return -1
}

var ndZeros = []rune{0x660, 0x6F0, 0x7C0, 0x966, 0x9E6, 0xA66, 0xAE6, 0xB66, 0xBE6, 0xC66, 0xCE6, 0xD66, 0xDE6,
	0xE50, 0xED0, 0xF20, 0x1040, 0x1090, 0x17E0, 0x1810, 0x1946, 0x19D0, 0x1A80, 0x1A90, 0x1B50, 0x1BB0, 0x1C40,
	0x1C50, 0xA620, 0xA8D0, 0xA900, 0xA9D0, 0xA9F0, 0xAA50, 0xABF0, 0xFF10}
var ndZeros2 = []rune{0x1D7CE}

// IsDigit is Python's str.isdigit() restricted to decimal digits (the ones int() accepts).
func IsDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Nd, r) {
			return false
		}
	}
	return true
}

// IsAlnum is Python's str.isalnum() for one rune.
func IsAlnumRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

// IsSpaceRune is Python's str.isspace() for one rune.
func IsSpaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x1f, 0x85:
		return true
	}
	return unicode.IsSpace(r) || unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp)
}

// Strip is Python's str.strip() (Unicode whitespace).
func Strip(s string) string { return strings.TrimFunc(s, IsSpaceRune) }

// LStrip / RStrip are str.lstrip() / str.rstrip().
func LStrip(s string) string { return strings.TrimLeftFunc(s, IsSpaceRune) }
func RStrip(s string) string { return strings.TrimRightFunc(s, IsSpaceRune) }

// Split is Python's str.split() with no separator: runs of whitespace, no empty items.
func Split(s string) []string { return strings.FieldsFunc(s, IsSpaceRune) }

// Len is Python's len(str): code points.
func Len(s string) int { return utf8.RuneCountInString(s) }

// Slice is Python's s[i:j] on code points, with negative indexes and clamping. j=nil means to the end.
func Slice(s string, i int, j ...int) string {
	if isASCII(s) {
		n := len(s)
		a, b := clamp(i, n), n
		if len(j) > 0 {
			b = clamp(j[0], n)
		}
		if a >= b {
			return ""
		}
		return s[a:b]
	}
	r := []rune(s)
	n := len(r)
	a, b := clamp(i, n), n
	if len(j) > 0 {
		b = clamp(j[0], n)
	}
	if a >= b {
		return ""
	}
	return string(r[a:b])
}

// Head is s[:n].
func Head(s string, n int) string { return Slice(s, 0, n) }

func clamp(i, n int) int {
	if i < 0 {
		i += n
		if i < 0 {
			i = 0
		}
	}
	if i > n {
		i = n
	}
	return i
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// SplitLines is Python's str.splitlines() (no keepends).
func SplitLines(s string) []string {
	var out []string
	start := 0
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			i += w
			start = i
			continue
		case '\r':
			out = append(out, s[start:i])
			i += w
			if i < len(s) && s[i] == '\n' {
				i++
			}
			start = i
			continue
		}
		i += w
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Lower / Upper / Title are Python's str.lower(), str.upper() for the text the pages use.
func Lower(s string) string { return strings.ToLower(s) }
func Upper(s string) string { return strings.ToUpper(s) }

// Capitalize is Python's str.capitalize().
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	r, w := utf8.DecodeRuneInString(s)
	return string(unicode.ToTitle(r)) + strings.ToLower(s[w:])
}

// Repr is Python's repr() of a str (as used in log lines): single quotes unless the text has one and no double.
func Repr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(q):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\x` + hex2(int(r)))
		case !unicode.IsPrint(r) && r > 0x7f:
			if r <= 0xff {
				b.WriteString(`\x` + hex2(int(r)))
			} else if r <= 0xffff {
				b.WriteString(`\u` + leftPad(strconv.FormatInt(int64(r), 16), 4))
			} else {
				b.WriteString(`\U` + leftPad(strconv.FormatInt(int64(r), 16), 8))
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

func hex2(n int) string { return leftPad(strconv.FormatInt(int64(n), 16), 2) }

func leftPad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

// ListRepr is Python's repr of a list of JSON values (rarely shown; kept for completeness).
func ListRepr(xs []any) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		if s, ok := x.(string); ok {
			parts[i] = Repr(s)
		} else {
			parts[i] = Str(x)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Plural returns "" when n == 1 else "s".
func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
