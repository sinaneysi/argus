package py

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Python's http.cookies.SimpleCookie(header) parser (3.12), so the session and theme cookies are read exactly
// as the Python app read them: the last of two equal names wins, a quoted value is unquoted, an invalid string
// yields no cookies, and an illegal name is a CookieError.

var cookiePattern = regexp.MustCompile(`^[ \t\n\r\f\v]*` +
	`([\w!#%&'~` + "`" + `><@,:/$*+\-.^|)(?}{=]+?)` +
	`([ \t\n\r\f\v]*=[ \t\n\r\f\v]*` +
	`("(?:[^\\"]|\\.)*"` +
	`|\w{3},[ \t\n\r\f\v][\w \t\n\r\f\v-]{9,11}[ \t\n\r\f\v][\d:]{8}[ \t\n\r\f\v]GMT` +
	`|[\w!#%&'~` + "`" + `><@,:/$*+\-.^|)(?}{=\[\]]*` +
	`))?` +
	`[ \t\n\r\f\v]*([ \t\n\r\f\v]+|;|$)`)

var reserved = map[string]bool{"expires": true, "path": true, "comment": true, "domain": true, "max-age": true,
	"secure": true, "httponly": true, "version": true, "samesite": true}
var flags = map[string]bool{"secure": true, "httponly": true}

const legalChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&'*+-.^_`|~:"

// ErrCookie is http.cookies.CookieError.
var ErrCookie = errors.New("CookieError")

func legalKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		if strings.IndexByte(legalChars, k[i]) < 0 {
			return false
		}
	}
	return true
}

var octal = regexp.MustCompile(`\\[0-3][0-7][0-7]|\\.`)

// cookieUnquote is http.cookies._unquote.
func cookieUnquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	return octal.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) == 4 {
			n, err := strconv.ParseUint(m[1:], 8, 8)
			if err == nil {
				return string(rune(n))
			}
		}
		return m[1:]
	})
}

// ParseCookies is SimpleCookie(header): name -> value. err is ErrCookie where Python raises CookieError.
func ParseCookies(header string) (map[string]string, error) {
	out := map[string]string{}
	type item struct {
		attr       bool
		key, value string
		hasValue   bool
	}
	var items []item
	morselSeen := false
	i, n := 0, len(header)
	for i >= 0 && i < n {
		loc := cookiePattern.FindStringSubmatchIndex(header[i:])
		if loc == nil {
			break
		}
		key := header[i+loc[2] : i+loc[3]]
		hasVal := loc[6] >= 0
		val := ""
		if hasVal {
			val = header[i+loc[6] : i+loc[7]]
		}
		end := i + loc[1]
		if end == i { // an empty match would loop forever (cannot happen: the key needs one character)
			break
		}
		i = end
		lk := strings.ToLower(key)
		switch {
		case key[0] == '$':
			if !morselSeen {
				continue
			}
			items = append(items, item{attr: true, key: key[1:], value: val, hasValue: hasVal})
		case reserved[lk]:
			if !morselSeen {
				return map[string]string{}, nil
			}
			if !hasVal {
				if flags[lk] {
					items = append(items, item{attr: true, key: key})
				} else {
					return map[string]string{}, nil
				}
			} else {
				items = append(items, item{attr: true, key: key, value: cookieUnquote(val), hasValue: true})
			}
		case hasVal:
			items = append(items, item{key: key, value: cookieUnquote(val), hasValue: true})
			morselSeen = true
		default:
			return map[string]string{}, nil
		}
	}
	for _, it := range items {
		if it.attr {
			if !reserved[strings.ToLower(it.key)] {
				return out, ErrCookie // Morsel.__setitem__ of an unknown attribute
			}
			continue
		}
		if reserved[strings.ToLower(it.key)] || !legalKey(it.key) {
			return out, ErrCookie
		}
		out[it.key] = it.value
	}
	return out, nil
}
