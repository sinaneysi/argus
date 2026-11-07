package app

// Help: Argus's user documentation, Go only (no Python counterpart; tests/parity masks the help markup).
// The Markdown lives in helpdocs/*.md (docs/user in the repository root is a link to it) and is embedded in the
// binary, so it ships with the image. Pages: /help (index and search), /help/<slug>, /help/all (printable).
//
// Format of a help file:
//
//	---
//	title: Operations
//	summary: one line
//	order: 2
//	audience: admin          (optional: the whole page is for admins)
//	---
//	intro text
//	## Section title {#id}   (a section; {admin} marks it admins-only; the id defaults to a slug of the title)
//	### Sub heading {#id}
//	:::details Question      (a folded block, closed by :::)
//	:::admin                 (rendered for admins only, closed by :::)
//	[label](/path)           (an internal link: a local path only)
//
// Everything else is the escaping-first Md() subset. No HTML from the files survives.

import (
	"embed"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

//go:embed helpdocs/*.md
var helpFS embed.FS

const (
	helpMaxQuery = 100
	helpMaxHits  = 30
)

type helpNode struct {
	kind string // md | h3 | details | admin
	text string // markdown, the h3 title or the details summary
	id   string
	kids []*helpNode
}

type helpSection struct {
	ID, Title string
	Admin     bool
	Nodes     []*helpNode
}

// HelpDoc is one help page.
type HelpDoc struct {
	Slug, Title, Summary string
	Order                int
	Admin                bool
	Intro                []*helpNode
	Sections             []*helpSection
	ids                  map[string]bool
}

var (
	helpDocs   []*HelpDoc
	helpBySlug = map[string]*HelpDoc{}
	reHelpH2   = regexp.MustCompile(`^## +(.+?)(?: +\{([^{}]*)\})? *$`)
	reHelpH3   = regexp.MustCompile(`^### +(.+?)(?: +\{([^{}]*)\})? *$`)

	reHelpSlug = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
)

func init() {
	names, err := fs.Glob(helpFS, "helpdocs/*.md")
	if err != nil {
		panic(err)
	}
	for _, n := range names {
		b, err := helpFS.ReadFile(n)
		if err != nil {
			panic(err)
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(n, "helpdocs/"), ".md")
		d := parseHelp(slug, HelpExpand(string(b))) // {{company}}, {{qa_room}} … (site.go)
		helpDocs = append(helpDocs, d)
		helpBySlug[slug] = d
	}
	sort.SliceStable(helpDocs, func(i, j int) bool { return helpDocs[i].Order < helpDocs[j].Order })
}

// helpSlug is a heading's anchor: lower-case ASCII letters and digits joined by dashes.
func helpSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	if b.Len() == 0 {
		return "s"
	}
	return b.String()
}

func helpAttrs(raw string) (id string, admin bool) {
	for _, t := range strings.Fields(raw) {
		if strings.HasPrefix(t, "#") {
			id = t[1:]
		} else if t == "admin" {
			admin = true
		}
	}
	return
}

func (d *HelpDoc) uniqueID(id string) string {
	base, n := id, 2
	for d.ids[id] {
		id = base + "-" + strconv.Itoa(n)
		n++
	}
	d.ids[id] = true
	return id
}

// parseHelp reads one file: front matter, the intro, then "## " sections with their block trees.
func parseHelp(slug, src string) *HelpDoc {
	d := &HelpDoc{Slug: slug, Title: slug, Order: 99, ids: map[string]bool{}}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i = 1; i < len(lines) && strings.TrimSpace(lines[i]) != "---"; i++ {
			k, v, ok := strings.Cut(lines[i], ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "title":
				d.Title = v
			case "summary":
				d.Summary = v
			case "order":
				if n, err := strconv.Atoi(v); err == nil {
					d.Order = n
				}
			case "audience":
				d.Admin = v == "admin"
			}
		}
		i++
	}
	var sec *helpSection
	root := &helpNode{kind: "root"}
	stack := []*helpNode{root}
	var buf []string
	var fence *mdFence
	flush := func() {
		if len(buf) > 0 {
			top := stack[len(stack)-1]
			top.kids = append(top.kids, &helpNode{kind: "md", text: strings.Join(buf, "\n")})
			buf = buf[:0]
		}
	}
	finish := func() {
		flush()
		stack = stack[:1]
		if sec == nil {
			d.Intro = root.kids
		} else {
			sec.Nodes = root.kids
			d.Sections = append(d.Sections, sec)
		}
		root = &helpNode{kind: "root"}
		stack = []*helpNode{root}
	}
	for ; i < len(lines); i++ {
		raw := lines[i]
		if fence != nil {
			if mdFenceClose(raw, fence) {
				fence = nil
			}
			buf = append(buf, raw)
			continue
		}
		if f := mdFenceOpen(raw); f != nil {
			fence = f
			buf = append(buf, raw)
			continue
		}
		t := strings.TrimSpace(raw)
		switch {
		case len(stack) == 1 && reHelpH2.MatchString(raw):
			m := reHelpH2.FindStringSubmatch(raw)
			finish()
			id, admin := helpAttrs(m[2])
			if id == "" {
				id = helpSlug(m[1])
			}
			sec = &helpSection{ID: d.uniqueID(id), Title: m[1], Admin: admin}
		case reHelpH3.MatchString(raw):
			m := reHelpH3.FindStringSubmatch(raw)
			flush()
			id, _ := helpAttrs(m[2])
			if id == "" {
				pre := ""
				if sec != nil {
					pre = sec.ID + "-"
				}
				id = pre + helpSlug(m[1])
			}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, &helpNode{kind: "h3", text: m[1], id: d.uniqueID(id)})
		case strings.HasPrefix(t, ":::details "):
			flush()
			n := &helpNode{kind: "details", text: strings.TrimSpace(strings.TrimPrefix(t, ":::details "))}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case t == ":::admin":
			flush()
			n := &helpNode{kind: "admin"}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case t == ":::" && len(stack) > 1:
			flush()
			stack = stack[:len(stack)-1]
		default:
			buf = append(buf, raw)
		}
	}
	finish()
	return d
}

// --- rendering ------------------------------------------------------------------------------------------

func (d *HelpDoc) visible(admin bool) bool { return admin || !d.Admin }

func (d *HelpDoc) sections(admin bool) []*helpSection {
	var out []*helpSection
	for _, s := range d.Sections {
		if admin || !s.Admin {
			out = append(out, s)
		}
	}
	return out
}

func (d *HelpDoc) outline(admin bool) string {
	var b strings.Builder
	for _, s := range d.sections(admin) {
		b.WriteString(`<li><a href="#` + s.ID + `">` + E(s.Title) + `</a></li>`)
	}
	return b.String()
}

func helpVisibleDocs(admin bool) []*HelpDoc {
	var out []*HelpDoc
	for _, d := range helpDocs {
		if d.visible(admin) {
			out = append(out, d)
		}
	}
	return out
}

// --- search ---------------------------------------------------------------------------------------------

var (
	reHelpPlainLink = regexp.MustCompile(`\[([^\[\]]+)\]\([^)\s]*\)`)
	reHelpPlainSep  = regexp.MustCompile(`(?m)^\s*\|?[\s:|-]*-{3,}[\s:|-]*$`)
	reHelpPlainMark = regexp.MustCompile("[*`|#>]+|(?m)^\\s*(?:[-*]|\\d+\\.)\\s+")
	reHelpSpaces    = regexp.MustCompile(`\s+`)
)

func helpPlainMd(s string) string {
	s = reHelpPlainLink.ReplaceAllString(s, "$1")
	s = reHelpPlainSep.ReplaceAllString(s, " ")
	s = reHelpPlainMark.ReplaceAllString(s, " ")
	return s
}

func helpPlain(nodes []*helpNode, admin bool) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.kind {
		case "md":
			b.WriteString(helpPlainMd(n.text) + " ")
		case "h3", "details":
			b.WriteString(helpPlainMd(n.text) + ". " + helpPlain(n.kids, admin) + " ")
		case "admin":
			if admin {
				b.WriteString(helpPlain(n.kids, admin) + " ")
			}
		}
	}
	return strings.TrimSpace(reHelpSpaces.ReplaceAllString(b.String(), " "))
}

func lowerRunes(s string) []rune {
	r := []rune(s)
	for i := range r {
		r[i] = unicode.ToLower(r[i])
	}
	return r
}

func runeIndex(hay, needle []rune, from int) int {
	for i := from; i+len(needle) <= len(hay); i++ {
		ok := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

type helpHit struct {
	doc       *HelpDoc
	id, title string
	text      string
	score     int
}

// HelpSearch is the server-side search: every word (case-insensitive substring) must be in the section.
func HelpSearch(q string, admin bool) []helpHit {
	var words [][]rune
	for _, w := range strings.Fields(q) {
		words = append(words, lowerRunes(w))
	}
	if len(words) == 0 {
		return nil
	}
	all := func(hay []rune) bool {
		for _, w := range words {
			if runeIndex(hay, w, 0) < 0 {
				return false
			}
		}
		return true
	}
	var hits []helpHit
	for _, d := range helpVisibleDocs(admin) {
		cands := []helpHit{{doc: d, title: d.Title, text: helpPlain(d.Intro, admin)}}
		for _, s := range d.sections(admin) {
			cands = append(cands, helpHit{doc: d, id: s.ID, title: s.Title, text: helpPlain(s.Nodes, admin)})
		}
		for _, h := range cands {
			if h.text == "" && h.id == "" {
				continue
			}
			if !all(lowerRunes(h.title + " " + d.Title + " " + h.text)) {
				continue
			}
			if all(lowerRunes(h.title)) {
				h.score = 2
			} else if all(lowerRunes(d.Title)) {
				h.score = 1
			}
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	return hits
}

// helpSnippet is about 180 characters around the first match, escaped, with every match in <mark>.
func helpSnippet(text string, q string) string {
	r := []rune(text)
	low := lowerRunes(text)
	var words [][]rune
	for _, w := range strings.Fields(q) {
		words = append(words, lowerRunes(w))
	}
	at := -1
	for _, w := range words {
		if i := runeIndex(low, w, 0); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	start, end := 0, len(r)
	if at > 60 {
		start = at - 60
		for start < at && !unicode.IsSpace(r[start]) {
			start++
		}
	}
	if end-start > 200 {
		end = start + 200
		for end > start+120 && !unicode.IsSpace(r[end-1]) {
			end--
		}
	}
	mark := make([]bool, len(r))
	for _, w := range words {
		for i := runeIndex(low, w, start); i >= 0 && i < end; i = runeIndex(low, w, i+1) {
			for j := i; j < i+len(w) && j < len(r); j++ {
				mark[j] = true
			}
		}
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("… ")
	}
	for i := start; i < end; {
		j := i
		for j < end && mark[j] == mark[i] {
			j++
		}
		piece := E(string(r[i:j]))
		if mark[i] {
			piece = "<mark>" + piece + "</mark>"
		}
		b.WriteString(piece)
		i = j
	}
	if end < len(r) {
		b.WriteString(" …")
	}
	return b.String()
}
