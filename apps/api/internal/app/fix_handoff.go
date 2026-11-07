package app

// Port of app/fix_handoff.py (the parts the web app uses: split, parse, approval_kind, load_options). The Python
// module stays for bin/. The app passes redact=None (it shows what bin stored, already scanned), so the secret
// scan is not ported.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	FHProdLine   = "PRODUCTION CHANGE — needs the operator's explicit approval before any step."
	FHMaxPrompt  = 32000
	FHMaxOptions = 4
	FHMaxField   = 1200
)

var (
	fhHeadA      = regexp.MustCompile(`(?i)^#{2,3}[ \t]*(?:section[ \t]+)?(?:A[.:)][ \t]*)?how to fix`)
	fhHeadB      = regexp.MustCompile(`(?i)^#{2,3}[ \t]*(?:section[ \t]+)?(?:B[.:)][ \t]*)?prompt for (?:a|the) write[- ]access agent`)
	fhFence      = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})[ \t]*([^\n`]*)$")
	fhFenceClose = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})[ \t]*$")
	fhProdRX     = regexp.MustCompile(`(?i)^[^\p{L}\p{N}_]*PRODUCTION CHANGE`)
	fhField      = regexp.MustCompile(`^` + reWS + `*[-*]` + reWS + `+\*\*([^*:]{2,40}?)` + reWS + `*:?` + reWS + `*\*\*` + reWS + `*:?` + reWS + `*(.*)$`)
	fhOptHead    = regexp.MustCompile(`(?m)^###[ \t]+(.+?)[ \t]*#*$`)

	fhNumbered   = regexp.MustCompile(`(?m)^` + reWS + `*(\p{Nd}{1,2})[.)]` + reWS + `+(.+)$`)
	fhOptionPfx  = regexp.MustCompile(`(?i)^option` + reWS + `*\p{Nd}+` + reWS + `*[:.)\-—–]` + reWS + `*`)
	fhNumPfx     = regexp.MustCompile(`^\p{Nd}+` + reWS + `*[.)]` + reWS + `*`)
	fhKindSuffix = regexp.MustCompile(`(?i)` + reWS + `*\((?:mitigation|permanent fix|prevention)\)` + reWS + `*$`)
	fhWSRun      = regexp.MustCompile(reWS + `+`)
)

var fhKeys = map[string]string{"kind": "kind", "type": "kind", "what": "what", "change": "what", "why it works": "why",
	"why": "why", "effort": "effort", "risk": "risk", "blast radius": "blast_radius", "blast-radius": "blast_radius",
	"approval": "approval", "who approves": "approval", "who must approve": "approval", "approver": "approval"}

// fhWordAt reports whether the rune starting at s[i:] is a Python \w character (false at the end).
func fhWordAt(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return isWordRune(r)
}

// fhWordBefore reports whether the rune ending at s[:i] is a \w character (false at the start).
func fhWordBefore(s string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return isWordRune(r)
}

// fhHeadSearch is HEAD_A/HEAD_B.search(md) with re.M: the first line whose head matches and is followed by a word
// boundary; (start, end of line) or (-1, -1).
func fhHeadSearch(rx *regexp.Regexp, md string) (int, int) {
	pos := 0
	for pos <= len(md) {
		nl := strings.IndexByte(md[pos:], '\n')
		end := len(md)
		if nl >= 0 {
			end = pos + nl
		}
		line := md[pos:end]
		if loc := rx.FindStringIndex(line); loc != nil && !fhWordAt(line, loc[1]) {
			return pos, end
		}
		if nl < 0 {
			break
		}
		pos = end + 1
	}
	return -1, -1
}

// fhFencedEnd is _fenced(text): (inner, found, rest_start); rest_start is -1 for an unclosed fence (the rest).
func fhFencedEnd(text string) (string, bool, int) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		m := fhFence.FindStringSubmatch(line)
		if m == nil {
			if py.Strip(line) != "" && i > 3 {
				return "", false, -1
			}
			continue
		}
		ch, n := m[1][0], len(m[1])
		closeAt := -1
		for j := i + 1; j < len(lines); j++ {
			c := fhFenceClose.FindStringSubmatch(lines[j])
			if c != nil && c[1][0] == ch && len(c[1]) >= n {
				closeAt = j
				if ch == '~' {
					break
				}
			}
		}
		if closeAt < 0 {
			return strings.Trim(strings.Join(lines[i+1:], "\n"), "\n"), true, -1
		}
		off := 0
		for _, x := range lines[:closeAt+1] {
			off += len(x) + 1
		}
		return strings.Trim(strings.Join(lines[i+1:closeAt], "\n"), "\n"), true, off
	}
	return "", false, -1
}

// fhNextH2 is _next_h2(md, start): the offset of the next "## " heading at or after start outside fences.
func fhNextH2(md string, start int) int {
	pos := start
	fence := ""
	for _, line := range strings.Split(md[start:], "\n") {
		m := fhFenceOpen3.FindStringSubmatch(line)
		if fence != "" {
			if m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.Trim(line, " \t`~") == "" {
				fence = ""
			}
		} else if m != nil {
			fence = m[1]
		} else if strings.HasPrefix(line, "## ") {
			return pos
		}
		pos += len(line) + 1
	}
	return len(md)
}

var fhFenceOpen3 = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})")

type fhSpan struct {
	start, stop int
	kind        string
	inner       string
	headEnd     int
}

// fhSpans is _spans(md).
func fhSpans(md string) []fhSpan {
	aS, aE := fhHeadSearch(fhHeadA, md)
	bS, bE := fhHeadSearch(fhHeadB, md)
	var out []fhSpan
	if bS >= 0 {
		rest := md[bE:]
		start := bE + len(rest) - len(strings.TrimLeft(rest, "\n"))
		inner, ok, end := fhFencedEnd(md[start:])
		var stop int
		if !ok {
			stop = fhNextH2(md, bE)
			inner = md[bE:stop]
		} else if end >= 0 {
			stop = start + end
		} else {
			stop = len(md)
		}
		if stop > len(md) {
			stop = len(md)
		}
		out = append(out, fhSpan{bS, stop, "B", inner, bE})
	}
	if aS >= 0 {
		stop := 0
		if bS >= 0 && bS > aS {
			stop = bS
		} else {
			stop = fhNextH2(md, aE)
		}
		out = append(out, fhSpan{aS, stop, "A", "", aE})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].start != out[j].start {
			return out[i].start < out[j].start
		}
		return out[i].stop < out[j].stop
	})
	return out
}

// FHSplit is split(md): (body, options_md, prompt_md); text after the prompt's closing fence stays in the body.
func FHSplit(md string) (string, string, string) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	opts, prompt, pos := "", "", 0
	var keep []string
	for _, sp := range fhSpans(md) {
		if sp.start < pos {
			continue
		}
		keep = append(keep, md[pos:sp.start])
		if sp.kind == "B" {
			prompt = py.Strip(sp.inner)
		} else {
			opts = py.Strip(md[sp.headEnd:sp.stop])
		}
		pos = sp.stop
	}
	keep = append(keep, md[pos:])
	var parts []string
	for _, k := range keep {
		if py.Strip(k) != "" {
			parts = append(parts, strings.Trim(k, "\n"))
		}
	}
	return py.RStrip(strings.Join(parts, "\n\n")), opts, prompt
}

// FHHasSections is has_sections(md).
func FHHasSections(md string) bool {
	a, _ := fhHeadSearch(fhHeadA, md)
	b, _ := fhHeadSearch(fhHeadB, md)
	return a >= 0 || b >= 0
}

// FHApprovalKind is approval_kind(text, env).
func FHApprovalKind(text any, env string) string {
	t := strings.ToLower(py.OrS(text, ""))
	e := FHNormEnv(env)
	switch {
	case e == "release" || strings.Contains(t, "production"):
		return "production"
	case e == "pre-release" || strings.Contains(t, "operator"):
		return "operator"
	case e == "develop" || strings.Contains(t, "fixer"):
		return "fixer"
	}
	return "operator"
}

// fhRecommended finds the case-insensitive word "recommended" with word boundaries on both sides, from byte pos.
func fhRecommended(t string, pos int) (int, int) {
	for s := pos; s+11 <= len(t); s++ {
		if (t[s] == 'r' || t[s] == 'R') && strings.EqualFold(t[s:s+11], "recommended") && !fhWordBefore(t, s) && !fhWordAt(t, s+11) {
			return s, s + 11
		}
	}
	return -1, -1
}

func fhIsSpace(r rune) bool { return py.IsSpaceRune(r) }

// fhCleanTitle is _clean_title(t): (title, recommended).
func fhCleanTitle(t string) (string, bool) {
	t = py.Strip(reStarTick.ReplaceAllString(t, ""))
	t = fhOptionPfx.ReplaceAllString(t, "")
	t = fhNumPfx.ReplaceAllString(t, "")
	s, _ := fhRecommended(t, 0)
	rec := s >= 0
	// re.sub(r"\s*[(\[]?\brecommended\b[)\]]?\s*", " ", t, flags=re.I)
	var b strings.Builder
	last := 0
	for {
		s, e := fhRecommended(t, last)
		if s < 0 {
			break
		}
		ms := s
		if ms > last {
			if c := t[ms-1]; c == '(' || c == '[' {
				ms--
			}
		}
		for ms > last {
			r, w := utf8.DecodeLastRuneInString(t[:ms])
			if !fhIsSpace(r) {
				break
			}
			ms -= w
		}
		me := e
		if me < len(t) && (t[me] == ')' || t[me] == ']') {
			me++
		}
		for me < len(t) {
			r, w := utf8.DecodeRuneInString(t[me:])
			if !fhIsSpace(r) {
				break
			}
			me += w
		}
		b.WriteString(t[last:ms])
		b.WriteString(" ")
		last = me
		if me == s { // cannot happen ("recommended" is consumed); guards an empty match loop
			last++
		}
	}
	b.WriteString(t[last:])
	t = strings.Trim(b.String(), " -—–:")
	t = fhKindSuffix.ReplaceAllString(t, "")
	return py.Head(t, 200), rec
}

type fhRaw struct {
	m   *py.OMap // title, what, why, ... (str)
	rec bool
}

// fhTableOptions is _table_options(text).
func fhTableOptions(text string) []fhRaw {
	var rows []string
	for _, r := range strings.Split(text, "\n") {
		if strings.HasPrefix(py.Strip(r), "|") {
			rows = append(rows, r)
		}
	}
	if len(rows) < 3 {
		return nil
	}
	cells := func(r string) []string {
		parts := strings.Split(strings.Trim(py.Strip(r), "|"), "|")
		for i := range parts {
			parts[i] = py.Strip(parts[i])
		}
		return parts
	}
	var head []string
	for _, c := range cells(rows[0]) {
		head = append(head, strings.ToLower(c))
	}
	any_ := false
	for _, h := range head {
		if strings.Contains(h, "option") {
			any_ = true
		}
	}
	if !any_ {
		return nil
	}
	col := py.NewOMap() // header -> its last index, in first-seen order
	for i, h := range head {
		col.Set(h, i)
	}
	var out []fhRaw
	for _, r := range rows[2:] {
		c := cells(r)
		get := func(names ...string) string {
			for _, h := range col.Keys() {
				idx := col.Get(h).(int)
				for _, nm := range names {
					if strings.Contains(h, nm) && idx < len(c) {
						return c[idx]
					}
				}
			}
			return ""
		}
		out = append(out, fhRaw{m: py.NewOMap("title", get("option"), "what", get("option"), "why", get("fixes", "frees", "why"),
			"risk", get("risk"), "approval", get("approv"), "effort", get("effort"), "blast_radius", get("blast"),
			"kind", get("kind"))})
	}
	return out
}

// FHParseOptions is parse_options(text, env).
func FHParseOptions(text, env string) []*py.OMap {
	text = py.Strip(text)
	var raw []fhRaw
	heads := fhOptHead.FindAllStringSubmatchIndex(text, -1)
	for i, h := range heads {
		end := len(text)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		block := text[h[1]:end]
		title, rec := fhCleanTitle(text[h[2]:h[3]])
		o := fhRaw{m: py.NewOMap("title", title), rec: rec}
		cur := ""
		for _, line := range strings.Split(block, "\n") {
			m := fhField.FindStringSubmatch(line)
			key := ""
			if m != nil {
				key = strings.ToLower(py.Strip(m[1]))
			}
			if k, ok := fhKeys[key]; m != nil && ok {
				cur = k
				o.m.Set(cur, py.Strip(m[2]))
			} else if m != nil && key == "recommended" {
				v := strings.ToLower(py.Strip(m[2]))
				o.rec = o.rec || strings.HasPrefix(v, "yes") || strings.HasPrefix(v, "true")
				cur = ""
			} else if cur != "" && py.Strip(line) != "" && !(strings.HasPrefix(py.LStrip(line), "- **") || strings.HasPrefix(py.LStrip(line), "* **")) {
				o.m.Set(cur, py.Strip(o.m.S(cur)+" "+py.Strip(line)))
			} else if py.Strip(line) != "" && !o.m.Has("what") && cur == "" {
				o.m.Set("what", strings.TrimLeft(py.Strip(line), "-* "))
			}
		}
		raw = append(raw, o)
	}
	if len(raw) == 0 {
		raw = fhTableOptions(text)
	}
	if len(raw) == 0 {
		for _, m := range fhNumbered.FindAllStringSubmatch(text, -1) {
			title, rec := fhCleanTitle(m[2])
			raw = append(raw, fhRaw{m: py.NewOMap("title", py.Head(title, 120), "what", title), rec: rec})
		}
	}
	var out []*py.OMap
	for i, o := range raw {
		if i >= FHMaxOptions {
			break
		}
		n := int64(i + 1)
		t := o.m.S("title")
		if t == "" {
			t = o.m.S("what")
		}
		if t == "" {
			t = "Option " + strconv.FormatInt(n, 10)
		}
		opt := py.NewOMap("n", n, "title", py.Head(t, 200))
		for _, k := range []string{"kind", "what", "why", "effort", "risk", "blast_radius", "approval"} {
			opt.Set(k, py.Head(py.Strip(fhWSRun.ReplaceAllString(o.m.S(k), " ")), FHMaxField))
		}
		appr := opt.S("approval")
		opt.Set("approval_kind", FHApprovalKind(appr, env))
		if FHNormEnv(env) == "release" {
			if !strings.Contains(strings.ToUpper(appr), "PRODUCTION") {
				s := "PRODUCTION CHANGE: the operator approves"
				if appr != "" {
					s += " (" + appr + ")"
				}
				opt.Set("approval", py.Head(s, FHMaxField))
			}
		}
		opt.Set("recommended", o.rec)
		out = append(out, opt)
	}
	seen := false
	for _, o := range out {
		if o.Get("recommended") == true {
			if seen {
				o.Set("recommended", false)
			}
			seen = true
		}
	}
	return out
}

// FHCheckPrompt is check_prompt(prompt, env) without a redactor: (prompt, flags).
func FHCheckPrompt(prompt, env string) (string, []string) {
	var flags []string
	p := py.Strip(strings.ReplaceAll(prompt, "\r\n", "\n"))
	if p == "" {
		return "", flags
	}
	if FHNormEnv(env) == "release" {
		lines := strings.Split(p, "\n")
		first := 0
		for i, x := range lines {
			if py.Strip(x) != "" {
				first = i
				break
			}
		}
		if py.Strip(lines[first]) != FHProdLine {
			if loc := fhProdRX.FindStringIndex(lines[first]); loc != nil && !fhWordAt(lines[first], loc[1]) {
				lines = append(lines[:first:first], lines[first+1:]...)
			}
			p = FHProdLine + "\n" + strings.TrimLeft(strings.Join(lines, "\n"), "\n")
			flags = append(flags, "production line added")
		}
	}
	if py.Len(p) > FHMaxPrompt {
		p = py.RStrip(py.Head(p, FHMaxPrompt-40)) + "\n\n[cut at 32,000 characters]"
		flags = append(flags, "prompt cut")
	}
	return p, flags
}

// FHParse is fix_handoff.parse(md, env, fallback_prompt).
func FHParse(md, env, fallbackPrompt string) Handoff {
	body, optsMD, prompt := FHSplit(md)
	var flags []string
	options := FHParseOptions(optsMD, env)
	if len(options) > 0 {
		any_ := false
		for _, o := range options {
			if o.Get("recommended") == true {
				any_ = true
			}
		}
		if !any_ {
			options[0].Set("recommended", true)
			flags = append(flags, "no option marked recommended: the first is shown as recommended")
		}
	}
	if prompt == "" && py.Strip(fallbackPrompt) != "" {
		if s, _ := fhHeadSearch(fhHeadB, md); s < 0 {
			prompt = py.Strip(fallbackPrompt)
			flags = append(flags, "prompt from the fix_prompt field")
		}
	}
	prompt, pflags := FHCheckPrompt(prompt, env)
	flags = append(flags, pflags...)
	state := "missing"
	if len(options) > 0 && prompt != "" {
		state = "ok"
	} else if len(options) > 0 || prompt != "" {
		state = "partial"
	}
	return Handoff{Options: options, Prompt: prompt, State: state, Flags: flags, Body: body}
}

// FHLoadOptions is load_options(text): stored fix_options -> option dicts; anything unreadable -> [].
func FHLoadOptions(v any) []*py.OMap {
	var text string
	switch x := v.(type) {
	case nil:
		text = ""
	case string:
		text = x
	default:
		return nil // json.loads of a number raises TypeError: []
	}
	if text == "" {
		text = "[]"
	}
	parsed, err := py.ParseJSON([]byte(text))
	if err != nil {
		return nil
	}
	var out []*py.OMap
	for _, o := range py.AsList(parsed) {
		if m := py.AsMap(o); m != nil {
			out = append(out, m)
		}
	}
	if len(out) > FHMaxOptions {
		out = out[:FHMaxOptions]
	}
	return out
}
