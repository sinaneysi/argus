package app

// sources.go: Settings -> Data sources (GET /admin/sources, POST /admin/sources; ADR-0017). Every system Argus reads
// (GitLab, Grafana, Kubernetes, Outline, Elasticsearch/OpenSearch, Sentry), as many of each type as the team has, each
// with its environment, purpose and a check that says whether the login works and is read-only.
//
// The app never stores or shows a credential. A save writes it once into settings-in/sources/<user>-<16 hex>.json (0600,
// O_EXCL) and forgets it; root's `argus sources apply` (argus-sources.path, internal/sources) checks it again, stores it
// under ARGUS_SOURCES_DIR/<type>/<name>.cred and checks the login. This module reads only settings-out/sources-status.json.

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const (
	srcFID     = "src"
	srcMaxCred = 8 * 1024 // internal/sources MaxCred
)

// srcType is one kind of data source: its label, what it is for, the purposes it may have (first = default).
type srcType struct {
	Key, Label, Blurb string
	Purposes          []string
	Username          bool   // Elasticsearch: basic auth (username + password) or an API key
	Managed           string // added on another page (Kubernetes)
	NoCred            bool   // the credential is optional (an Alertmanager without a login)
	CredLabel         string
	CredHint          string
}

var (
	srcNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`) // sources.NameRe
	srcUserRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)   // sources.UserRe
	srcKeyRe  = regexp.MustCompile(`^([a-z]{3,20})/([a-z0-9][a-z0-9-]{0,30})$`)
	srcEnvs   = []string{"develop", "pre-release", "release", "shared"}
	srcTypes  = []srcType{
		{Key: "gitlab", Label: "GitLab", Blurb: "Code, merge requests, pipelines and job logs; or the issue tracker.",
			Purposes: []string{"agent", "tracker"}, CredLabel: "Access token",
			CredHint: "A personal, project, group or impersonation token. For the agent: scopes read_api and read_repository only."},
		{Key: "grafana", Label: "Grafana", Blurb: "Prometheus and Loki through Grafana: metrics, logs, alert rules.",
			Purposes: []string{"agent"}, CredLabel: "Service-account token",
			CredHint: "A service account with the Viewer role (Administration → Users and access → Service accounts)."},
		{Key: "kubernetes", Label: "Kubernetes", Blurb: "Pods, logs and events of a cluster, read-only.",
			Purposes: []string{"agent"}, Managed: "/admin/kube"},
		{Key: "outline", Label: "Outline", Blurb: "The wiki: service docs Argus publishes, or pages the agent reads.",
			Purposes: []string{"docs", "agent"}, CredLabel: "API key",
			CredHint: "Outline → Settings → API & Apps → New API key. Publishing needs a member; the agent needs a viewer."},
		{Key: "elasticsearch", Label: "Elasticsearch / OpenSearch", Blurb: "Log and event indices.",
			Purposes: []string{"agent"}, Username: true, CredLabel: "API key or password",
			CredHint: "With a username: that user's password (basic auth). Without: an Elasticsearch API key. The role must only read."},
		{Key: "sentry", Label: "Sentry", Blurb: "Errors and releases per project.",
			Purposes: []string{"agent"}, CredLabel: "Auth token",
			CredHint: "An internal integration or user auth token with read scopes only (project:read, org:read, event:read)."},
		{Key: "alertmanager", Label: "Alertmanager / Prometheus", Blurb: "Firing alerts for the Alerts and Incidents pages.",
			Purposes: []string{"alerts"}, NoCred: true, CredLabel: "Bearer token (optional)",
			CredHint: "Only when the server asks for a login. Argus reads /api/v2/alerts (Alertmanager) or /api/v1/alerts (Prometheus)."},
		{Key: "jira", Label: "Jira", Blurb: "The board's issue tracker: tasks become Jira issues.",
			Purposes: []string{"tracker"}, Username: true, CredLabel: "API token or password",
			CredHint: "With a username: that user's API token or password (basic auth). Without: a personal access token."},
		{Key: "matrix", Label: "Matrix", Blurb: "Chat: notices to one room, and the conversations Argus reads.",
			Purposes: []string{"notify"}, CredLabel: "Access token",
			CredHint: "The access token of the bot account (in the chat client's settings)."},
		{Key: "anthropic", Label: "Anthropic API", Blurb: "The model for planning, reports and chat when ARGUS_MODEL_PROVIDER=api.",
			Purposes: []string{"model"}, CredLabel: "API key",
			CredHint: "An API key from the Anthropic Console (sk-ant-api..). Address: https://api.anthropic.com. A Claude subscription token goes to Settings → Claude."},
		{Key: "s3", Label: "S3 / MinIO", Blurb: "Object storage the backup check lists: age and size of the newest backups.",
			Purposes: []string{"backups"}, Username: true, CredLabel: "Secret key",
			CredHint: "Username: the access key of a user that may only list and read the backup buckets. Here: its secret key."},
	}
	// srcComing are the types ADR-0017 plans for P2; listed so nobody looks for them in vain.
	srcComing       = []string{"Loki (direct)", "Splunk"}
	srcPurposeWords = map[string][2]string{
		"agent":   {"Agent reads", "The agent reads through it. Must be read-only."},
		"tracker": {"Issue tracker", "The board's issues. Needs a login that changes issues (GitLab: scope api)."},
		"alerts":  {"Alerts", "Argus reads the firing alerts every minute."},
		"notify":  {"Notices", "Argus sends notices to the configured room."},
		"docs":    {"Publishes docs", "Argus writes service docs there. Needs a member login."},
		"model":   {"Model", "Argus's model calls use it (ARGUS_MODEL_PROVIDER=api)."},
		"backups": {"Backup check", "Argus lists the backup objects daily. Must only list and read."},
	}
)

func srcTypeOf(k string) (srcType, bool) {
	for _, t := range srcTypes {
		if t.Key == k {
			return t, true
		}
	}
	return srcType{}, false
}

// SourcesStatus is settings-out/sources-status.json ({} when root has not published it yet).
func SourcesStatus() *py.OMap { return ReadJSONMap(filepath.Join(SET_OUT, "sources-status.json")) }

// srcCleanURL mirrors root's clean_url: https://host[:port][/path], no user, query or fragment. "" + why when refused.
func srcCleanURL(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "Enter the address, for example https://grafana.example.com."
	}
	if len(raw) > 300 || strings.ContainsAny(raw, " \t\r\n") {
		return "", "The address is too long or has spaces."
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && env("ARGUS_SOURCES_ALLOW_HTTP", "0") == "1")) {
		return "", "The address must start with https://."
	}
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "?") || strings.Contains(raw, "#") {
		return "", "Use https://host[:port][/path], without a user, query or fragment."
	}
	return strings.TrimRight(u.Scheme+"://"+strings.ToLower(u.Host)+u.EscapedPath(), "/"), ""
}

func srcDrop(sess *Session, payload *py.OMap) (string, error) {
	rid := auth.TokenHex(8)
	p := payload.Copy()
	p.Set("user", sess.Username)
	p.Set("at", NowInt())
	if err := boardDropExcl(filepath.Join(SET_IN, "sources"), sess.Username+"-"+rid+".json", []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("data source %s %s/%s requested by %s: %s", py.Str(payload.Get("kind")), py.Str(payload.Get("type")),
		py.Str(payload.Get("name")), sess.Username, rid) // never the credential
	return rid, nil
}

// SourcesPost is POST /admin/sources: save (add or edit), test, remove, check-all.
func SourcesPost(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	action := form.Get("action")
	if action == "check-all" {
		rid, err := srcDrop(sess, py.NewOMap("kind", "check-all"))
		if err != nil {
			return boardCouldNotSave(c, err)
		}
		return 303, "/admin/sources?sr=" + rid
	}
	if action != "save" && action != "test" && action != "remove" {
		return 400, "Unknown action"
	}
	t, ok := srcTypeOf(form.Get("type"))
	if !ok || t.Managed != "" {
		return 400, "Unknown data source type"
	}
	name := py.Strip(form.Get("name"))
	mode := form.Get("mode")
	if action != "save" {
		if !srcNameRe.MatchString(name) {
			return 400, "Unknown data source"
		}
		rid, err := srcDrop(sess, py.NewOMap("kind", action, "type", t.Key, "name", name))
		if err != nil {
			return boardCouldNotSave(c, err)
		}
		return 303, "/admin/sources?sr=" + rid
	}
	if mode != "add" && mode != "edit" {
		return 400, "Unknown save mode"
	}
	rej := func(field, msg string) (int, string) { return 400, sourcesRefused(c, field, msg) }
	if !srcNameRe.MatchString(name) {
		return rej("name", "Use lowercase letters, digits and -, up to 31 characters, for example prod or east.")
	}
	env := form.Get("env")
	if !In(env, srcEnvs...) {
		return rej("env", "Choose the environment this data source belongs to.")
	}
	purpose := py.OrS(form.Get("purpose"), t.Purposes[0])
	if !In(purpose, t.Purposes...) {
		if len(t.Purposes) == 1 { // the form has no choice to make: a hand-made request
			return 400, "Unknown purpose"
		}
		return rej("purpose", "Choose what Argus uses it for.")
	}
	if purpose != "agent" && env == "release" {
		return rej("purpose", "Release data sources are for the agent only, read-only: production is never written.")
	}
	override := form.Get("override") == "1"
	if override && (purpose != "agent" || (env != "develop" && env != "pre-release")) {
		return rej("override", "Only develop and pre-release take the override; release and shared must be read-only.")
	}
	u, why := srcCleanURL(form.Get("url"))
	if why != "" {
		return rej("url", why)
	}
	user := py.Strip(form.Get("username"))
	if user != "" && !t.Username {
		return 400, "This type has no username"
	}
	if user != "" && !srcUserRe.MatchString(user) {
		return rej("username", "A username uses letters, digits and . _ @ - only.")
	}
	cred := strings.TrimSpace(form.Get("credential"))
	if cred == "" && mode == "add" && !t.NoCred {
		return rej("credential", "Paste the "+strings.ToLower(t.CredLabel)+".")
	}
	if len(cred) > srcMaxCred || strings.ContainsAny(cred, "\r\n\t\x00") {
		return rej("credential", "The "+strings.ToLower(t.CredLabel)+" is too long or has line breaks.")
	}
	rid, err := srcDrop(sess, py.NewOMap("kind", "save", "mode", mode, "type", t.Key, "name", name, "env", env, "url", u,
		"purpose", purpose, "username", user, "credential", cred, "override", override))
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/sources?sr=" + rid
}
