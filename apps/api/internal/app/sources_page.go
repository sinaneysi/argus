package app

// sources_page.go: the Settings -> Data sources page (GET /admin/sources; ADR-0017). Its one question: "What can Argus
// read, and is every login the agent uses read-only?" A status block answers it, then one panel per type with a row per
// data source (state badge, environment, purpose, what the last check found, Check / Edit / Remove), the add flow
// (?add=1 picks a type, ?add=<type> is its form) and the edit form (?edit=<type>/<name>). Reuses the Settings -> Claude
// sheet (claude.css: .cl-now, .cl-sec, .cl-form ...), form.css and ul.rows; no rule of its own (DESIGN.md
// "Settings → Data sources").

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

type srcEntry struct {
	Key, Type, Name, Env, URL, Purpose, Status, Detail, Identity string
	Managed, CredWhere, Manage, UsedBy, Last4, Username          string
	CheckedBy, CreatedBy                                         string
	LastCheck, CreatedAt                                         int64
	Enabled, Override, OverrideReq                               bool
	Findings                                                     []string
}

type srcFacts struct {
	Known   bool
	Entries []srcEntry
}

func srcFactsNow() srcFacts {
	st := SourcesStatus()
	f := srcFacts{Known: st.Len() > 0}
	m := py.AsMap(st.Get("sources"))
	if m == nil {
		return f
	}
	for _, k := range m.Keys() {
		e := py.AsMap(m.Get(k))
		mm := srcKeyRe.FindStringSubmatch(k)
		if e == nil || mm == nil {
			continue
		}
		if _, ok := srcTypeOf(mm[1]); !ok {
			continue
		}
		x := srcEntry{Key: k, Type: mm[1], Name: mm[2], Env: e.S("env"), URL: e.S("url"), Purpose: e.S("purpose"),
			Status: py.OrS(e.S("status"), "unchecked"), Detail: e.S("detail"), Identity: e.S("identity"),
			Managed: py.OrS(e.S("managed"), "registry"), CredWhere: e.S("cred_where"), Manage: e.S("manage"),
			UsedBy: e.S("used_by"), Last4: e.S("cred_last4"), Username: e.S("username"), CheckedBy: e.S("checked_by"),
			CreatedBy: e.S("created_by"), LastCheck: py.I(e.Get("last_check")), CreatedAt: py.I(e.Get("created_at")),
			Enabled: e.Get("enabled") == true, Override: e.Get("override") == true, OverrideReq: e.Get("override_requested") == true}
		for _, v := range e.List("findings") {
			if s, ok := v.(string); ok && len(x.Findings) < 8 {
				x.Findings = append(x.Findings, s)
			}
		}
		f.Entries = append(f.Entries, x)
	}
	sort.Slice(f.Entries, func(i, j int) bool { return f.Entries[i].Key < f.Entries[j].Key })
	return f
}

func (f srcFacts) find(key string) (srcEntry, bool) {
	for _, e := range f.Entries {
		if e.Key == key {
			return e, true
		}
	}
	return srcEntry{}, false
}

// srcAttention: the login does not work, or the agent's login can write.
func srcAttention(e srcEntry) bool {
	return e.Status == "failed" || e.Status == "unreachable" || (e.Status == "refused" && !e.Override)
}

// srcBadge is a data source's state in one word, matching its colour.
func srcBadge(e srcEntry) string {
	switch e.Status {
	case "readonly":
		return `<span class="badge ok">Read-only</span>`
	case "ok":
		return `<span class="badge ok">OK</span>`
	case "refused":
		if e.Override {
			return `<span class="badge warn">Override</span>`
		}
		return `<span class="badge fail">Refused: can write</span>`
	case "failed":
		return `<span class="badge fail">Login failed</span>`
	case "unreachable":
		return `<span class="badge warn">Unreachable</span>`
	}
	return `<span class="badge">Not checked</span>`
}

func srcVerdictOf(f srcFacts) claudeVerdict {
	if !f.Known {
		return claudeVerdict{"unknown", "Not checked", "Root has not reported on data sources yet.",
			"The list appears within minutes of the next deploy: root brings in GitLab, Grafana, Outline and the Kubernetes clusters Argus already uses.", "", ""}
	}
	if len(f.Entries) == 0 {
		return claudeVerdict{"unknown", "None yet", "No data source yet.",
			"Add the systems Argus should read: GitLab, Grafana, Kubernetes, Outline, Elasticsearch or Sentry.", "", ""}
	}
	var bad []srcEntry
	for _, e := range f.Entries {
		if srcAttention(e) {
			bad = append(bad, e)
		}
	}
	n := len(f.Entries)
	if len(bad) > 0 {
		names := make([]string, 0, 3)
		for i, e := range bad {
			if i == 3 {
				names = append(names, "…")
				break
			}
			names = append(names, e.Key)
		}
		return claudeVerdict{If(len(bad) == len(f.Entries), "fail", "warn"), If(len(bad) == len(f.Entries), "Not working", "Check"),
			E(strings.Join(names, ", ")) + If(len(bad) == 1, " needs", " need") + " attention.",
			"A login failed, the source did not answer, or the agent's login can write. The agent uses only sources whose login works and is read-only.",
			If(len(bad) == 1, "See "+bad[0].Key, "See the first of "+I2S(int64(len(bad)))), "#src-" + bad[0].Type + "-" + bad[0].Name}
	}
	for _, e := range f.Entries {
		if e.Purpose == "agent" && e.Override {
			return claudeVerdict{"warn", "Override", E(e.Key) + " can write; the agent uses it with the override.",
				"Make a read-only login and save it, then turn the override off. The agent's tools still only read.",
				"See " + e.Key, "#src-" + e.Type + "-" + e.Name}
		}
	}
	unchecked := 0
	for _, e := range f.Entries {
		if e.Status == "unchecked" || e.Status == "" {
			unchecked++
		}
	}
	if unchecked > 0 {
		return claudeVerdict{"unknown", "Not checked", I2S(int64(unchecked)) + " of " + I2S(int64(n)) + If(n == 1, " data source is", " data sources are") + " not checked yet.",
			"Root checks a new source within seconds and all of them every 6 hours.", "", ""}
	}
	return claudeVerdict{"ok", "Working", "Argus reads " + I2S(int64(n)) + If(n == 1, " data source", " data sources") + "; every agent login is read-only.",
		"Each login was checked at its last check. Root checks them all again every 6 hours.", "", ""}
}

// srcFactList is the facts under the status block, in plain text.
func srcFactList(f srcFacts) []claudeFact {
	types := map[string]bool{}
	agent, ro, bad := 0, 0, 0
	var last int64
	for _, e := range f.Entries {
		types[e.Type] = true
		if e.Purpose == "agent" {
			agent++
			if e.Enabled && e.Status == "readonly" {
				ro++
			}
		}
		if srcAttention(e) {
			bad++
		}
		if e.LastCheck > last {
			last = e.LastCheck
		}
	}
	n := len(f.Entries)
	out := []claudeFact{{"", "Data sources", I2S(int64(n)), ""}}
	if n > 0 {
		out = append(out, claudeFact{"", "Types", I2S(int64(len(types))), ""})
		if agent > 0 {
			out = append(out, claudeFact{If(ro == agent, "ok", "warn"), "Read-only for the agent", I2S(int64(ro)) + " of " + I2S(int64(agent)), ""})
		}
		if bad > 0 {
			out = append(out, claudeFact{"fail", "Need attention", I2S(int64(bad)), ""})
		}
	}
	if last > 0 {
		out = append(out, claudeFact{"", "Last check", Ago(last), ""})
	}
	return out
}

func srcHost(u string) string {
	return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
}

// srcRowLines is a row's facts, what the last check said and where it is managed, in plain text.
func srcRowLines(e srcEntry) (meta []string, detail, where string) {
	meta = []string{}
	if h := srcHost(e.URL); h != "" {
		if len(h) > 60 {
			h = py.Slice(h, 0, 59) + "…"
		}
		meta = append(meta, h)
	}
	if e.Identity != "" {
		meta = append(meta, "as "+e.Identity)
	}
	if e.Last4 != "" {
		meta = append(meta, "credential ends …"+e.Last4)
	}
	if e.LastCheck > 0 {
		meta = append(meta, "checked "+Ago(e.LastCheck))
	} else {
		meta = append(meta, "never checked")
	}
	detail = strings.TrimSuffix(SafeErr(e.Detail), ".")
	if detail != "" {
		r, n := utf8.DecodeRuneInString(detail)
		detail = string(unicode.ToUpper(r)) + detail[n:] + "."
	}
	if e.Status == "refused" && e.Override {
		detail = strings.TrimSpace(detail + " Enabled for the agent with the override; its tool still only reads.")
	}
	if e.Type == "kubernetes" {
		where = "Managed in Settings → Kubernetes."
	} else if e.Managed == "legacy" {
		where = "Brought in from the old settings; jobs still read it from " + e.CredWhere + "."
	}
	return
}

// --- results of a request to root (?sr=<request id>) ---------------------------------------------------------------

var reSrcDone = regexp.MustCompile(`^(saved|tested) ([a-z]+)/([a-z0-9-]+): ([a-z]+) \((.*)\)$`)
var reSrcChecked = regexp.MustCompile(`^checked (\d+) sources$`)

func sourcesResult(c *Ctx, rid, user string) string {
	if !jiraRidRe.MatchString(rid) {
		return ""
	}
	busy := func(title, detail string) string {
		c.Refresh = 5
		return `<div class="cl-res">` + StateBox(StBusy, title, detail, `<a href="/admin/sources">Pause live updates</a>`) + `</div>`
	}
	if boardExists(filepath.Join(SET_IN, "sources", user+"-"+rid+".json")) {
		return busy("Checking on "+ARGUS_HOSTNAME+"…", "Root is storing it and asking the source what this login may do. "+
			"You can leave this page; the result shows here and the page refreshes itself.")
	}
	r := py.AsMap(py.AsMap(SourcesStatus().Get("results")).Get(rid))
	if r == nil || r.Get("user") != user {
		return busy("Sent. Waiting for root to pick it up.", "The result shows here. The page refreshes itself.")
	}
	kind, title, detail := srcResultWords(r.S("result"))
	box := ""
	switch kind {
	case "ok":
		box = StateBox(StSynced, title, detail, "")
	case "fail":
		box = StateBox(StFailed, title, detail, "")
	default:
		box = `<div class="banner warn" role="status">` + Icon("alert") + `<span><b>` + E(title) + `</b> ` + detail + `</span></div>`
	}
	return `<div class="cl-res">` + box + `</div>`
}

// srcResultWords maps root's result line (internal/sources handle) to a state, a plain title and the next step.
func srcResultWords(res string) (kind, title, detail string) {
	if strings.HasPrefix(res, "refused: ") {
		return "fail", "Not done: " + strings.TrimSuffix(SafeErr(py.Slice(res, 9)), ".") + ".", "Nothing was changed."
	}
	if strings.HasPrefix(res, "removed ") {
		n := strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(res, "removed "), ";", 2)[0], ".")
		return "ok", n + " removed. Argus no longer reads it.", "Root kept a backup of the credential."
	}
	if m := reSrcChecked.FindStringSubmatch(res); m != nil {
		return "ok", "Checked " + m[1] + If(m[1] == "1", " data source.", " data sources."), "Each row below shows its result."
	}
	m := reSrcDone.FindStringSubmatch(res)
	if m == nil {
		return "warn", "Finished.", E(strings.TrimSuffix(SafeErr(res), ".")) + "."
	}
	verb := If(m[1] == "saved", "saved", "checked")
	key, st := m[2]+"/"+m[3], m[4]
	why := E(strings.TrimSuffix(SafeErr(m[5]), "."))
	switch st {
	case "readonly":
		return "ok", key + " " + verb + ": read-only. The agent can use it.", "The check found " + why + "."
	case "ok":
		return "ok", key + " " + verb + ": it works.", "The check found " + why + "."
	case "refused":
		return "fail", key + " " + verb + ", but refused for the agent.", "<q>" + why + "</q>. Make a read-only login and save that instead."
	case "failed":
		return "fail", key + " " + verb + ", but the login failed.", "<q>" + why + "</q>. Edit it and paste a working credential."
	case "unreachable":
		return "warn", key + " " + verb + ", but Argus could not reach it.", "<q>" + why + "</q>. Check the address, then Check again."
	}
	return "warn", key + " " + verb + ".", why + "."
}

// --- the page -----------------------------------------------------------------------------------------------------

// sourcesPageWith is the page after a refused save: the form again, the field marked, the credential to paste again.
func sourcesRefused(c *Ctx, field, msg string) string {
	c.Rej = Reject(srcFID, field, msg, py.QS{})
	return msg
}

// srcBlockOf is what the query asks for under the status block: ?edit=<type>/<name>, ?add=<type> or ?add=1, the type
// picker. Nothing while a request is being applied, and no form for a type managed elsewhere.
func srcBlockOf(f srcFacts, qs py.QS, busy bool) (string, srcType, srcEntry) {
	switch {
	case busy:
	case qs.Get("edit") != "":
		if cur, ok := f.find(qs.Get("edit")); ok && cur.Managed != "legacy" {
			if t, ok := srcTypeOf(cur.Type); ok && t.Managed == "" {
				return "edit", t, cur
			}
		}
	case qs.Get("add") == "1":
		return "picker", srcType{}, srcEntry{}
	case qs.Get("add") != "":
		if t, ok := srcTypeOf(qs.Get("add")); ok && t.Managed == "" {
			return "add", t, srcEntry{}
		}
		return "picker", srcType{}, srcEntry{}
	}
	return "", srcType{}, srcEntry{}
}
