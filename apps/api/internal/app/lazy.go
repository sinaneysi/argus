package app

// Port of app/lazy.py (parse, strip, the stored JSON) and of the Lazy reading helpers of app/views_fix.py (the card,
// the "Full analysis" fold, the one-line QA version). Every value is escaped with E() (fixInl).

import (
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// Lazy is the five-line summary; Source is report | derived | model.
type Lazy struct {
	What, Why, Impact, Fix, Status, Source, Ref string
}

var (
	lzFields = []string{"what", "why", "impact", "fix", "status"}
	lzLabels = map[string]string{"what": "What broke", "why": "Why", "impact": "Impact", "fix": "Fix", "status": "Status"}
	lzKeys   = map[string]string{"what broke": "what", "what": "what", "what happened": "what", "broke": "what", "why": "why",
		"cause": "why", "root cause": "why", "impact": "impact", "who is affected": "impact", "fix": "fix",
		"the fix": "fix", "recommended fix": "fix", "status": "status", "state": "status"}
	lzSources  = map[string]bool{"report": true, "derived": true, "model": true}
	lzHead     = regexp.MustCompile(`(?im)^#{1,3}[ \t]+lazy[ \t]+reading\b[^\n]*$`)
	lzNextHead = regexp.MustCompile(`^#{1,6}[ \t]`)
	lzField    = regexp.MustCompile(`^[ \t]*[-*][ \t]+\*\*([^*:\n]{2,30}?)[ \t]*:?[ \t]*\*\*[ \t]*:?[ \t]*(.*)$`)
	lzPlain    = regexp.MustCompile(`^[ \t]*[-*][ \t]+([A-Za-z][A-Za-z ]{1,20}):[ \t]*(.*)$`)
	lzStatusK  = map[string]string{"open": "fail", "needs operator": "warn", "mitigated": "ok", "resolved": "ok"}
	lzSourceT  = map[string]string{"report": "From the report.",
		"derived": "Derived by Argus from the stored RCA and its fix options.",
		"model":   "Summarised by a model from the full RCA; check it against the analysis."}
)

// The field caps of app/lazy.py: one short line, at most lzMaxWords words and lzMaxField characters ("…" included).
const (
	lzMaxField    = 180
	lzMaxWords    = 25
	lzMinSentence = 5
	lzMinClause   = 8
	lzCutTrail    = " ,;:.!?—–-\u060c\u061b\u061f"
)

var lzGrade = regexp.MustCompile(`(?i) \((proven|probable)\)\.?$`)

func lzBalanced(ws []string) bool {
	t := strings.Join(ws, " ")
	return strings.Count(t, "(") <= strings.Count(t, ")") && strings.Count(t, "`")%2 == 0
}

func lzEndsIn(w string, set ...string) bool {
	for _, x := range set {
		if strings.HasSuffix(w, x) {
			return true
		}
	}
	return false
}

// lzTrim is lazy.trim(v): the text within the caps and where it was cut ("", sentence, clause, word).
func lzTrim(v string) (string, string) {
	s := strings.Join(py.Split(v), " ")
	ws := py.Split(s)
	if len(ws) <= lzMaxWords && py.Len(s) <= lzMaxField {
		return s, ""
	}
	tail := ""
	if m := lzGrade.FindStringSubmatchIndex(s); m != nil {
		tail = " (" + strings.ToLower(s[m[2]:m[3]]) + ")"
		s = s[:m[0]]
		ws = strings.Split(s, " ")
	}
	maxw := lzMaxWords
	if tail != "" {
		maxw--
	}
	room := lzMaxField - 1 - py.Len(tail)
	keep, n := []string{}, 0
	for _, w := range ws {
		add := py.Len(w)
		if len(keep) > 0 {
			add++
		}
		if len(keep) >= maxw || n+add > room {
			break
		}
		keep = append(keep, w)
		n += add
	}
	if len(keep) == len(ws) {
		return s + tail, ""
	}
	if len(keep) == 0 {
		return strings.TrimRight(py.Head(s, room), lzCutTrail) + "…" + tail, "word"
	}
	cut, how := len(keep), "word"
	for i := len(keep); i >= lzMinSentence; i-- {
		if lzEndsIn(keep[i-1], ".", "!", "?", ";", "\u061f", "\u061b") && lzBalanced(keep[:i]) {
			cut, how = i, "sentence"
			break
		}
	}
	if how == "word" {
		for i := len(keep); i >= lzMinClause; i-- {
			if (lzEndsIn(keep[i-1], ",", ":", "\u060c") || ws[i] == "—" || ws[i] == "–" || ws[i] == "-") && lzBalanced(keep[:i]) {
				cut, how = i, "clause"
				break
			}
		}
	}
	return strings.TrimRight(strings.Join(keep[:cut], " "), lzCutTrail) + "…" + tail, how
}

func lzClean(s string) string {
	t, _ := lzTrim(s)
	return t
}

func lzNormStatus(v string) string {
	t := strings.Join(py.Split(strings.ToLower(v)), " ")
	switch {
	case t == "":
		return ""
	case strings.Contains(t, "mitigat"):
		return "mitigated"
	case strings.Contains(t, "operator") || strings.Contains(t, "approv") || strings.Contains(t, "human"):
		return "needs operator"
	case strings.Contains(t, "resolved") || strings.Contains(t, "fixed") || strings.Contains(t, "closed"):
		return "resolved"
	}
	return "open"
}

func lzSection(md string) (int, int, bool) {
	loc := lzHead.FindStringIndex(md)
	if loc == nil {
		return 0, 0, false
	}
	pos := loc[1]
	if pos < len(md) && md[pos] == '\n' {
		pos++
	}
	end, off := len(md), pos
	for _, line := range strings.Split(md[pos:], "\n") {
		if lzNextHead.MatchString(line) {
			end = off
			break
		}
		off += len(line) + 1
	}
	if end > len(md) {
		end = len(md)
	}
	return loc[0], end, true
}

func lzParseFields(text string) map[string]string {
	vals := map[string]string{}
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		m := lzField.FindStringSubmatch(line)
		if m == nil {
			m = lzPlain.FindStringSubmatch(line)
		}
		if m != nil {
			key := lzKeys[strings.Join(py.Split(strings.ToLower(m[1])), " ")]
			cur = key
			if key != "" {
				if _, ok := vals[key]; !ok {
					vals[key] = m[2]
				} else {
					cur = ""
				}
			}
			continue
		}
		st := py.Strip(line)
		if cur != "" && st != "" && !strings.HasPrefix(st, "-") && !strings.HasPrefix(st, "*") && !strings.HasPrefix(st, "#") {
			vals[cur] = vals[cur] + " " + st
		} else if st == "" {
			continue
		} else {
			cur = ""
		}
	}
	return vals
}

func lzNormalize(vals map[string]string) (Lazy, bool) {
	l := Lazy{What: lzClean(vals["what"]), Why: lzClean(vals["why"]), Impact: lzClean(vals["impact"]),
		Fix: lzClean(vals["fix"]), Status: lzNormStatus(lzClean(vals["status"]))}
	if l.Status == "" {
		l.Status = "open"
	}
	if l.What == "" || l.Fix == "" {
		return Lazy{}, false
	}
	return l, true
}

// LazyParse is lazy.parse(md).
func LazyParse(md string) (Lazy, bool) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	a, b, ok := lzSection(md)
	if !ok {
		return Lazy{}, false
	}
	return lzNormalize(lzParseFields(md[a:b]))
}

// LazyStrip is lazy.strip(md).
func LazyStrip(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	a, b, ok := lzSection(md)
	if !ok {
		return md
	}
	head, tail := strings.TrimRight(md[:a], "\n"), strings.TrimLeft(md[b:], "\n")
	if py.Strip(head) != "" {
		return head + "\n\n" + tail
	}
	return tail
}

// LazyLoad is lazy.loads(text): the stored JSON.
func LazyLoad(v any) (Lazy, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return Lazy{}, false
	}
	parsed, err := py.ParseJSON([]byte(s))
	if err != nil {
		return Lazy{}, false
	}
	m := py.AsMap(parsed)
	if m == nil {
		return Lazy{}, false
	}
	vals := map[string]string{}
	for _, k := range lzFields {
		if x, ok := m.Get(k).(string); ok {
			vals[k] = x
		}
	}
	l, ok := lzNormalize(vals)
	if !ok {
		return Lazy{}, false
	}
	l.Source = "derived"
	if x, ok := m.Get("source").(string); ok && lzSources[x] {
		l.Source = x
	}
	if x, ok := m.Get("ref").(string); ok {
		l.Ref = x
	}
	return l, true
}

// LazyRow is views_fix.lazy_row(row, md_key).
func LazyRow(r Row, mdKey string) (Lazy, bool) {
	if r.Has("lazy") {
		if l, ok := LazyLoad(r.Get("lazy")); ok {
			return l, true
		}
	}
	if mdKey != "" && r.Has(mdKey) {
		if l, ok := LazyParse(r.S(mdKey)); ok {
			l.Source, l.Ref = "report", ""
			return l, true
		}
	}
	return Lazy{}, false
}

// LazyIncident is views_fix.lazy_incident(inc).
func LazyIncident(inc Row) (Lazy, bool) {
	l, ok := LazyRow(inc, "")
	if !ok {
		if reps := Q("SELECT * FROM rca_reports WHERE incident_id=? ORDER BY created DESC LIMIT 1", inc.I("id")); len(reps) > 0 {
			l, ok = LazyRow(reps[0], "content")
		}
	}
	if !ok {
		ps := Q("SELECT p.* FROM incident_events e JOIN triage_problems p ON p.run_id=e.run_id AND p.idx=e.idx "+
			"WHERE e.incident_id=? AND e.kind='finding' ORDER BY e.at DESC LIMIT 1", inc.I("id"))
		if len(ps) > 0 {
			l, ok = LazyRow(ps[0], "rca")
		}
	}
	if ok && inc.Get("status") == "resolved" {
		l.Status = "resolved"
	}
	return l, ok
}

func (l Lazy) field(k string) string {
	switch k {
	case "what":
		return l.What
	case "why":
		return l.Why
	case "impact":
		return l.Impact
	case "fix":
		return l.Fix
	}
	return l.Status
}
