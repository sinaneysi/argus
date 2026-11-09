package main

// credexpiry.go: the worker jobs `cred-expiry` (internal/credexpiry: daily, and on "Check now" of Settings ->
// Credentials) and `credcheck` (hourly: cred-status.json from the data sources' last checks). Every Settings -> Data
// sources entry with a credential and every credential Argus keeps itself is checked; only metadata is written.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/credexpiry"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/secret"
	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/worker"
)

// the data-source types whose credential can tell its own state, and the label of each type's credential
var credChecks = map[string]string{"gitlab": "gitlab", "grafana": "grafana", "jira": "jira", "matrix": "matrix", "outline": "outline"}
var credKinds = map[string]string{"gitlab": "GitLab token", "grafana": "Grafana service-account token", "jira": "Jira token",
	"matrix": "Matrix access token", "outline": "Outline API key", "alertmanager": "Alerts login", "kubernetes": "Kubernetes login",
	"elasticsearch": "Elasticsearch login", "sentry": "Sentry token", "teleport": "Teleport login", "claude": "Claude OAuth token",
	"oidc": "Sign-in client secret", "webhook": "Secret URL"}

func credHowto(kind, where string) []string {
	switch kind {
	case "claude":
		return []string{"Make a new token: `claude setup-token` on a computer signed in to the subscription (valid for one year).",
			"Paste it in Settings → Claude and test it. The old token stops being used at once."}
	case "oidc":
		return []string{"Make a new client secret for the Argus client in the identity provider.",
			"Set OIDC_CLIENT_SECRET in argus.env and restart the api."}
	case "webhook":
		return []string{"Make a new push address in the push monitor.", "Set ARGUS_DEADMAN_PUSH_URL in argus.env and restart the worker."}
	}
	return []string{"Make a new " + credKinds[kind] + " with the same role and scopes (read-only where it is read-only now).",
		"Replace it where it lives: " + where + ". Test it there.",
		"Revoke the old one in the service. Never paste a token into a chat or a ticket."}
}

func fileTime(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime().Unix()
	}
	return 0
}

// credList is every credential to check: the data sources' credentials, then Argus's own.
func credList(c *config.Config) []credexpiry.Cred {
	v := func(k string) string { return c.Values[k].Raw }
	so := sourcesOptions(c)
	reg := so.Registry()
	var keys []string
	for k := range reg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []credexpiry.Cred
	for _, k := range keys {
		e := reg[k]
		val := so.Credential(e)
		if val == "" {
			continue // an entry without a login (an open Alertmanager): nothing can expire
		}
		created, src := e.CredSetAt, "set in Settings"
		if created == 0 {
			created, src = fileTime(filepath.Join(v("ARGUS_SOURCES_DIR"), e.Type, e.Name+".cred")), "file changed"
		}
		where := "Settings → Data sources → " + k
		out = append(out, credexpiry.Cred{ID: "source-" + strings.ReplaceAll(k, "/", "-"), Name: credKinds[e.Type] + " (" + k + ")",
			Kind: e.Type, KindLabel: credKinds[e.Type], UsedBy: "data source " + k + " (purpose " + e.Purpose + ")", Check: credChecks[e.Type],
			URL: e.URL, User: e.Username, Where: where, Value: val, CreatedAt: created, CreatedSource: src, Howto: credHowto(e.Type, where)})
	}
	key := volumeKey(c)
	open := func(name, stored string) string {
		if stored == "" {
			return ""
		}
		if t, err := key.Open(name, stored); err == nil {
			return t
		}
		return ""
	}
	if f := v("ARGUS_CLAUDE_ENV_FILE"); f != "" {
		out = append(out, credexpiry.Cred{ID: "claude", Name: "Claude login (CLAUDE_CODE_OAUTH_TOKEN)", Kind: "claude", KindLabel: credKinds["claude"],
			UsedBy: "every model run", Check: "none", Where: "Settings → Claude", Value: open(secret.ClaudeName, secret.EnvValue(f, "CLAUDE_CODE_OAUTH_TOKEN")),
			CreatedAt: fileTime(f), CreatedSource: "file changed", ValidDays: 365, Optional: true, Howto: credHowto("claude", "")})
	}
	files := secret.FilesOf(v)
	if firstSource(c, "matrix") == nil && files.MatrixFile != "" {
		where := "the Matrix token file " + filepath.Base(files.MatrixFile)
		out = append(out, credexpiry.Cred{ID: "matrix-token", Name: "Matrix notify token", Kind: "matrix", KindLabel: credKinds["matrix"],
			UsedBy: "notices", Check: "matrix", URL: v("MATRIX_HOMESERVER"), Where: where, Value: open(secret.MatrixName, sources.ReadSecret(files.MatrixFile)),
			CreatedAt: fileTime(files.MatrixFile), CreatedSource: "file changed", Optional: true, Howto: credHowto("matrix", where)})
	}
	for p, name := range files.EtcFiles() {
		where := "Settings → Integrations → GitLab issues"
		out = append(out, credexpiry.Cred{ID: "gitlab-issues", Name: "GitLab issues token", Kind: "gitlab", KindLabel: credKinds["gitlab"],
			UsedBy: "the board's GitLab issue tasks", Check: "gitlab", URL: gitlabURL(c), Where: where, Value: open(name, sources.ReadSecret(p)),
			CreatedAt: fileTime(p), CreatedSource: "file changed", Optional: true, Howto: credHowto("gitlab", where)})
	}
	out = append(out, credexpiry.Cred{ID: "oidc", Name: "Sign-in client secret (OIDC_CLIENT_SECRET)", Kind: "oidc", KindLabel: credKinds["oidc"],
		UsedBy: "single sign-on", Check: "none", Where: "argus.env (OIDC_CLIENT_SECRET)", Value: v("OIDC_CLIENT_SECRET"), Optional: true, Howto: credHowto("oidc", "")})
	out = append(out, credexpiry.Cred{ID: "deadman-push", Name: "Push monitor address (ARGUS_DEADMAN_PUSH_URL)", Kind: "webhook", KindLabel: credKinds["webhook"],
		UsedBy: "the self-check's dead-man switch", Check: "none", Where: "argus.env (ARGUS_DEADMAN_PUSH_URL)", Value: v("ARGUS_DEADMAN_PUSH_URL"), Optional: true, Howto: credHowto("webhook", "")})
	return out
}

func credThresholds(c *config.Config) credexpiry.Thresholds {
	n := func(k string) int { i, _ := strconv.Atoi(c.Values[k].Raw); return i }
	return credexpiry.Thresholds{WarnDays: n("ARGUS_CRED_WARN_DAYS"), CritDays: n("ARGUS_CRED_CRIT_DAYS"), RotateDays: n("ARGUS_CRED_ROTATE_DAYS"),
		TodayDays: n("ARGUS_CRED_TODAY_DAYS")}.Fix()
}

// credExpiryRun is one check + record: credentials.json, incidents, notices. Names only in the log.
func credExpiryRun(ctx context.Context, c *config.Config, stderr io.Writer) error {
	o := &credexpiry.Options{Out: credExpiryFile(c), Thresholds: credThresholds(c),
		Prober: &credexpiry.Prober{AllowHTTP: c.Values["ARGUS_SOURCES_ALLOW_HTTP"].Raw == "1"}}
	doc, err := o.Check(ctx, credList(c))
	if err != nil {
		return err
	}
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	io := incidentOptions(c)
	res, err := credexpiry.Record(db, doc, doc.CheckedAt, io)
	if err != nil {
		return fmt.Errorf("cred-expiry record: %w", err)
	}
	for _, n := range res.Notices {
		credNotice(c, n, stderr)
	}
	fmt.Fprintf(stderr, "cred-expiry: %d checked, %d need attention, %d incident findings, %d notices, %d resolved\n",
		len(doc.Items), doc.Attention, len(res.Incidents), len(res.Notices), len(res.Resolved))
	return nil
}

// credNotice is one notice (m.notice, never a ping), secret-scanned by the sender.
func credNotice(c *config.Config, n credexpiry.Notice, stderr io.Writer) {
	notify.SetAppName(c.DisplayName)
	title, _ := credexpiry.AlertText(n.Item)
	lvl := "WARN"
	if n.Item.Status == "expired" || n.Item.Status == "rejected" {
		lvl = "FAIL"
	}
	text := lvl + ": " + title + ": " + n.Item.Detail + ". Used by " + n.Item.UsedBy + ". Rotate it: Settings → Credentials."
	if n.Incident != 0 {
		text += fmt.Sprintf(" Incident #%d.", n.Incident)
	}
	msg := notify.Formatter{Base: c.PublicURL}.Text("cred", text, "")
	msg.MsgType = "m.notice"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := senderFrom(c).Send(ctx, msg, "notify:cred"); err != nil && !errors.Is(err, notify.ErrNotSent) {
		fmt.Fprintln(stderr, "warning: credential notice not sent:", err)
	}
}

// credRequestJob runs the check when Settings -> Credentials asked for it (settings-in/requests/cred-expiry.req) and
// records the start and the result in settings-out/run-requests.json, which the page reads.
func credRequestJob(c *config.Config, stderr io.Writer) func(context.Context) error {
	req := filepath.Join(c.SettingsIn, "requests", "cred-expiry.req")
	return func(ctx context.Context) error {
		st, err := os.Stat(req)
		if err != nil {
			return nil
		}
		os.Remove(req)
		requested := st.ModTime().Unix()
		runRequest(c, "cred-expiry", map[string]any{"requested": requested, "started": time.Now().Unix(), "result": "running"})
		err = credExpiryRun(ctx, c, stderr)
		result := "ok"
		if err != nil {
			result = "failed: " + err.Error()
		}
		runRequest(c, "cred-expiry", map[string]any{"requested": requested, "started": time.Now().Unix(), "result": result})
		return err
	}
}

// runRequest sets one task's entry of settings-out/run-requests.json, keeping the others.
func runRequest(c *config.Config, task string, v map[string]any) {
	p := filepath.Join(c.SettingsOut, "run-requests.json")
	doc := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &doc)
	}
	tasks, _ := doc["tasks"].(map[string]any)
	if tasks == nil {
		tasks = map[string]any{}
	}
	tasks[task] = v
	doc["tasks"] = tasks
	b, _ := json.MarshalIndent(doc, "", " ")
	tmp := p + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) == nil {
		os.Rename(tmp, p)
	}
}

// credcheckRun writes cred-status.json: each data source's last check (the sources-check job tests every entry with
// one cheap read call), the Claude login and the notify switch. Outcomes only, never a value.
func credcheckRun(c *config.Config) error {
	type row struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	var rows []row
	st := sources.LoadStatus(filepath.Join(c.SettingsOut, "sources-status.json"))
	var keys []string
	for k := range st {
		if st[k] != nil && strings.Contains(k, "/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := st[k]
		d := e.Status
		if e.Detail != "" {
			d += ": " + e.Detail
		}
		if e.LastCheck > 0 {
			d += " (checked " + time.Unix(e.LastCheck, 0).UTC().Format("2006-01-02 15:04") + " UTC)"
		}
		rows = append(rows, row{credKinds[e.Type] + " " + k, e.Status == "ok" || e.Status == "readonly", sources.Mask(d)})
	}
	claude := false
	if f := c.Values["ARGUS_CLAUDE_ENV_FILE"].Raw; f != "" {
		claude = secret.EnvValue(f, "CLAUDE_CODE_OAUTH_TOKEN") != ""
	}
	if claude {
		rows = append(rows, row{"Claude login", true, "set"})
	} else {
		rows = append(rows, row{"Claude login", false, "not set"})
	}
	if c.Values["ARGUS_NOTIFY_MATRIX"].Raw == "1" {
		rows = append(rows, row{"Matrix notify", true, "on"})
	} else {
		rows = append(rows, row{"Matrix notify", false, "off"})
	}
	if rows == nil {
		rows = []row{}
	}
	b, _ := json.MarshalIndent(map[string]any{"checked_at": time.Now().Unix(), "items": rows}, "", " ")
	p := c.Values["ARGUS_CRED_STATUS_FILE"].Raw
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func credJobs(c *config.Config, stderr io.Writer) []worker.Job {
	return []worker.Job{
		{Name: "cred-expiry", Every: 24 * time.Hour, Run: func(ctx context.Context) error { return credExpiryRun(ctx, c, stderr) }},
		{Name: "cred-requests", Every: applyEvery, Run: credRequestJob(c, stderr)},
		{Name: "credcheck", Every: time.Hour, Run: func(context.Context) error { return credcheckRun(c) }},
	}
}
