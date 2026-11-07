package app

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// Python's \s in a str pattern is Unicode whitespace and \d any decimal digit; RE2's are ASCII. These classes
// reproduce Python's so the markdown renders the same.
const (
	wsClass  = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\p{Z}`
	reWS     = `[` + wsClass + `]`
	reDigit  = `\p{Nd}`
	reNotWS_ = `[^` + wsClass + `&<]`
)

var (
	reTableSep = regexp.MustCompile(`^` + reWS + `*\|?(` + reWS + `*:?-{3,}:?` + reWS + `*\|)+` + reWS + `*(:?-{3,}:?` + reWS + `*)?$`)
	reVerdict  = regexp.MustCompile(`(?i)^(?:\*\*)?(proven|probable|unproven|not proven)(?:\*\*)?(.*)$`)
	reCode     = regexp.MustCompile("`([^`]+)`")
	reStrong   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reLink     = regexp.MustCompile(`(https?://` + reNotWS_ + `+)`)
	reHead     = regexp.MustCompile(`^(#{1,4})` + reWS + `+(.*)`)
	reItem     = regexp.MustCompile(`^` + reWS + `*(?:[-*]|` + reDigit + `+\.)` + reWS + `+(.*)`)
)

// mdInline is md()'s inline(): code, bold, links on already escaped text.
func mdInline(line string) string {
	line = reCode.ReplaceAllString(line, "<code>${1}</code>")
	line = reStrong.ReplaceAllString(line, "<strong>${1}</strong>")
	return reLink.ReplaceAllString(line, `<a href="${1}" rel="noopener noreferrer">${1}</a>`)
}

// mdCells is _md_cells(raw): split a table row on | outside `code` spans; \| is a literal pipe.
func mdCells(raw string) []string {
	s := strings.Trim(py.Strip(raw), "|")
	var cells []string
	var cur []rune
	code := false
	var prev rune
	for _, ch := range s {
		if ch == '`' {
			code = !code
		}
		if ch == '|' && !code && prev != '\\' {
			cells = append(cells, string(cur))
			cur = cur[:0:0]
		} else if ch == '|' && prev == '\\' {
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
			cur = append(cur, ch)
		} else {
			cur = append(cur, ch)
		}
		prev = ch
	}
	cells = append(cells, string(cur))
	for i := range cells {
		cells[i] = py.Strip(cells[i])
	}
	return cells
}

func mdTable(head, sep string, body []string) string {
	var aligns []string
	for _, c := range mdCells(sep) {
		if strings.HasSuffix(c, ":") && !strings.HasPrefix(c, ":") {
			aligns = append(aligns, "num")
		} else {
			aligns = append(aligns, "")
		}
	}
	cell := func(tag, text string, n int) string {
		cls := ""
		if n < len(aligns) && aligns[n] != "" {
			cls = ` class="` + aligns[n] + `"`
		}
		if tag == "td" {
			if v := reVerdict.FindStringSubmatch(text); v != nil {
				kind := "warn"
				if strings.ToLower(v[1]) == "proven" {
					kind = "ok"
				}
				return "<" + tag + cls + `><span class="badge ` + kind + `">` + E(strings.ToLower(v[1])) + `</span>` +
					mdInline(E(v[2])) + "</" + tag + ">"
			}
		}
		return "<" + tag + cls + ">" + mdInline(E(text)) + "</" + tag + ">"
	}
	hc := mdCells(head)
	width := len(hc)
	var b strings.Builder
	b.WriteString(`<div class="tablewrap md-table"><table><thead><tr>`)
	for n, c := range hc {
		b.WriteString(cell("th", c, n))
	}
	b.WriteString("</tr></thead><tbody>")
	for _, r := range body {
		cells := mdCells(r)
		for len(cells) < width {
			cells = append(cells, "")
		}
		cells = cells[:width]
		b.WriteString("<tr>")
		for n, c := range cells {
			b.WriteString(cell("td", c, n))
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table></div>")
	return b.String()
}

// Md is md(text, ids): a small markdown subset. Escapes first, then formats, so no HTML from the source
// survives. ids numbers the headings (id="s1", ...) for MdOutline.
func Md(text any, ids ...bool) string { return MdH(text, len(ids) > 0 && ids[0], "fold") }

var (
	reFenceOpen  = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})(.*)$")
	reFenceClose = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})[ \t]*$")
)

type mdFence struct {
	ch byte
	n  int
}

// mdFenceOpen is _fence_open(raw): ``` or ~~~ (3+), an optional info string (no backtick after backticks).
func mdFenceOpen(raw string) *mdFence {
	m := reFenceOpen.FindStringSubmatch(raw)
	if m == nil || (m[1][0] == '`' && strings.Contains(m[2], "`")) {
		return nil
	}
	return &mdFence{m[1][0], len(m[1])}
}

// mdFenceClose is _fence_close(raw, fence).
func mdFenceClose(raw string, f *mdFence) bool {
	m := reFenceClose.FindStringSubmatch(raw)
	return m != nil && m[1][0] == f.ch && len(m[1]) >= f.n
}

// mdHandoff is _handoff(text, mode): (text without the fix handoff sections, html to append).
func mdHandoff(text, mode string) (string, string) {
	if mode == "keep" || !FHHasSections(text) {
		return text, ""
	}
	body, opts, prompt := FHSplit(text)
	if mode == "strip" || (opts == "" && prompt == "") {
		return body, ""
	}
	inner := ""
	if opts != "" {
		inner = MdH(opts, false, "keep")
	}
	if prompt != "" {
		inner += `<div class="codeblock"><div class="codeblock-h"><b>Prompt for a write-access agent</b></div>` +
			`<pre class="prompt"><code>` + E(prompt) + `</code></pre></div>`
	}
	return body, `<details class="report handoff-fold"><summary>How to fix it and the prompt for a write-access ` +
		`agent</summary>` + inner + `</details>`
}

// MdH is md(text, ids, handoff): handoff "fold" (default), "strip" (the page shows the fix cards) or "keep".
func MdH(text any, withIDs bool, handoff string) string {
	t, tail := mdHandoff(strings.ReplaceAll(py.OrS(text, ""), "\r\n", "\n"), handoff)
	var out, para []string
	var fence *mdFence
	inList := false
	hn := 0
	flush := func() {
		if len(para) > 0 {
			out = append(out, "<p>"+mdInline(strings.Join(para, " "))+"</p>")
			para = para[:0]
		}
		if inList {
			out = append(out, "</ul>")
			inList = false
		}
	}
	lines := py.SplitLines(t)
	i := 0
	for i < len(lines) {
		raw := lines[i]
		i++
		line := E(raw)
		if fence != nil {
			if mdFenceClose(raw, fence) {
				out = append(out, "</code></pre>")
				fence = nil
			} else {
				out = append(out, line)
			}
			continue
		}
		if strings.HasPrefix(py.Strip(raw), "|") && i < len(lines) && reTableSep.MatchString(lines[i]) {
			flush()
			head, sep := raw, lines[i]
			var body []string
			i++
			for i < len(lines) && strings.HasPrefix(py.Strip(lines[i]), "|") {
				body = append(body, lines[i])
				i++
			}
			out = append(out, mdTable(head, sep, body))
			continue
		}
		if f := mdFenceOpen(raw); f != nil {
			flush()
			out = append(out, "<pre><code>")
			fence = f
			continue
		}
		head := reHead.FindStringSubmatch(line)
		item := reItem.FindStringSubmatch(line)
		if head != nil || py.Strip(line) == "" {
			flush()
			if head != nil {
				lvl := len(head[1]) + 1
				if lvl > 5 {
					lvl = 5
				}
				hn++
				hid := ""
				if withIDs {
					hid = ` id="s` + strconv.Itoa(hn) + `"`
				}
				l := strconv.Itoa(lvl)
				out = append(out, "<h"+l+hid+">"+mdInline(head[2])+"</h"+l+">")
			}
		} else if item != nil {
			if len(para) > 0 {
				out = append(out, "<p>"+mdInline(strings.Join(para, " "))+"</p>")
				para = para[:0]
			}
			if !inList {
				out = append(out, "<ul>")
				inList = true
			}
			out = append(out, "<li>"+mdInline(item[1])+"</li>")
		} else if inList && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) {
			last := out[len(out)-1]
			out[len(out)-1] = last[:len(last)-5] + " " + mdInline(py.Strip(line)) + "</li>"
		} else {
			if inList {
				out = append(out, "</ul>")
				inList = false
			}
			para = append(para, py.Strip(line))
		}
	}
	flush()
	if fence != nil {
		out = append(out, "</code></pre>")
	}
	if tail != "" {
		out = append(out, tail)
	}
	return strings.Join(out, "\n")
}

// OutlineItem is one (anchor, text) of MdOutline.
type OutlineItem struct{ Anchor, Text string }

// MdOutlineH is md_outline(text, handoff): same fences and handoff handling as MdH, so the anchors match.
func MdOutlineH(text any, handoff string) []OutlineItem {
	t, _ := mdHandoff(strings.ReplaceAll(py.OrS(text, ""), "\r\n", "\n"), handoff)
	var out []OutlineItem
	hn := 0
	var fence *mdFence
	for _, raw := range py.SplitLines(t) {
		if fence != nil {
			if mdFenceClose(raw, fence) {
				fence = nil
			}
			continue
		}
		if f := mdFenceOpen(raw); f != nil {
			fence = f
			continue
		}
		if head := reHead.FindStringSubmatch(E(raw)); head != nil {
			hn++
			if len(head[1]) == 2 {
				out = append(out, OutlineItem{"s" + strconv.Itoa(hn), py.Strip(reMarks.ReplaceAllString(head[2], ""))})
			}
		}
	}
	return out
}
