package capability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxArgsBytes is the largest arguments object a step may carry (ADR-0022 D3).
const MaxArgsBytes = 2048

// Redacted replaces the value of a secret field everywhere outside the capability.
const Redacted = "<redacted>"

// Type is the JSON type of one argument.
type Type int

const (
	String    Type = iota + 1
	Int            // an integer
	IntOrList      // an integer or a list of integers
	Bool
)

func (t Type) String() string {
	switch t {
	case String:
		return "string"
	case Int:
		return "integer"
	case IntOrList:
		return "integer or list of integers"
	case Bool:
		return "boolean"
	}
	return "unknown"
}

// Field is one argument of a capability.
type Field struct {
	Name     string         `json:"name"`
	Type     Type           `json:"-"`
	TypeName string         `json:"type"`
	Required bool           `json:"required,omitempty"`
	Pattern  *regexp.Regexp `json:"-"`
	MaxLen   int            `json:"max_length,omitempty"` // String: runes; IntOrList: list items
	Min, Max int            `json:"-"`                    // Int, IntOrList: the allowed range when Max > 0
	Secret   bool           `json:"secret,omitempty"`     // never in a plan, the audit, a result or a log
	// Input: the value is a reference "input:<NAME>" to a secret the admin types at approval (sealed to actd)
	Input bool `json:"input,omitempty"`
	// Digest: outside the capability (the audit) the value is shown only as "sha256:<first 12 hex>" (a search text
	// or a user name that must not be kept in clear)
	Digest    bool   `json:"-"`
	AllowList string `json:"allow_list,omitempty"` // the configuration key that limits the value
}

// Schema is the argument list of a capability.
type Schema struct {
	Fields []Field
}

func (s Schema) field(name string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// ParseArgs reads a step's arguments: one flat JSON object of at most MaxArgsBytes. A value is a string, a number, a
// boolean, or a list of those; nested objects are refused. Numbers stay exact (json.Number).
func ParseArgs(raw []byte) (Args, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return Args{}, nil
	}
	if len(raw) > MaxArgsBytes {
		return nil, Refuse(4, CodeArguments, "the arguments are larger than %d bytes", MaxArgsBytes)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var a Args
	if err := d.Decode(&a); err != nil || a == nil {
		return nil, Refuse(4, CodeArguments, "the arguments are not a JSON object")
	}
	if d.More() {
		return nil, Refuse(4, CodeArguments, "the arguments are not one JSON object")
	}
	return a, nil
}

// Check validates args against the schema: size, unknown keys, required keys, types, patterns, lengths, ranges.
func (s Schema) Check(a Args) error {
	if b, err := json.Marshal(a); err != nil || len(b) > MaxArgsBytes {
		return Refuse(4, CodeArguments, "the arguments are larger than %d bytes or not JSON", MaxArgsBytes)
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f, ok := s.field(k)
		if !ok {
			return Refuse(4, CodeArguments, "unknown argument %q", clipName(k))
		}
		if err := f.check(a[k]); err != nil {
			return err
		}
	}
	for _, f := range s.Fields {
		if _, ok := a[f.Name]; f.Required && !ok {
			return Refuse(4, CodeArguments, "argument %q is required", f.Name)
		}
	}
	return nil
}

func (f Field) check(v any) error {
	bad := func(why string, x ...any) error {
		return Refuse(4, CodeArguments, "argument %q: "+why, append([]any{f.Name}, x...)...)
	}
	switch f.Type {
	case String:
		s, ok := v.(string)
		if !ok {
			return bad("must be a string")
		}
		if !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
			return bad("holds a control character")
		}
		if f.MaxLen > 0 && utf8.RuneCountInString(s) > f.MaxLen {
			return bad("longer than %d characters", f.MaxLen)
		}
		if f.Pattern != nil && !f.Pattern.MatchString(s) {
			return bad("does not match %s", f.Pattern.String())
		}
		if f.Input && !InputRefRe.MatchString(s) {
			return bad("must be a reference input:<NAME> (A-Z, 0-9, _), never a literal value")
		}
	case Int:
		n, ok := AsInt(v)
		if !ok {
			return bad("must be an integer")
		}
		if f.Max > 0 && (n < f.Min || n > f.Max) {
			return bad("must be %d..%d", f.Min, f.Max)
		}
	case IntOrList:
		ns, ok := AsInts(v)
		if !ok || len(ns) == 0 {
			return bad("must be an integer or a non-empty list of integers")
		}
		if f.MaxLen > 0 && len(ns) > f.MaxLen {
			return bad("more than %d values", f.MaxLen)
		}
		for _, n := range ns {
			if f.Max > 0 && (n < f.Min || n > f.Max) {
				return bad("every value must be %d..%d", f.Min, f.Max)
			}
		}
	case Bool:
		if _, ok := v.(bool); !ok {
			return bad("must be true or false")
		}
	default:
		return bad("has no type")
	}
	return nil
}

// Redacted is a copy of args with every secret field replaced by "<redacted>". A key the schema does not know is
// redacted too: only known, non-secret fields leave the capability. An input field keeps its reference
// ("input:<NAME>", which names the input and is no secret) and is redacted when it holds anything else (a literal
// value the model wrote by mistake).
func (s Schema) Redacted(a Args) Args {
	out := Args{}
	for k, v := range a {
		f, ok := s.field(k)
		ref, isStr := v.(string)
		switch {
		case ok && f.Input && isStr && InputRefRe.MatchString(ref):
			out[k] = v
		case ok && f.Digest && isStr && !f.Secret && !f.Input:
			out[k] = DigestOf(ref)
		case ok && !f.Secret && !f.Input && !f.Digest:
			out[k] = v
		default:
			out[clipName(k)] = Redacted
		}
	}
	return out
}

// DigestOf is "sha256:<first 12 hex>" of s: enough to tell two values apart in the audit, not the value.
func DigestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

// Catalogue is the schema as JSON-ready fields (type names filled in) for the intake's capability list.
func (s Schema) Catalogue() []Field {
	out := make([]Field, len(s.Fields))
	for i, f := range s.Fields {
		f.TypeName = f.Type.String()
		out[i] = f
	}
	return out
}

// AsInt reads an integer argument: json.Number, a whole float64, int or int64.
func AsInt(v any) (int, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		if err != nil || n > math.MaxInt32 || n < math.MinInt32 {
			return 0, false
		}
		return int(n), true
	case float64:
		if x != math.Trunc(x) || x > math.MaxInt32 || x < math.MinInt32 {
			return 0, false
		}
		return int(x), true
	case int:
		return x, true
	case int64:
		if x > math.MaxInt32 || x < math.MinInt32 {
			return 0, false
		}
		return int(x), true
	}
	return 0, false
}

// AsInts reads an integer or a list of integers.
func AsInts(v any) ([]int, bool) {
	if n, ok := AsInt(v); ok {
		return []int{n}, true
	}
	var list []any
	switch x := v.(type) {
	case []any:
		list = x
	case []int:
		return append([]int(nil), x...), true
	default:
		return nil, false
	}
	out := make([]int, 0, len(list))
	for _, e := range list {
		n, ok := AsInt(e)
		if !ok {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func clipName(k string) string {
	if len(k) > 40 {
		k = k[:40]
	}
	return strings.ToValidUTF8(k, "?")
}

// InputRefRe is a reference to a sealed input: "input:<NAME>".
var InputRefRe = regexp.MustCompile(`^input:[A-Z][A-Z0-9_]{0,31}$`)

// InputNames are the names of the sealed inputs the arguments refer to (fields marked Input), sorted, no repeats.
func (s Schema) InputNames(a Args) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range s.Fields {
		v, ok := a[f.Name].(string)
		if !f.Input || !ok || !InputRefRe.MatchString(v) {
			continue
		}
		n := strings.TrimPrefix(v, "input:")
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
