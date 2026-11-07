package app

// Parts the /api/v1 section endpoints share: list state (filters, sort, pager), links inside text, record facts,
// markdown, the Summary card, the fix cards and the Problem panel, all as data for the Next.js pages (ADR-0018).
// Reports stay markdown: apps/web renders them as elements, the frontend never takes HTML from the API.

import (
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

type apiFacetOpt struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// apiFacet is one filter: its query key, label, chosen value and choices with counts that honour the other filters.
type apiFacet struct {
	Key     string        `json:"key"`
	Label   string        `json:"label"`
	Value   string        `json:"value"`
	Any     bool          `json:"any"` // offers "Any" (an empty value)
	Options []apiFacetOpt `json:"options"`
}

type apiTile struct {
	Label   string `json:"label"`
	Figure  string `json:"figure"`
	Sub     string `json:"sub"`
	Href    string `json:"href,omitempty"`
	Current bool   `json:"current,omitempty"`
	Hot     bool   `json:"hot,omitempty"`
}

// apiFacetOf lists the values with a count, and the chosen one (listFacet's rule).
func apiFacetOf(key, label, cur string, vals [][2]string, counts map[string]int) apiFacet {
	f := apiFacet{Key: key, Label: label, Value: cur, Any: true, Options: []apiFacetOpt{}}
	for _, v := range vals {
		if counts[v[0]] > 0 || cur == v[0] {
			f.Options = append(f.Options, apiFacetOpt{v[0], v[1], int64(counts[v[0]])})
		}
	}
	return f
}

// apiListOf is the toolbar and pager of a listQ page; call it after listPage.
func apiListOf(l listQ, matched, total int, noun string, sorts [][3]string) map[string]any {
	opts := []apiOption{}
	for _, s := range sorts {
		opts = append(opts, apiOption{s[0], s[1]})
	}
	n := l.per()
	pages := max(1, (matched+n-1)/n)
	out := map[string]any{"path": l.path, "q": l.get("q"), "sort": l.get("sort"), "sorts": opts, "filtered": l.filtered(),
		"clearHref": l.cleared().href(), "matched": matched, "total": total, "noun": noun, "page": l.page, "pages": pages,
		"from": min((l.page-1)*n+1, matched), "to": min(l.page*n, matched), "hidden": map[string]string{}}
	if l.page > 1 {
		out["prev"] = l.atPage(l.page - 1).href()
	}
	if l.page < pages {
		out["next"] = l.atPage(l.page + 1).href()
	}
	return out
}

var reAnchor = regexp.MustCompile(`(?s)<a\s[^>]*?href="([^"]*)"[^>]*>(.*?)</a>`)

func apiSafeHref(h string) string {
	h = strings.ReplaceAll(h, "&amp;", "&")
	if strings.HasPrefix(h, "https://") || (strings.HasPrefix(h, "/") && !strings.HasPrefix(h, "//") && !strings.Contains(h, `\`)) {
		return h
	}
	return ""
}

func apiSegText(html string) string {
	t := Text(html)
	if t == "" {
		return ""
	}
	raw := reTags.ReplaceAllString(html, " ")
	if raw != "" && (raw[0] == ' ' || raw[0] == '\n') {
		t = " " + t
	}
	if raw != "" && (raw[len(raw)-1] == ' ' || raw[len(raw)-1] == '\n') {
		t += " "
	}
	return t
}

// apiSegs turns a line of HTML into text and links, so a sentence keeps its links without any markup.
func apiSegs(html string) []apiLink {
	out := []apiLink{}
	at := 0
	for _, m := range reAnchor.FindAllStringSubmatchIndex(html, -1) {
		if t := apiSegText(html[at:m[0]]); t != "" {
			out = append(out, apiLink{Text: t})
		}
		out = append(out, apiLink{Text: Text(html[m[4]:m[5]]), Href: apiSafeHref(html[m[2]:m[3]])})
		at = m[1]
	}
	if t := apiSegText(html[at:]); t != "" {
		out = append(out, apiLink{Text: t})
	}
	return out
}

// apiKV is a record rail as facts.
func apiKV(kv [][2]string) []apiFact {
	out := []apiFact{}
	for _, x := range kv {
		out = append(out, apiFact{Label: x[0], Value: Text(x[1]), Href: firstHref(x[1]), Mono: strings.Contains(x[1], `class="mono`)})
	}
	return out
}

// apiMd is a report as markdown: the fix handoff sections removed, or (mode "fold", no cards on the page) kept apart.
func apiMd(text any, mode string) map[string]any {
	t := strings.ReplaceAll(py.OrS(text, ""), "\r\n", "\n")
	out := map[string]any{"md": t}
	if FHHasSections(t) {
		body, opts, prompt := FHSplit(t)
		out["md"] = body
		if mode == "fold" {
			out["foldOptions"], out["foldPrompt"] = opts, prompt
		}
	}
	return out
}

func apiOutline(items []OutlineItem) []apiOption {
	out := []apiOption{}
	for _, o := range items {
		out = append(out, apiOption{o.Anchor, Text(o.Text)})
	}
	return out
}

// apiLazy is the Summary card (LazyCard) as data; nil without one.
func apiLazy(l Lazy, ok bool) map[string]any {
	if !ok {
		return nil
	}
	rows := []apiOption{}
	for _, k := range []string{"what", "why", "impact", "fix"} {
		if v := l.field(k); v != "" {
			rows = append(rows, apiOption{v, lzLabels[k]})
		}
	}
	src, found := lzSourceT[l.Source]
	if !found {
		src = lzSourceT["derived"]
	}
	st := py.OrS(l.Status, "open")
	return map[string]any{"rows": rows, "status": st, "statusKind": getD(lzStatusK, st, "warn"), "source": Text(src)}
}

func apiFixOption(o *py.OMap, env string, prod bool) map[string]any {
	k := strings.ToLower(py.Strip(py.OrS(o.Get("kind"), "")))
	k = py.Strip(strings.SplitN(k, " |", 2)[0])
	kv := []apiOption{}
	for _, f := range [][2]string{{"why", "Why it works"}, {"effort", "Effort"}, {"risk", "Risk"}, {"blast_radius", "Blast radius"}} {
		if py.Truthy(o.Get(f[0])) {
			kv = append(kv, apiOption{py.Str(o.Get(f[0])), f[1]})
		}
	}
	out := map[string]any{"n": py.I(py.Or(o.Get("n"), int64(0))), "title": py.OrS(o.Get("title"), ""), "kind": fixKindLabel[k],
		"recommended": py.Truthy(o.Get("recommended")), "kv": kv, "what": ""}
	if w := o.Get("what"); py.Truthy(w) && !fixSame(w, o.Get("title")) {
		out["what"] = py.Str(w)
	}
	if !prod {
		tag := fixApprovalTag(o, env)
		out["approval"] = Text(tag)
		if m := regexp.MustCompile(`title="([^"]*)"`).FindStringSubmatch(tag); m != nil {
			out["approvalTitle"] = unescape(m[1])
		}
	}
	return out
}

var (
	reH2Tag  = regexp.MustCompile(`(?s)<h2[^>]*>(.*?)</h2>`)
	reBtnTag = regexp.MustCompile(`(?s)<a class="btn[^"]*" href="([^"]*)"[^>]*>(.*?)</a>`)
)

// apiFixAction is FixerProblemActions as data: a heading, the sentence, and the button to the review page.
func apiFixAction(html string) map[string]any {
	if html == "" {
		return nil
	}
	out := map[string]any{"heading": ""}
	if m := reH2Tag.FindStringSubmatch(html); m != nil {
		out["heading"] = Text(m[1])
		html = reH2Tag.ReplaceAllString(html, "")
	}
	if m := reBtnTag.FindStringSubmatch(html); m != nil {
		out["button"] = apiLink{Text: Text(m[2]), Href: apiSafeHref(m[1])}
		html = reBtnTag.ReplaceAllString(html, "")
	}
	out["body"] = apiSegs(html)
	return out
}

// apiFix is FixCards as data: the options and the prompt for a write-access agent.
func apiFix(h Handoff, env, dlQuery, actions, note string) map[string]any {
	prod := FHNormEnv(env) == "release"
	opts := []map[string]any{}
	for _, o := range h.Options {
		opts = append(opts, apiFixOption(o, env, prod))
	}
	checks := []string{}
	for _, x := range h.Flags {
		if !strings.HasPrefix(x, "no option marked") {
			checks = append(checks, x)
		}
	}
	if prod && len(opts) > 0 {
		note = strings.TrimSpace(note + " Production: the operator approves every option.")
	}
	out := map[string]any{"options": opts, "note": apiSegs(note), "prompt": h.Prompt, "checks": checks, "prod": prod, "action": apiFixAction(actions)}
	if h.Prompt != "" && dlQuery != "" {
		out["promptHref"] = "/rca/prompt.md?" + strings.ReplaceAll(dlQuery, "&amp;", "&")
	}
	if prod && actions == "" {
		out["releaseNote"] = Text(fixReleaseNote())
	}
	return out
}

// apiBoardBtn is BoardCreateButton as a link: "Create task", or the task that already exists.
func apiBoardBtn(kind string, sid any) apiLink {
	b := BoardCreateButton(kind, sid, "", "")
	return apiLink{Text: Text(b), Href: firstHref(b)}
}

type apiProbItem struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	Href   string `json:"href,omitempty"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
	Env    string `json:"env,omitempty"`
	Exists bool   `json:"exists"`
}

func apiProbItemOf(it probItem) apiProbItem {
	out := apiProbItem{Type: probTypeWord[it.T], Label: probRefLabel(it.T, it.ID), Href: it.Href, Status: it.Status, Env: it.Env, Exists: it.Exists}
	if it.T != "capacity" {
		out.Title = FirstLine(it.Title, 140)
	}
	return out
}

// apiProbSuggestion is one suggested link (probSuggestionRow).
func apiProbSuggestion(l Row, showItem, showProblem bool) map[string]any {
	src := probSourceWord[l.S("source")]
	if l.S("source") == "human" {
		src = l.S("by")
	}
	out := map[string]any{"id": l.I("id"), "confidence": py.F(l.F("confidence")*100, 0) + " %", "linkWord": probLinkWord[l.S("link_type")],
		"from": src, "at": l.I("at"), "reason": l.S("reason"), "canDecide": false}
	if showItem {
		out["item"] = apiProbItemOf(probDescribe(l.S("ref_type"), l.S("ref_id")))
	}
	if showProblem {
		out["problem"] = map[string]any{"id": l.I("problem_id"), "label": probPID(l.I("problem_id")), "title": FirstLine(l.S("ptitle"), 90)}
	}
	return out
}

// apiProblemPanel is ProblemPanel as data; nil when the page shows no panel.
func apiProblemPanel(c *Ctx, qs py.QS, t, id, title, noun string, inc int64) map[string]any {
	if c.Sess == nil || !ProblemsOn() {
		return nil
	}
	admin := c.Sess.Role == "admin"
	can := ProblemsCanAct()
	ps := problemsFor(t, id, inc)
	sugg := Q("SELECT l.*, p.title ptitle FROM problem_links l JOIN problems p ON p.id=l.problem_id WHERE l.ref_type=? AND l.ref_id=? "+
		"AND l.state='suggested' ORDER BY l.confidence DESC, l.id LIMIT 10", t, id)
	banners := apiBannersOf(probSentBanner(c, qs))
	if len(ps) == 0 && len(sugg) == 0 && !can && len(banners) == 0 {
		return nil
	}
	back := probBack(c, qs) + "#problem"
	out := map[string]any{"banners": banners, "banner": nil, "back": back, "ref": t + ":" + id, "suggestions": []map[string]any{}, "link": nil, "create": nil}
	if len(ps) > 0 {
		out["banner"] = apiSegs(probBannerHTML(ps))
	}
	list := []map[string]any{}
	for _, l := range sugg {
		s := apiProbSuggestion(l, false, true)
		s["canDecide"] = admin && can
		list = append(list, s)
	}
	out["suggestions"] = list
	if can {
		pq := py.Head(qs.Get("pq"), 80)
		picks := []map[string]any{}
		for _, r := range probOpenRows(pq, 10) {
			picks = append(picks, map[string]any{"id": r.I("id"), "title": FirstLine(r.S("title"), 110), "status": probWord[r.S("status")]})
		}
		types := []apiOption{}
		for _, lt := range probLinkTypes[t] {
			types = append(types, apiOption{lt, probLinkWord[lt]})
		}
		summary := "Link to problem…"
		if len(ps) > 0 {
			summary = "Link to another problem…"
		}
		if !admin {
			summary = "Suggest a problem…"
		}
		keep := map[string]string{}
		for _, kv := range qs.Pairs("sent", "preq", "pq", "all") {
			keep[kv[0]] = kv[1]
		}
		out["link"] = map[string]any{"summary": summary, "pq": pq, "picks": picks, "types": types, "button": If(admin, "Link", "Suggest"),
			"keep": keep, "path": py.URLSplit(c.Path).Path}
		if admin && len(ps) == 0 && In(t, append(probIssueTypes, "rca")...) {
			out["create"] = map[string]any{"title": py.Head(title, probMaxTitle), "noun": noun, "from": t + ":" + id, "first": t == "rca"}
		}
	}
	return out
}

// apiForm is a JSON body as the form a page handler reads; arrays of strings (ref, link, issue) stay repeated fields.
func apiForm(body map[string]any) py.QS {
	q := apiQS(body)
	for k, v := range body {
		if l, ok := v.([]any); ok {
			for _, x := range l {
				if s, ok := x.(string); ok {
					q.Add(k, s)
				}
			}
		}
	}
	return q
}
