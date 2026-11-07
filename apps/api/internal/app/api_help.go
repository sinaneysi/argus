package app

// Help as JSON for the Next.js pages (ADR-0018): GET /api/v1/help (the index and the search), /help/page and /help/all.
// Same files, same order and the same admin filter as the Go pages: a viewer never receives an admin section or an
// admin block. The text stays markdown; apps/web renders it as elements.

import (
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiHelpNodes(nodes []*helpNode, admin bool, pre string) []map[string]any {
	out := []map[string]any{}
	for _, n := range nodes {
		if n.kind == "admin" && !admin {
			continue
		}
		x := map[string]any{"kind": n.kind, "text": n.text}
		if n.id != "" {
			x["id"] = pre + n.id
		}
		if n.kind == "details" || n.kind == "admin" {
			x["kids"] = apiHelpNodes(n.kids, admin, pre)
		}
		out = append(out, x)
	}
	return out
}

// apiHelpDoc is one page: intro and sections; pre prefixes every id (the printable page joins all pages).
func apiHelpDoc(d *HelpDoc, admin bool, pre string) map[string]any {
	secs := []map[string]any{}
	for _, s := range d.sections(admin) {
		secs = append(secs, map[string]any{"id": pre + s.ID, "title": s.Title, "admin": s.Admin, "nodes": apiHelpNodes(s.Nodes, admin, pre)})
	}
	return map[string]any{"slug": d.Slug, "title": d.Title, "summary": d.Summary, "admin": d.Admin,
		"intro": apiHelpNodes(d.Intro, admin, pre), "sections": secs}
}

func apiHelpPages(admin bool) []map[string]any {
	out := []map[string]any{}
	for _, d := range helpVisibleDocs(admin) {
		href := "/help/" + d.Slug
		if d.Slug == "index" {
			href = "/help"
		}
		out = append(out, map[string]any{"slug": d.Slug, "title": d.Title, "href": href})
	}
	return out
}

var reHelpMark = regexp.MustCompile(`(?s)<mark>(.*?)</mark>`)

// apiHelpSnippet is helpSnippet as pieces: text, and the matches marked.
func apiHelpSnippet(html string) []map[string]any {
	out := []map[string]any{}
	at := 0
	add := func(s string, mark bool) {
		if s != "" {
			out = append(out, map[string]any{"text": unescape(s), "mark": mark})
		}
	}
	for _, m := range reHelpMark.FindAllStringSubmatchIndex(html, -1) {
		add(html[at:m[0]], false)
		add(html[m[2]:m[3]], true)
		at = m[1]
	}
	add(html[at:], false)
	return out
}

func apiHelpSearch(q string, admin bool) map[string]any {
	hits := HelpSearch(q, admin)
	rows := []map[string]any{}
	for i, h := range hits {
		if i == helpMaxHits {
			break
		}
		href := "/help/" + h.doc.Slug
		if h.doc.Slug == "index" {
			href = "/help"
		}
		if h.id != "" {
			href += "#" + h.id
		}
		rows = append(rows, map[string]any{"href": href, "page": h.doc.Title, "section": If(h.id != "", h.title, ""), "snippet": apiHelpSnippet(helpSnippet(h.text, q))})
	}
	return map[string]any{"count": len(hits), "shown": len(rows), "rows": rows}
}

func apiHelpJSON(qs py.QS, sess *Session) (any, *apiErr) {
	admin := sess.Role == "admin"
	q := strings.TrimSpace(qs.Get("q"))
	if r := []rune(q); len(r) > helpMaxQuery {
		q = string(r[:helpMaxQuery])
	}
	out := map[string]any{"q": q, "maxQuery": helpMaxQuery}
	if q != "" {
		out["results"] = apiHelpSearch(q, admin)
		return out, nil
	}
	cards := []map[string]any{}
	for _, d := range helpVisibleDocs(admin) {
		if d.Slug == "index" {
			continue
		}
		secs := []apiOption{}
		for _, s := range d.sections(admin) {
			if len(secs) == 4 {
				break
			}
			if s.ID != "faq" {
				secs = append(secs, apiOption{"/help/" + d.Slug + "#" + s.ID, s.Title})
			}
		}
		cards = append(cards, map[string]any{"href": "/help/" + d.Slug, "title": d.Title, "summary": d.Summary, "admin": d.Admin, "sections": secs})
	}
	out["cards"] = cards
	if d, ok := helpBySlug["index"]; ok {
		out["home"] = apiHelpDoc(d, admin, "")
	}
	return out, nil
}

func apiHelpPageJSON(qs py.QS, sess *Session) (any, *apiErr) {
	admin := sess.Role == "admin"
	slug := qs.Get("slug")
	d, ok := helpBySlug[slug]
	if !ok || slug == "index" || !reHelpSlug.MatchString(slug) {
		return nil, &apiErr{404, "no such help page"}
	}
	if !d.visible(admin) {
		return nil, &apiErr{403, "This help page is for admins"}
	}
	return map[string]any{"doc": apiHelpDoc(d, admin, ""), "pages": apiHelpPages(admin), "maxQuery": helpMaxQuery}, nil
}

func apiHelpAllJSON(_ py.QS, sess *Session) (any, *apiErr) {
	admin := sess.Role == "admin"
	docs := []map[string]any{}
	for _, d := range helpVisibleDocs(admin) {
		docs = append(docs, apiHelpDoc(d, admin, d.Slug+"--"))
	}
	return map[string]any{"docs": docs}, nil
}
