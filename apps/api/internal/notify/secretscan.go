package notify

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// The secret scan every message passes before it leaves the host. A hit is replaced by [REDACTED:<kind>]; only the
// kinds and counts are logged, never the value.

type pattern struct {
	kind string
	re   *regexp.Regexp
	grp  int // the group to replace, 0 for the whole match
}

var patterns = []pattern{ // order matters: blocks first
	{"private-key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), 0},
	{"gitlab-token", regexp.MustCompile(`\b(?:glpat|gldt|glrt|glptt|gloas|glsoat)-[A-Za-z0-9_-]{16,}`), 0},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})`), 0},
	{"matrix-token", regexp.MustCompile(`\bsyt_[A-Za-z0-9_-]{10,}`), 0},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), 0},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`), 0},
	{"aws-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), 0},
	{"url-credentials", regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@"'<>]+:([^@\s/"'<>]+)@`), 1},
	{"bearer", regexp.MustCompile(`\b(?:Bearer|Basic)\s+([A-Za-z0-9._~+/=-]{12,})`), 1},
	{"private-token", regexp.MustCompile(`(?i)\b(?:PRIVATE-TOKEN|X-Api-Key|X-Auth-Token|Authorization)\s*[:=]\s*["']?([^\s"'<>]{8,})`), 1},
	{"assignment", regexp.MustCompile(`(?i)\b[A-Za-z0-9_.-]*(?:passw(?:or)?d|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|` +
		`client[_-]?secret|credentials?)[A-Za-z0-9_.-]*["']?\s*[:=]\s*["']?([^\s"',;&<>` + "`" + `]{6,})`), 1},
}

var (
	safeValue = regexp.MustCompile(`(?i)^\[REDACTED|^\*+$|^<[^>]*>$|^\$\{?[A-Z_]+\}?$|^(?:true|false|null|none|required|optional|` +
		`redacted|xxx+|\.\.\.|changeme)$`)
	proseWord = regexp.MustCompile(`^[A-Za-z][a-z]{1,19}$`)
	proseSep  = regexp.MustCompile(`:\s+["']?$`)
	absPath   = regexp.MustCompile(`^/[\p{L}\p{N}_./-]+$`)
)

// Allowlist holds exact substrings that are reviewed fixtures, never redacted (one per line, "#" comments).
type Allowlist []string

// LoadAllowlist reads an allowlist file; a missing file is an empty list.
func LoadAllowlist(path string) Allowlist {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out Allowlist
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimLeft(l, " \t"), "#") {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

func (a Allowlist) spans(text string) [][2]int {
	var out [][2]int
	for _, e := range a {
		for i := 0; ; {
			j := strings.Index(text[i:], e)
			if j < 0 {
				break
			}
			out = append(out, [2]int{i + j, i + j + len(e)})
			i += j + 1
			if i > len(text) {
				break
			}
		}
	}
	return out
}

// Redact returns text with every secret replaced and the hits per kind.
func Redact(text string, allow Allowlist) (string, map[string]int) {
	hits := map[string]int{}
	spans := allow.spans(text)
	for _, p := range patterns {
		var b strings.Builder
		last := 0
		for _, m := range p.re.FindAllStringSubmatchIndex(text, -1) {
			s, e := m[0], m[1]
			gs, ge := m[2*p.grp], m[2*p.grp+1]
			b.WriteString(text[last:s])
			last = e
			whole := text[s:e]
			val := ""
			if gs >= 0 {
				val = text[gs:ge]
			}
			keep := (p.grp > 0 && safeValue.MatchString(val)) || (p.kind == "assignment" && prose(text[s:gs], val))
			for _, sp := range spans {
				if gs >= 0 && sp[0] <= gs && ge <= sp[1] {
					keep = true
				}
			}
			if keep {
				b.WriteString(whole)
				continue
			}
			hits[p.kind]++
			tag := "[REDACTED:" + p.kind + "]"
			if p.grp == 0 {
				b.WriteString(tag)
			} else {
				b.WriteString(text[s:gs] + tag + text[ge:e])
			}
		}
		b.WriteString(text[last:])
		text = b.String()
	}
	return text, hits
}

// prose: an "assignment" hit that is prose or code, not a value ("the token: revoked", a call, an absolute path).
func prose(head, val string) bool {
	if strings.Contains(val, "(") || absPath.MatchString(val) {
		return true
	}
	return proseSep.MatchString(head) && proseWord.MatchString(val)
}

// RedactContent redacts every text field of a message.
func RedactContent(c Content, allow Allowlist) (Content, map[string]int) {
	all := map[string]int{}
	add := func(s string) string {
		t, h := Redact(s, allow)
		for k, v := range h {
			all[k] += v
		}
		return t
	}
	c.MsgType, c.Body, c.Format, c.FormattedBody = add(c.MsgType), add(c.Body), add(c.Format), add(c.FormattedBody)
	return c, all
}

// HitsLine is "kind:n,kind:n" in kind order ("" without hits).
func HitsLine(h map[string]int) string {
	var ks []string
	for k, v := range h {
		ks = append(ks, k+":"+itoa(v))
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
