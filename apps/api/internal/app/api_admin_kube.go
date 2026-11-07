package app

// Settings → Kubernetes as JSON for the Next.js page (ADR-0018): GET /api/v1/admin/kube and the twin of POST /admin/kube.
// Admins only. A kubeconfig goes to root once and is never read back: root publishes a cluster's facts, and only those
// listed in kubeFactsNow reach this answer.

import (
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiKubeCluster(c kubeCluster) map[string]any {
	line, agent, saved, test, why, find := kubeClusterLines(c)
	return map[string]any{"connect": ACCESS.Kube(c.Name), "connectVia": ACCESS.Label(), "name": c.Name, "env": c.Env, "badge": apiAgentBadge(kubeBadge(c)), "line": Text(line), "agent": Text(agent + saved), "test": Text(test),
		"why": apiClaudeText(why), "findings": find}
}

func apiAdminKubeJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/admin/kube"}
	notices := apiNotices(kubeResult(c, qs.Get("kr"), sess.Username))
	busy := c.Refresh != nil
	f := kubeFactsNow()
	v := kubeVerdictOf(f)
	clusters := []map[string]any{}
	replace, env := "", ""
	for _, x := range f.Clusters {
		clusters = append(clusters, apiKubeCluster(x))
		if x.Name == qs.Get("replace") {
			replace, env = x.Name, x.Env
		}
	}
	if kubeNameRe.MatchString(qs.Get("replace")) && replace == "" {
		replace = qs.Get("replace")
	}
	return map[string]any{"host": ARGUS_HOSTNAME, "notices": notices, "refresh": apiRefresh(c), "busy": busy,
		"status":   map[string]any{"kind": v.Kind, "badge": v.Badge, "head": Text(v.Head), "detail": apiClaudeText(v.Detail), "act": v.Act, "href": v.Href, "facts": kubeFactList(f)},
		"clusters": clusters, "add": map[string]any{"replace": replace, "env": env, "open": !busy && (len(f.Clusters) == 0 || replace != "")},
		"help": map[string]string{"yaml": strings.TrimSpace(kubeHelpYAML), "cmds": strings.TrimSpace(kubeHelpCmds)}}, nil
}
