package py

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
)

// OMap is a JSON object that keeps its key order, like a Python dict from json.load. Values are
// nil, bool, int64 (a JSON number without fraction or exponent), float64, string, []any or *OMap.
type OMap struct {
	keys []string
	m    map[string]any
}

// NewOMap builds an ordered map from alternating key, value pairs.
func NewOMap(kv ...any) *OMap {
	o := &OMap{m: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func (o *OMap) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Keys in insertion order.
func (o *OMap) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// Get returns the value or nil (Python d.get(k)).
func (o *OMap) Get(k string) any {
	if o == nil {
		return nil
	}
	return o.m[k]
}

// Has is `k in d`.
func (o *OMap) Has(k string) bool {
	if o == nil {
		return false
	}
	_, ok := o.m[k]
	return ok
}

// GetD is d.get(k, def): def only when the key is missing.
func (o *OMap) GetD(k string, def any) any {
	if o == nil {
		return def
	}
	if v, ok := o.m[k]; ok {
		return v
	}
	return def
}

// Set keeps the first position of an existing key (Python dict assignment).
func (o *OMap) Set(k string, v any) *OMap {
	if o.m == nil {
		o.m = map[string]any{}
	}
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
	return o
}

// Delete removes a key.
func (o *OMap) Delete(k string) {
	if o == nil {
		return
	}
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// Copy is dict(d) (shallow).
func (o *OMap) Copy() *OMap {
	n := &OMap{m: map[string]any{}}
	if o == nil {
		return n
	}
	for _, k := range o.keys {
		n.Set(k, o.m[k])
	}
	return n
}

// Map returns o.Get(k) as *OMap, or nil when it is not an object.
func (o *OMap) Map(k string) *OMap { m, _ := o.Get(k).(*OMap); return m }

// List returns o.Get(k) as a list, or nil when it is not one.
func (o *OMap) List(k string) []any { l, _ := o.Get(k).([]any); return l }

// S is str(d.get(k, "")) for a string value; "" for a missing key or a non-string.
func (o *OMap) S(k string) string { s, _ := o.Get(k).(string); return s }

// PyRepr is repr(dict) (rarely shown).
func (o *OMap) PyRepr() string {
	var parts []string
	for _, k := range o.Keys() {
		v := o.m[k]
		vs := Str(v)
		if s, ok := v.(string); ok {
			vs = Repr(s)
		}
		parts = append(parts, Repr(k)+": "+vs)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// AsMap returns v as *OMap or nil (isinstance(v, dict)).
func AsMap(v any) *OMap { m, _ := v.(*OMap); return m }

// AsList returns v as []any or nil (isinstance(v, list)).
func AsList(v any) []any { l, _ := v.([]any); return l }

// IsInt is isinstance(v, int) for a JSON value (bool excluded: JSON true is never an int here).
func IsInt(v any) bool { _, ok := v.(int64); return ok }

// IsNumber is isinstance(v, (int, float)) and not bool.
func IsNumber(v any) bool {
	switch v.(type) {
	case int64, float64, int:
		return true
	}
	return false
}

// --- decoding -------------------------------------------------------------------------------------

// ParseJSON is json.loads: ordered objects, int64/float64 numbers. Like Python it accepts NaN/Infinity? No:
// Python accepts them, but no file this app reads contains them; they are refused here.
func ParseJSON(data []byte) (any, error) { return parseJSONFast(data) }

// parseJSONStd is the encoding/json based parser (the reference the fast parser is tested against).
func parseJSONStd(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("extra data")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeFrom(dec, t)
}

func decodeFrom(dec *json.Decoder, t json.Token) (any, error) {
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			o := &OMap{m: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("bad key")
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v) // a repeated key keeps its first position and the last value, like Python
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			l := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return l, nil
		}
		return nil, errors.New("unexpected delimiter")
	case json.Number:
		s := string(x)
		if !strings.ContainsAny(s, ".eE") {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n, nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil && !strings.Contains(err.Error(), "range") {
			return nil, err
		}
		return f, nil
	default:
		return x, nil // string, bool, nil
	}
}

// ReadJSON is the app's read_json(path, default): the parsed file, or def when it is missing or not JSON.
func ReadJSON(path string, def any) any {
	st, err := os.Stat(path)
	if err != nil {
		return def
	}
	ver := jsonVer{size: st.Size(), mtime: st.ModTime().UnixNano()}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		ver.ino = int64(s.Ino)
	}
	jsonMu.Lock()
	c, hit := jsonCache[path]
	jsonMu.Unlock()
	if hit && c.ver == ver {
		if c.bad {
			return def
		}
		return DeepCopy(c.v)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	var v any
	bad := !utf8.Valid(data) // Python's open() would raise UnicodeDecodeError (a ValueError): the default
	if !bad {
		v, err = ParseJSON(data)
		bad = err != nil
	}
	if int64(len(data)) == ver.size {
		jsonMu.Lock()
		if len(jsonCache) > 512 {
			jsonCache = map[string]jsonEntry{}
		}
		jsonCache[path] = jsonEntry{ver, v, bad}
		jsonMu.Unlock()
	}
	if bad {
		return def
	}
	return DeepCopy(v)
}

// The parsed files are kept per (inode, size, mtime) and handed out as deep copies, so a caller may change
// what it gets; a rewritten file is parsed again.
type jsonVer struct{ size, mtime, ino int64 }

type jsonEntry struct {
	ver jsonVer
	v   any
	bad bool
}

var (
	jsonMu    sync.Mutex
	jsonCache = map[string]jsonEntry{}
)

// DeepCopy copies a parsed JSON value (maps and lists; scalars are immutable).
func DeepCopy(v any) any {
	switch x := v.(type) {
	case *OMap:
		n := &OMap{keys: append([]string(nil), x.keys...), m: make(map[string]any, len(x.m))}
		for k, e := range x.m {
			n.m[k] = DeepCopy(e)
		}
		return n
	case []any:
		n := make([]any, len(x))
		for i, e := range x {
			n[i] = DeepCopy(e)
		}
		return n
	}
	return v
}

// ReadJSONMap is ReadJSON(path, {}) or {} when the file holds something other than an object
// (the common `read_json(p, {}) or {}` followed by dict use).
func ReadJSONMap(path string) *OMap {
	if m, ok := ReadJSON(path, nil).(*OMap); ok {
		return m
	}
	return NewOMap()
}

// --- encoding (Python json.dumps) ----------------------------------------------------------------

// Dumps is json.dumps(v) (separators ", " and ": ", ensure_ascii); indent >= 0 gives json.dumps(v, indent=n)
// (item separator "," then a newline). Pass indent -1 for no indentation. sortKeys is sort_keys=True.
func Dumps(v any, indent int, sortKeys ...bool) string {
	var b strings.Builder
	sk := len(sortKeys) > 0 && sortKeys[0]
	writeJSON(&b, v, indent, 0, sk, ", ", ": ")
	return b.String()
}

// DumpsCompact is json.dumps(v, separators=(",", ":")).
func DumpsCompact(v any, sortKeys ...bool) string {
	var b strings.Builder
	writeJSON(&b, v, -1, 0, len(sortKeys) > 0 && sortKeys[0], ",", ":")
	return b.String()
}

func writeJSON(b *strings.Builder, v any, indent, level int, sortKeys bool, itemSep, keySep string) {
	nl := func(l int) {
		if indent >= 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*l))
		}
	}
	sep := itemSep
	if indent >= 0 {
		sep = strings.TrimRight(itemSep, " ")
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
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		switch s := FloatRepr(x); s {
		case "inf":
			b.WriteString("Infinity")
		case "-inf":
			b.WriteString("-Infinity")
		case "nan":
			b.WriteString("NaN")
		default:
			b.WriteString(s)
		}
	case string:
		writeJSONString(b, x)
	case []string:
		l := make([]any, len(x))
		for i, s := range x {
			l[i] = s
		}
		writeJSON(b, l, indent, level, sortKeys, itemSep, keySep)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, it := range x {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(level + 1)
			writeJSON(b, it, indent, level+1, sortKeys, itemSep, keySep)
		}
		nl(level)
		b.WriteByte(']')
	case *OMap:
		keys := append([]string(nil), x.Keys()...)
		if len(keys) == 0 {
			b.WriteString("{}")
			return
		}
		if sortKeys {
			sort.Strings(keys)
		}
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(level + 1)
			writeJSONString(b, k)
			b.WriteString(keySep)
			writeJSON(b, x.m[k], indent, level+1, sortKeys, itemSep, keySep)
		}
		nl(level)
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

// writeJSONString is Python's ensure_ascii string encoder.
func writeJSONString(b *strings.Builder, s string) {
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
					r1, r2 := utf16.EncodeRune(r)
					b.WriteString(`\u` + leftPad(strconv.FormatInt(int64(r1), 16), 4))
					b.WriteString(`\u` + leftPad(strconv.FormatInt(int64(r2), 16), 4))
				} else {
					b.WriteString(`\u` + leftPad(strconv.FormatInt(int64(r), 16), 4))
				}
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
