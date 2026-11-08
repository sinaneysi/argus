// Package notify builds and sends Argus's chat notices (the port of the reference installation's notification
// formatter and sender).
//
// Every message has the same shape, so a phone glance answers what happened, how bad and where to click:
//
//	🔴 FAIL · Backups                      status emoji + bold title
//	3 of 127 backup sets are failing.      one sentence
//	• db-orders: stale, newest 52 h old    at most MaxFacts short facts
//	Open backups ↗ · 1m 12s · $0.31        link to the web page + grey meta
//
// An OK scheduled check is one line: "🟢 Backups OK · 127 sets · next 07:00".
//
// FAIL and ERROR go out as m.text so the chat client notifies the room; OK, WARN, INFO, settings changes and
// watcher notices go out as m.notice, which the default push rules keep silent. Untrusted text can never ping:
// "@room" is defused and m.mentions is always empty.
//
// Everything that came from logs, chat, alerts or a model is untrusted: it is stripped of control and bidi-override
// characters, token-looking strings are redacted, it is capped, and it is HTML-escaped in formatted_body. The only
// HTML emitted is b, br, ul/li, a and font data-mx-color. Links are built from the configured base URL, fixed paths
// and integers.
package notify

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxFacts is the number of fact lines a message shows; the rest is "+N more".
const MaxFacts = 5

const grey = "#8a8a8a"

type level struct{ emoji, msgtype string }

var levels = map[string]level{
	"OK":    {"🟢", "m.notice"},
	"WARN":  {"🟠", "m.notice"},
	"FAIL":  {"🔴", "m.text"},
	"ERROR": {"🔴", "m.text"},
	"INFO":  {"🔵", "m.notice"},
}

// Titles are the message titles of the known sources; any other source is its own title.
var Titles = map[string]string{"backup-check": "Backups", "cert-check": "Certificates", "log-rca": "Alert RCA",
	"chat-triage": "Chat triage", "incident": "Incident", "admin": "Settings", "ops-watch": "Alert watcher",
	"setup": "Setup", "selfcheck": "Health", "rca-deep": "Deep RCA", "disk-explain": "Storage analysis",
	"vm-explain": "VM analysis", "cicd": "CI/CD", "cred": "Credentials"}

// Pages are the web pages the messages of a source link to (default "/").
var Pages = map[string]string{"backup-check": "/backups", "cert-check": "/certs", "log-rca": "/alerts",
	"chat-triage": "/incidents", "incident": "/incidents", "admin": "/admin", "ops-watch": "/alerts", "setup": "/",
	"rca-deep": "/rca", "disk-explain": "/capacity", "vm-explain": "/inventory"}

// Content is one chat message: msgtype, body, format, formatted_body. It marshals with its keys in that order and
// non-ASCII text as is, like the reference formatter's output.
type Content struct {
	MsgType       string
	Body          string
	Format        string
	FormattedBody string
}

// MarshalJSON writes {"msgtype": .., "body": .., "format": .., "formatted_body": ..} (format and formatted_body only
// when set), with ", " and ": " separators.
func (c Content) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"msgtype": `)
	writeStr(&b, c.MsgType)
	b.WriteString(`, "body": `)
	writeStr(&b, c.Body)
	if c.Format != "" {
		b.WriteString(`, "format": `)
		writeStr(&b, c.Format)
		b.WriteString(`, "formatted_body": `)
		writeStr(&b, c.FormattedBody)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads a message; unknown fields are ignored.
func (c *Content) UnmarshalJSON(data []byte) error {
	var m struct {
		MsgType       any `json:"msgtype"`
		Body          any `json:"body"`
		Format        any `json:"format"`
		FormattedBody any `json:"formatted_body"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	s := func(v any) string {
		switch x := v.(type) {
		case string:
			return x
		case nil:
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	*c = Content{MsgType: s(m.MsgType), Body: s(m.Body), Format: s(m.Format), FormattedBody: s(m.FormattedBody)}
	return nil
}

// writeStr is a JSON string like Python's json.dumps(ensure_ascii=False): only quote, backslash and control
// characters are escaped.
func writeStr(b *bytes.Buffer, s string) {
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
			if r < 0x20 || r == utf8.RuneError {
				if r == utf8.RuneError {
					b.WriteString(`�`)
					continue
				}
				b.WriteString(`\u00`)
				b.WriteString(strconv.FormatInt(int64(r)>>4, 16))
				b.WriteString(strconv.FormatInt(int64(r)&15, 16))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// --- cleaning untrusted text -----------------------------------------------------------------------------------------

var (
	reSecret = regexp.MustCompile(`(?i)\b(glpat-|gldt-|ghp_|github_pat_|syt_|xox[abp]-|sk-ant-)[A-Za-z0-9_\-]{6,}` +
		`|\b(bearer)\s+[A-Za-z0-9._\-]{12,}` +
		`|\b([A-Z0-9_]*(?:password|passwd|token|secret|api_?key)[A-Z0-9_]*)\s*[=:]\s*\S+`)
	reRoom   = regexp.MustCompile(`(?i)@room\b`)
	reStatus = regexp.MustCompile(`^\s*\**\s*(OK|WARN|FAIL|ERROR)\s*\**\s*[:\-–—]?\s*`)
	reMark   = regexp.MustCompile("\\*\\*|__|`")
	reFirst  = regexp.MustCompile(`^(.+?[.!?])(\s|$)`)
	reWord   = regexp.MustCompile(`^\s*\**\s*(OK|WARN|FAIL|ERROR)\b`)
)

func isBidi(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

func runeHead(s string, n int) string {
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

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// collapse replaces every run of white space by one space and trims both ends.
func collapse(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// Clean makes an untrusted value one safe line of plain text (not yet HTML-escaped), at most cap characters.
func Clean(value string, cap int) string {
	s := runeHead(value, cap*4)
	var b strings.Builder
	for _, r := range s {
		switch {
		case isBidi(r):
		case unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s = b.String()
	s = replaceSub(reSecret, s, func(m []string) string {
		switch {
		case m[1] != "":
			return m[1] + "[redacted]"
		case m[2] != "":
			return m[2] + " [redacted]"
		}
		return m[3] + "=[redacted]"
	})
	s = reRoom.ReplaceAllString(s, "@⁠room")
	s = collapse(s)
	if runeLen(s) <= cap {
		return s
	}
	return strings.TrimRightFunc(runeHead(s, cap-1), unicode.IsSpace) + "…"
}

func replaceSub(re *regexp.Regexp, s string, f func(m []string) string) string {
	return re.ReplaceAllStringFunc(s, func(x string) string { return f(re.FindStringSubmatch(x)) })
}

// Escape is HTML escaping with quotes, like Python's html.escape(s, quote=True).
func Escape(s string) string {
	if !strings.ContainsAny(s, `&<>"'`) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
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
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FirstSentence is the first sentence of a text, without a leading status word and markdown.
func FirstSentence(text string, cap int) string {
	s := reStatus.ReplaceAllString(text, "")
	s, _, _ = strings.Cut(s, "\n")
	s = reMark.ReplaceAllString(s, "")
	if m := reFirst.FindStringSubmatch(s); m != nil {
		return Clean(m[1], cap)
	}
	return Clean(s, cap)
}

// StatusWord is the OK/WARN/FAIL/ERROR a text starts with, or "".
func StatusWord(text string) string {
	if m := reWord.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// --- messages --------------------------------------------------------------------------------------------------------

// Message is the card: level, title, one-sentence summary, facts, a link and grey meta.
type Message struct {
	Level, Title, Summary string
	Facts                 []string
	Link, LinkText        string
	Meta                  []string
	More                  int // facts not shown, besides those beyond MaxFacts
	Prefix, Emoji         string
}

func (m Message) norm() (Message, level) {
	lv, ok := levels[m.Level]
	if !ok {
		m.Level, lv = "WARN", levels["WARN"]
	}
	if len(m.Facts) > MaxFacts {
		m.More += len(m.Facts) - MaxFacts
		m.Facts = m.Facts[:MaxFacts]
	}
	if m.LinkText == "" {
		m.LinkText = "Open"
	}
	var meta []string
	for _, x := range m.Meta {
		if x != "" {
			meta = append(meta, x)
		}
	}
	m.Meta = meta
	return m, lv
}

func capStr(s string, n int) string { return runeHead(s, n) }

// Render is the message as chat content.
func (m Message) Render() Content {
	m, lv := m.norm()
	emoji := lv.emoji
	if m.Emoji != "" {
		emoji = m.Emoji
	}
	head := emoji + " " + m.Level + " · " + Clean(m.Title, 80)
	preT, preH := "", ""
	if m.Prefix != "" {
		preT, preH = m.Prefix+" ", Escape(m.Prefix)+" "
	}
	body := []string{preT + head}
	out := []string{preH + "<b>" + Escape(head) + "</b>"}
	if summary := Clean(m.Summary, 240); summary != "" {
		r, n := utf8.DecodeRuneInString(summary)
		summary = strings.ToUpper(string(r)) + summary[n:]
		body = append(body, summary)
		out = append(out, Escape(summary))
	}
	var facts []string
	for _, f := range m.Facts {
		facts = append(facts, Clean(f, 160))
	}
	if m.More > 0 {
		facts = append(facts, "+"+strconv.Itoa(m.More)+" more")
	}
	var tail []string
	if m.Link != "" {
		tail = append(tail, m.Link)
	}
	tailT := strings.Join(append(tail, m.Meta...), " · ")
	metaH := Escape(strings.Join(m.Meta, " · "))
	linkH := ""
	if m.Link != "" {
		linkH = `<a href="` + Escape(m.Link) + `">` + Escape(m.LinkText) + " ↗</a>"
	}
	tailH := ""
	switch {
	case linkH != "" && metaH != "":
		tailH = linkH + ` <font data-mx-color="` + grey + `">· ` + metaH + "</font>"
	case metaH != "":
		tailH = `<font data-mx-color="` + grey + `">` + metaH + "</font>"
	default:
		tailH = linkH
	}
	for _, f := range facts {
		body = append(body, "• "+f)
	}
	if tailT != "" {
		body = append(body, tailT)
	}
	fbody := strings.Join(out, "<br>")
	if len(facts) > 0 {
		fbody += "<ul>"
		for _, f := range facts {
			fbody += "<li>" + Escape(f) + "</li>"
		}
		fbody += "</ul>"
	} else if tailH != "" {
		fbody += "<br>"
	}
	fbody += tailH
	return Content{MsgType: lv.msgtype, Body: capStr(strings.Join(body, "\n"), 4000), Format: "org.matrix.custom.html",
		FormattedBody: capStr(fbody, 12000)}
}

// RenderOneLine is an OK result as one line: "🟢 Backups OK · 127 sets · next 07:00"; the title links to the page.
func (m Message) RenderOneLine() Content {
	m, lv := m.norm()
	preT, preH := "", ""
	if m.Prefix != "" {
		preT, preH = m.Prefix+" ", Escape(m.Prefix)+" "
	}
	var bits, facts []string
	for _, f := range m.Facts {
		facts = append(facts, Clean(f, 60))
	}
	bits = append(append(bits, facts...), m.Meta...)
	title := Clean(m.Title, 60)
	body := preT + lv.emoji + " " + title + " " + m.Level
	for _, b := range bits {
		body += " · " + b
	}
	t := Escape(title)
	if m.Link != "" {
		t = `<a href="` + Escape(m.Link) + `">` + t + "</a>"
	}
	fb := preH + lv.emoji + " <b>" + t + " " + m.Level + "</b>"
	for _, f := range facts {
		fb += " · " + Escape(f)
	}
	if len(m.Meta) > 0 {
		fb += ` <font data-mx-color="` + grey + `">· ` + Escape(strings.Join(m.Meta, " · ")) + "</font>"
	}
	return Content{MsgType: lv.msgtype, Body: capStr(body, 4000), Format: "org.matrix.custom.html", FormattedBody: capStr(fb, 4000)}
}

// Formatter builds the messages; Base is the web app's address (ARGUS_PUBLIC_URL) the links start with.
type Formatter struct {
	Base string
}

// Page is a web address from a fixed path and integer query values only, in the given order.
func (f Formatter) Page(path string, q ...QueryInt) string {
	s := strings.TrimRight(f.Base, "/") + path
	for i, x := range q {
		if i == 0 {
			s += "?"
		} else {
			s += "&"
		}
		s += x.Key + "=" + strconv.FormatInt(x.Val, 10)
	}
	return s
}

// QueryInt is one integer query value of a link.
type QueryInt struct {
	Key string
	Val int64
}

func title(source string) string {
	if t, ok := Titles[source]; ok {
		return t
	}
	return source
}

func pageOf(source, def string) string {
	if p, ok := Pages[source]; ok {
		return p
	}
	return def
}

// Text wraps free text from a job: its status word is the level (INFO without one), its first sentence the summary.
func (f Formatter) Text(source, text, prefix string) Content {
	lv := StatusWord(text)
	if lv == "" {
		lv = "INFO"
	}
	return Message{Level: lv, Title: title(source), Summary: FirstSentence(text, 300), Link: f.Page(pageOf(source, "/")),
		LinkText: "Open " + appName, Prefix: prefix}.Render()
}

// appName is the product name in the "Open …" link of free-text notices (SetAppName).
var appName = "Argus"

// SetAppName sets the product name shown in the "Open …" link of free-text notices (ARGUS_DISPLAY_NAME).
func SetAppName(name string) {
	if name = strings.TrimSpace(name); name != "" {
		appName = name
	}
}

// Settings is the notice for a saved settings change.
func (f Formatter) Settings(by string, changes []string, prefix string) Content {
	var cs []string
	for _, c := range changes {
		cs = append(cs, Clean(c, 80))
	}
	s := "s"
	if len(cs) == 1 {
		s = ""
	}
	return Message{Level: "INFO", Title: "Settings changed", Summary: strconv.Itoa(len(cs)) + " setting" + s +
		" changed by " + Clean(by, 40) + ".", Facts: cs, Link: f.Page("/admin"), LinkText: "Open admin", Prefix: prefix}.Render()
}

// Ratelimit is the notice of the alert watcher's rate limit.
func (f Formatter) Ratelimit(perHour, perDay int64, dropped []string, prefix string) Content {
	var facts []string
	for _, d := range dropped {
		facts = append(facts, "dropped: "+Clean(d, 100))
	}
	return Message{Level: "WARN", Title: "Alert watcher rate limit", Summary: "Limit of " + strconv.FormatInt(perHour, 10) +
		"/hour, " + strconv.FormatInt(perDay, 10) + "/day reached: new alerts are dropped, not investigated, until it clears.",
		Facts: facts, Link: f.Page("/alerts"), LinkText: "Open alerts", Prefix: prefix}.Render()
}
