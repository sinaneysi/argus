package app

// Settings → Claude as JSON for the Next.js page (ADR-0018): GET /api/v1/admin/claude and the twins of POST
// /admin/claude-token and /admin/claude-egress (the usage switch is /api/v1/admin/claude). Admins only. The token never
// reaches this file: root publishes whether one is set and its last 4 characters, as the Go page showed them.

import (
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var claudeQuotes = strings.NewReplacer("<q>", "“", "</q>", "”")

func apiClaudeText(html string) string { return Text(claudeQuotes.Replace(html)) }

func apiAdminClaudeJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/admin/claude"}
	notices := apiNotices(claudeResult(c, qs.Get("ct"), sess.Username))
	busy := c.Refresh != nil
	f := claudeFactsNow()
	v := claudeVerdictOf(f)
	if v.Kind == "ok" && !f.Guard {
		v.Detail += " The location guard is off."
	}
	tb, state, tl, why, warn := claudeTokenState(f)
	eb, line, ewhy, sum := claudeEgressState(f)
	guard, blocked, fail := ClaudeEgressSettings()
	return map[string]any{"user": sess.Username, "host": ARGUS_HOSTNAME, "notices": notices, "refresh": apiRefresh(c), "busy": busy,
		"status": map[string]any{"kind": v.Kind, "badge": v.Badge, "head": v.Head, "detail": apiClaudeText(v.Detail), "act": v.Act, "href": v.Href, "facts": claudeFactList(f)},
		"token":  map[string]any{"badge": apiAgentBadge(tb), "line": Text(state + " " + tl), "why": apiClaudeText(why), "warn": apiNotices(warn), "open": claudeTokenOpen(f, busy)},
		"egress": map[string]any{"badge": apiAgentBadge(eb), "line": Text(line), "why": apiClaudeText(ewhy), "summary": Text(sum), "guard": guard, "blocked": strings.Join(blocked, ", "), "fail": fail},
		"usage":  map[string]any{"on": f.On}}, nil
}
