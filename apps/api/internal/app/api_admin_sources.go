package app

// Settings → Data sources as JSON for the Next.js page (ADR-0018): GET /api/v1/admin/sources and the twin of POST
// /admin/sources. Admins only. A credential goes to root once and is never read back: root publishes a source's facts
// and the last 4 characters, and only the fields of srcFactsNow reach this answer.

import (
	"github.com/sinaneysi/argus/internal/py"
)

func apiSrcRow(t srcType, e srcEntry) map[string]any {
	meta, detail, where := srcRowLines(e)
	out := map[string]any{"key": e.Key, "type": e.Type, "name": e.Name, "env": e.Env, "badge": apiAgentBadge(srcBadge(e)), "meta": meta, "detail": detail, "where": where,
		"findings": []string{}, "canCheck": t.Managed == ""}
	if len(e.Findings) > 0 && e.Status != "readonly" {
		out["findings"] = e.Findings
	}
	if w, ok := srcPurposeWords[e.Purpose]; ok {
		out["purpose"] = map[string]string{"label": w[0], "title": w[1]}
	}
	switch {
	case e.Managed == "legacy" && len(e.Manage) > 7 && e.Manage[:7] == "/admin/":
		out["change"] = e.Manage
	case e.Managed == "legacy":
		out["operator"] = true
	default:
		out["editable"] = true
	}
	return out
}

func apiSrcForm(t srcType, mode string, cur srcEntry) map[string]any {
	purposes := []map[string]string{}
	for _, p := range t.Purposes {
		purposes = append(purposes, map[string]string{"value": p, "label": srcPurposeWords[p][0], "sub": srcPurposeWords[p][1]})
	}
	return map[string]any{"type": t.Key, "label": t.Label, "blurb": t.Blurb, "mode": mode, "credLabel": t.CredLabel, "credHint": t.CredHint, "username": t.Username,
		"purposes": purposes, "override": In("agent", t.Purposes...), "name": cur.Name, "env": cur.Env, "url": cur.URL, "user": cur.Username,
		"purpose": py.OrS(cur.Purpose, t.Purposes[0]), "last4": cur.Last4, "overrideOn": cur.OverrideReq}
}

func apiAdminSourcesJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/admin/sources"}
	notices := apiNotices(sourcesResult(c, qs.Get("sr"), sess.Username))
	busy := c.Refresh != nil
	f := srcFactsNow()
	v := srcVerdictOf(f)
	panels := []map[string]any{}
	for _, t := range srcTypes {
		rows := []map[string]any{}
		for _, e := range f.Entries {
			if e.Type == t.Key {
				rows = append(rows, apiSrcRow(t, e))
			}
		}
		if len(rows) > 0 {
			panels = append(panels, map[string]any{"key": t.Key, "label": t.Label, "managed": t.Managed, "rows": rows})
		}
	}
	out := map[string]any{"host": ARGUS_HOSTNAME, "notices": notices, "refresh": apiRefresh(c), "busy": busy, "panels": panels, "count": len(f.Entries),
		"status": map[string]any{"kind": v.Kind, "badge": v.Badge, "head": Text(v.Head), "detail": apiClaudeText(v.Detail), "act": v.Act, "href": v.Href, "facts": srcFactList(f)}}
	mode, t, cur := srcBlockOf(f, qs, busy)
	out["block"] = mode
	switch mode {
	case "picker":
		types := []map[string]string{}
		for _, x := range srcTypes {
			href := "/admin/sources?add=" + x.Key + "#src-add"
			if x.Managed != "" {
				href = x.Managed + "#kube-add"
			}
			types = append(types, map[string]string{"label": x.Label, "blurb": x.Blurb, "href": href})
		}
		out["types"], out["coming"] = types, srcComing
	case "add", "edit":
		out["form"] = apiSrcForm(t, mode, cur)
	}
	return out, nil
}
