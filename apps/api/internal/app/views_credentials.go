package app

// Settings → Credentials (/admin/credentials, admins; Go only) and the Overview card "Credentials": which credential
// Argus uses is near its expiry date. Root's bin/cred_expiry.py check writes settings-out/credentials.json (metadata
// only: never a value); this page reads that file and nothing else. "Check now" writes the same request file as the
// scan bar (settings-in/requests/cred-expiry.req), which root's run-requests turns into argus-cred-expiry.service.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

const credTask = "cred-expiry"

// CredExpiryPath is OPS_CRED_EXPIRY, else <settings-out>/credentials.json.
func CredExpiryPath() string {
	if p := env("ARGUS_CRED_EXPIRY_FILE", ""); p != "" {
		return p
	}
	return filepath.Join(SET_OUT, "credentials.json")
}

func credDoc() *py.OMap {
	d := ReadJSONMap(CredExpiryPath())
	if d == nil || !py.Truthy(d.Get("checked_at")) {
		return nil
	}
	return d
}

func credItems(d *py.OMap) []*py.OMap {
	var out []*py.OMap
	for _, v := range d.List("items") {
		if m := py.AsMap(v); m != nil {
			out = append(out, m)
		}
	}
	return out
}

var credAttention = map[string]bool{"warn": true, "rotate": true, "crit": true, "expired": true, "rejected": true, "error": true}

// credExpiresShort is credExpires without its explanation lines (the Overview card).
func credExpiresShort(it *py.OMap) string {
	cell := credExpires(it)
	if i := strings.Index(cell, "<div"); i > 0 {
		cell = cell[:i]
	}
	return `<span class="nw">` + cell + `</span>`
}

// credExpires is the Expires cell: the date and a days-left badge whose word matches its colour.
func credExpires(it *py.OMap) string {
	st, band := it.S("status"), it.S("band")
	date := it.S("expires_at")
	dl := ""
	if v := it.Get("days_left"); v != nil {
		dl = I2S(int64(py.Num(v))) + " d"
	}
	src := it.S("expiry_source")
	note := ""
	switch src {
	case "estimate":
		note = `<div class="dim small">estimated: valid ` + E(I2S(int64(py.Num(it.GetD("valid_days", int64(365)))))) +
			` days from when it was set</div>`
	case "last-known":
		note = `<div class="dim small">last known date (the check failed)</div>`
	}
	if st == "absent" {
		return `<span class="muted">-</span>`
	}
	if st == "rejected" {
		cell := `<span class="badge fail">rejected</span>`
		if date != "" {
			cell = E(date) + ` ` + cell
		}
		return cell + note
	}
	switch band {
	case "ok":
		return E(date) + ` <span class="badge ok">` + E(dl) + `</span>` + note
	case "warn":
		return E(date) + ` <span class="badge warn">` + E(dl) + `</span>` + note
	case "crit":
		return E(date) + ` <span class="badge fail">` + E(dl) + `</span>` + note
	case "expired":
		return E(date) + ` <span class="badge fail">expired</span>` + note
	}
	word := "no expiry info"
	if band == "never" {
		word = "never expires"
	}
	cell := `<span class="tag">` + word + `</span>`
	if due := it.S("rotate_due"); due != "" {
		rot := I2S(int64(py.Num(it.Get("rotate_days"))))
		if st == "rotate" {
			cell += `<div class="small"><span class="badge warn">rotate now</span> policy ` + E(rot) + ` d, due ` + E(due) + `</div>`
		} else {
			cell += `<div class="dim small">rotate by ` + E(due) + ` (every ` + E(rot) + ` d)</div>`
		}
	}
	return cell
}

func credSet(it *py.OMap) string {
	t := it.Get("created_at")
	if !py.Truthy(t) {
		return ""
	}
	how := map[string]string{"api": "created", "backup": "set on or before", "env file": "env file changed",
		"file changed": "file changed"}[it.S("created_source")]
	if how == "" {
		how = "set"
	}
	return how + " " + Strftime("2006-01-02", py.Num(t))
}

// credCheckState says what the check is doing: the words while it runs, or why the last request did not start.
func credCheckState(d *py.OMap) (busy, skipped string) {
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", credTask+".req")); err == nil {
		return "Queued", ""
	}
	st := ReadJSONMap(filepath.Join(SET_OUT, "run-requests.json")).Map("tasks")
	if st == nil {
		return "", ""
	}
	t := st.Map(credTask)
	if t == nil {
		return "", ""
	}
	started := py.Num(t.Get("started"))
	checked := 0.0
	if d != nil {
		checked = py.Num(d.Get("checked_at"))
	}
	if started > checked && Now()-started < 300 {
		return "Checking since " + Ago(started), ""
	}
	if r := t.S("result"); strings.HasPrefix(r, "skipped") && py.Num(t.Get("requested")) > checked {
		return "", strings.Replace(r, "skipped: ", "Not started: ", 1)
	}
	return "", ""
}

type credGroup struct {
	label string
	items []*py.OMap
	worst int
}

// credHowtoGroups: the credentials with steps, one group per kind, the worst kind first.
func credHowtoGroups(items []*py.OMap) []*credGroup {
	rank := map[string]int{"expired": 0, "rejected": 0, "crit": 1, "warn": 2, "rotate": 3, "error": 4, "ok": 5, "absent": 6}
	var order []*credGroup
	groups := map[string]*credGroup{}
	for _, it := range items {
		if len(it.List("howto")) == 0 || it.S("status") == "absent" {
			continue
		}
		k := it.S("kind_label")
		g, ok := groups[k]
		if !ok {
			g = &credGroup{label: k, worst: 9}
			groups[k] = g
			order = append(order, g)
		}
		g.items = append(g.items, it)
		if r, ok := rank[it.S("status")]; ok && r < g.worst {
			g.worst = r
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].worst < order[j].worst })
	return order
}
