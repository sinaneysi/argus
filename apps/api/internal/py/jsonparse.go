package py

import (
	"errors"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// A direct JSON parser with Python's json.loads semantics (ordered objects, int/float split, NaN/Infinity
// literals, control characters refused in strings). encoding/json's token API is several times slower, and the
// pages parse files of a few hundred KB on every request.

var errJSON = errors.New("invalid JSON")

type jparser struct {
	s     []byte
	i     int
	depth int
}

func (p *jparser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jparser) lit(word string) bool {
	if len(p.s)-p.i >= len(word) && string(p.s[p.i:p.i+len(word)]) == word {
		p.i += len(word)
		return true
	}
	return false
}

func (p *jparser) value() (any, error) {
	p.ws()
	if p.i >= len(p.s) {
		return nil, errJSON
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case c == 't':
		if p.lit("true") {
			return true, nil
		}
	case c == 'f':
		if p.lit("false") {
			return false, nil
		}
	case c == 'n':
		if p.lit("null") {
			return nil, nil
		}
	case c == 'N':
		if p.lit("NaN") {
			return math.NaN(), nil
		}
	case c == 'I':
		if p.lit("Infinity") {
			return math.Inf(1), nil
		}
	case c == '-' || (c >= '0' && c <= '9'):
		if c == '-' && p.i+1 < len(p.s) && p.s[p.i+1] == 'I' {
			p.i++
			if p.lit("Infinity") {
				return math.Inf(-1), nil
			}
			return nil, errJSON
		}
		return p.number()
	}
	return nil, errJSON
}

func (p *jparser) number() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.s) {
		return nil, errJSON
	}
	if p.s[p.i] == '0' {
		p.i++
	} else if p.s[p.i] >= '1' && p.s[p.i] <= '9' {
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
	} else {
		return nil, errJSON
	}
	isFloat := false
	if p.i < len(p.s) && p.s[p.i] == '.' && p.i+1 < len(p.s) && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
		isFloat = true
		p.i++
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		j := p.i + 1
		if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
			j++
		}
		if j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
			isFloat = true
			p.i = j
			for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
				p.i++
			}
		}
	}
	text := string(p.s[start:p.i])
	if !isFloat {
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n, nil
		}
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); !ok || ne.Err != strconv.ErrRange {
			return nil, errJSON
		}
	}
	return f, nil
}

func hexv(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

func (p *jparser) u4() (rune, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	r := 0
	for k := 0; k < 4; k++ {
		h := hexv(p.s[p.i+k])
		if h < 0 {
			return 0, false
		}
		r = r<<4 | h
	}
	p.i += 4
	return rune(r), true
}

func (p *jparser) str() (string, error) {
	p.i++ // the opening quote
	start := p.i
	for p.i < len(p.s) { // fast path: no escapes
		c := p.s[p.i]
		if c == '"' {
			s := string(p.s[start:p.i])
			p.i++
			return s, nil
		}
		if c == '\\' || c < 0x20 {
			break
		}
		p.i++
	}
	buf := append([]byte(nil), p.s[start:p.i]...)
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return string(buf), nil
		case c < 0x20:
			return "", errJSON
		case c == '\\':
			p.i++
			if p.i >= len(p.s) {
				return "", errJSON
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case '"', '\\', '/':
				buf = append(buf, e)
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'u':
				r, ok := p.u4()
				if !ok {
					return "", errJSON
				}
				if utf16.IsSurrogate(r) && p.i+6 <= len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
					save := p.i
					p.i += 2
					if r2, ok := p.u4(); ok && r2 >= 0xDC00 && r2 <= 0xDFFF && r < 0xDC00 {
						r = utf16.DecodeRune(r, r2)
					} else {
						p.i = save
					}
				}
				buf = utf8.AppendRune(buf, r)
			default:
				return "", errJSON
			}
		default:
			buf = append(buf, c)
			p.i++
		}
	}
	return "", errJSON
}

func (p *jparser) object() (any, error) {
	p.depth++
	if p.depth > 1000 {
		return nil, errJSON
	}
	p.i++
	o := &OMap{m: map[string]any{}}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		p.depth--
		return o, nil
	}
	for {
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, errJSON
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, errJSON
		}
		p.i++
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		o.Set(k, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, errJSON
		}
		if p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.s[p.i] == '}' {
			p.i++
			p.depth--
			return o, nil
		}
		return nil, errJSON
	}
}

func (p *jparser) array() (any, error) {
	p.depth++
	if p.depth > 1000 {
		return nil, errJSON
	}
	p.i++
	l := []any{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		p.depth--
		return l, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		l = append(l, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, errJSON
		}
		if p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.s[p.i] == ']' {
			p.i++
			p.depth--
			return l, nil
		}
		return nil, errJSON
	}
}

func parseJSONFast(data []byte) (any, error) {
	p := &jparser{s: data}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, errors.New("extra data")
	}
	return v, nil
}
