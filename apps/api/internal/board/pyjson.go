package board

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// object is a JSON object in key order. pyDumps writes values like Python's json.dumps (", " and ": " separators,
// ASCII only), so the rows and files this job writes are byte-identical to the reference job's.
type object []kv

type kv struct {
	k string
	v any
}

func (o object) get(k string) any {
	for _, x := range o {
		if x.k == k {
			return x.v
		}
	}
	return nil
}

func (o object) MarshalJSON() ([]byte, error) { return pyDumps(o), nil }

func pyDumps(v any) []byte { var b bytes.Buffer; enc(&b, v, -1, 0); return b.Bytes() }

// pyDumpsIndent is json.dumps(v, indent=n).
func pyDumpsIndent(v any, n int) []byte { var b bytes.Buffer; enc(&b, v, n, 0); return b.Bytes() }

func nl(b *bytes.Buffer, ind, lvl int) {
	if ind >= 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", ind*lvl))
	}
}

func enc(b *bytes.Buffer, v any, ind, lvl int) {
	sep := ", "
	if ind >= 0 {
		sep = ","
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		encStr(b, x)
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e16 {
			b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
		} else {
			b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
	case json.Number:
		b.WriteString(x.String())
	case []string:
		a := make([]any, len(x))
		for i, s := range x {
			a[i] = s
		}
		enc(b, a, ind, lvl)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(b, ind, lvl+1)
			enc(b, e, ind, lvl+1)
		}
		nl(b, ind, lvl)
		b.WriteByte(']')
	case []object:
		a := make([]any, len(x))
		for i, s := range x {
			a[i] = s
		}
		enc(b, a, ind, lvl)
	case object:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(b, ind, lvl+1)
			encStr(b, e.k)
			b.WriteString(": ")
			enc(b, e.v, ind, lvl+1)
		}
		nl(b, ind, lvl)
		b.WriteByte('}')
	case map[string]any:
		ks := make([]string, 0, len(x))
		for k := range x {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		o := make(object, 0, len(ks))
		for _, k := range ks {
			o = append(o, kv{k, x[k]})
		}
		enc(b, o, ind, lvl)
	default:
		raw, _ := json.Marshal(x)
		b.Write(raw)
	}
}

func encStr(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r > 0x7e {
				if r == utf8.RuneError {
					r = 0xfffd
				}
				if r > 0xffff {
					a, c := utf16.EncodeRune(r)
					fmt.Fprintf(b, `\u%04x\u%04x`, a, c)
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// pyQuote is urllib.parse.quote(s) (safe "/").
func pyQuote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.IndexByte("_.-~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pyStr is str(v) of a JSON value.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// head is s[:n] in characters.
func head(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for p := range s {
		if i == n {
			return s[:p]
		}
		i++
	}
	return s
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

// orStr is Python's `str(v or "")`.
func orStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if !x {
			return ""
		}
	case json.Number:
		if x.String() == "0" {
			return ""
		}
	case []any:
		if len(x) == 0 {
			return ""
		}
	case map[string]any:
		if len(x) == 0 {
			return ""
		}
	}
	return pyStr(v)
}
