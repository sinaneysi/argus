package sources

// apply.go: the root side (argus sources apply, run by argus-sources.path; argus sources check, run by
// argus-sources-check.timer). The web app (no access to any secret) writes one request per action into
// <settings-in>/sources/<user>-<16 hex>.json (0600):
//
//	{"kind": "save", "mode": "add"|"edit", "type": "gitlab", "name": "main", "env": "shared", "url": "https://...",
//	 "purpose": "agent", "username": "", "credential": "...", "override": false, "user", "at"}
//	{"kind": "test", "type": "gitlab", "name": "main"}     check it again
//	{"kind": "remove", "type": "gitlab", "name": "main"}   back the credential up and remove the source
//	{"kind": "check-all"}                                  check every source now
//
// Apply reads each request once (no symlinks, a regular file written by the app, size-capped), wipes and deletes it,
// checks that the user is an enabled admin, and then acts. The credential goes to <dir>/<type>/<name>.cred (0600 root;
// 0640 root:<agent group> only while the source is enabled for the agent: purpose agent and verified read-only, or the
// admin's override on develop or pre-release). It is written nowhere else: not to the log, the status file, the audit
// log or a result line; the registry keeps its last 4 characters only.
//
// Migration (ADR-0017 D5): every run mirrors the single integrations Argus already has into the registry (managed
// "legacy", keyed by a legacy key, idempotent): the GitLab read-only token (GITLAB_URL + the gitlab-ro-token secret or
// GITLAB_RO_TOKEN in the legacy env file), the GitLab issues tracker (gitlab-issues.json + gitlab-issues.token), every
// Grafana site with a URL and a token (GRAFANA_<ID>_URL + the grafana-<id>-token secret or GRAFANA_<ID>_TOKEN), Outline
// (outline.json + outline.token) and the clusters of Settings -> Kubernetes. Root reads those credentials where they
// live and never writes there; a legacy entry disappears when its old configuration does.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// Legacy says where the existing single integrations live (read only).
type Legacy struct {
	EtcDir      string            // ARGUS_ETC_DIR: gitlab-issues.json/.token, outline.json/.token (root-written by the pages)
	SecretsDir  string            // ARGUS_SECRETS_DIR: gitlab-ro-token, grafana-<id>-token, outline.token, gitlab-issues.token
	EnvFile     string            // ARGUS_SOURCES_LEGACY_ENV: KEY=value fallback (GITLAB_URL, GITLAB_RO_TOKEN, GRAFANA_<ID>_URL/_TOKEN)
	KubeDir     string            // ARGUS_KUBE_DIR: clusters.json of Settings -> Kubernetes
	GitLabURL   string            // GITLAB_URL when configured ("" falls back to the legacy env file)
	GrafanaURLs map[string]string // GRAFANA_<ID>_URL keys of the configuration
}

// Options say where everything is.
type Options struct {
	ReqDir     string // <settings-in>/sources
	OutDir     string // <settings-out>
	AuthDB     string
	Dir        string // ARGUS_SOURCES_DIR: registry.json, <type>/<name>.cred, backup/
	AgentGroup string // the group whose members (the agent) may read an enabled credential
	AppUID     int
	Legacy     Legacy
	Now        func() time.Time
	Log        io.Writer
}

func (o *Options) now() int64 {
	if o.Now != nil {
		return o.Now().Unix()
	}
	return time.Now().Unix()
}

func isRoot() bool { return os.Geteuid() == 0 }

// Entry is one data source in registry.json and sources-status.json (never a secret).
type Entry struct {
	Type              string         `json:"type"`
	Name              string         `json:"name"`
	Env               string         `json:"env"`
	URL               string         `json:"url"`
	Purpose           string         `json:"purpose"`
	Username          string         `json:"username,omitempty"`
	Status            string         `json:"status"`
	Detail            string         `json:"detail"`
	Identity          string         `json:"identity"`
	Findings          []string       `json:"findings"`
	Facts             map[string]any `json:"facts"`
	LastCheck         int64          `json:"last_check,omitempty"`
	CheckedBy         string         `json:"checked_by,omitempty"`
	Enabled           bool           `json:"enabled"`
	Override          bool           `json:"override"`
	OverrideRequested bool           `json:"override_requested"`
	Managed           string         `json:"managed"` // registry | legacy
	Legacy            string         `json:"legacy,omitempty"`
	CredRef           string         `json:"cred_ref,omitempty"` // legacy: secret:<file>, etc:<file>, env:<KEY>, kube:<name>
	CredWhere         string         `json:"cred_where,omitempty"`
	Manage            string         `json:"manage,omitempty"`
	UsedBy            string         `json:"used_by,omitempty"`
	CredLast4         string         `json:"cred_last4,omitempty"`
	CredSetAt         int64          `json:"cred_set_at,omitempty"`
	CredSetBy         string         `json:"cred_set_by,omitempty"`
	CreatedBy         string         `json:"created_by"`
	CreatedAt         int64          `json:"created_at,omitempty"`
	UpdatedBy         string         `json:"updated_by,omitempty"`
	UpdatedAt         int64          `json:"updated_at,omitempty"`
}

func (e *Entry) key() string { return e.Type + "/" + e.Name }

func (e *Entry) normalise() {
	if e.Findings == nil {
		e.Findings = []string{}
	}
	if e.Facts == nil {
		e.Facts = map[string]any{}
	}
	if e.Status == "" {
		e.Status = "unchecked"
	}
	if e.Managed == "" {
		e.Managed = "registry"
	}
}

// --- files ------------------------------------------------------------------------------------------------------------

func (o *Options) credPath(typ, name string) string { return filepath.Join(o.Dir, typ, name+".cred") }

func (o *Options) gid() int {
	g, err := user.LookupGroup(o.AgentGroup)
	if err != nil {
		return -1
	}
	n, _ := strconv.Atoi(g.Gid)
	return n
}

func (o *Options) ensureDirs(typ string) error {
	if err := os.MkdirAll(filepath.Join(o.Dir, "backup"), 0o700); err != nil {
		return err
	}
	g := o.gid()
	if isRoot() && g >= 0 {
		os.Chown(o.Dir, 0, g)
	}
	os.Chmod(o.Dir, 0o750) // the agent's group may enter it; each file says who may read it
	os.Chmod(filepath.Join(o.Dir, "backup"), 0o700)
	if typ != "" {
		d := filepath.Join(o.Dir, typ)
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
		if isRoot() && g >= 0 {
			os.Chown(d, 0, g)
		}
		os.Chmod(d, 0o750)
	}
	return nil
}

// setEnabled is 0640 root:<agent group> (the agent's tools may read it) or 0600 root:root.
func (o *Options) setEnabled(p string, on bool) error {
	g := o.gid()
	if isRoot() {
		owner := 0
		if on && g >= 0 {
			owner = g
		}
		os.Chown(p, 0, owner)
	}
	if on && g >= 0 {
		return os.Chmod(p, 0o640)
	}
	return os.Chmod(p, 0o600)
}

func (o *Options) writeCred(typ, name, value string) error {
	if err := o.ensureDirs(typ); err != nil {
		return err
	}
	p := o.credPath(typ, name)
	tmp := fmt.Sprintf("%s.%d.tmp", p, os.Getpid())
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		os.Chmod(tmp, 0o600)
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// backup copies the credential to backup/<type>-<name>.cred.<UTC stamp> (0600) and keeps the last 5; "" when none.
func (o *Options) backup(typ, name string) (string, error) {
	data := readSecretFile(o.credPath(typ, name), MaxCred+1)
	if _, err := os.Lstat(o.credPath(typ, name)); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err := o.ensureDirs(""); err != nil {
		return "", err
	}
	bdir := filepath.Join(o.Dir, "backup")
	prefix := typ + "-" + name + ".cred."
	stamp := time.Unix(o.now(), 0).UTC().Format("20060102-150405")
	dst := filepath.Join(bdir, prefix+stamp)
	for n := 1; ; n++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = filepath.Join(bdir, fmt.Sprintf("%s%s-%d", prefix, stamp, n))
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(data + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	olds, _ := filepath.Glob(filepath.Join(bdir, prefix+"*"))
	sort.Strings(olds)
	for len(olds) > keepBackups {
		os.Remove(olds[0])
		olds = olds[1:]
	}
	return filepath.Base(dst), nil
}

func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		os.Chmod(tmp, mode)
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Registry is <dir>/registry.json.
func (o *Options) Registry() map[string]*Entry {
	var doc struct {
		Sources map[string]*Entry `json:"sources"`
	}
	if b, err := os.ReadFile(filepath.Join(o.Dir, "registry.json")); err == nil {
		json.Unmarshal(b, &doc)
	}
	out := map[string]*Entry{}
	for k, e := range doc.Sources {
		if e != nil && e.key() == k {
			e.normalise()
			out[k] = e
		}
	}
	return out
}

func (o *Options) saveRegistry(s map[string]*Entry) error {
	if err := o.ensureDirs(""); err != nil {
		return err
	}
	for _, e := range s {
		e.normalise()
	}
	return writeJSON(filepath.Join(o.Dir, "registry.json"), map[string]any{"sources": s, "at": o.now()}, 0o644)
}

func (o *Options) audit(line string) {
	f, err := os.OpenFile(filepath.Join(o.OutDir, "audit.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Unix(o.now(), 0).Format("2006-01-02 15:04:05"), Mask(line))
}

// --- checks -------------------------------------------------------------------------------------------------------------

func (o *Options) envFile() map[string]string {
	if o.Legacy.EnvFile == "" {
		return map[string]string{}
	}
	return ParseEnv(readSecretFile(o.Legacy.EnvFile, 256*1024))
}

// Credential is the entry's credential (for the worker's jobs that read through a source; never logged).
func (o *Options) Credential(e *Entry) string { return o.credential(e) }

// credential reads the entry's credential where it lives (registry file, or the legacy location it points at).
func (o *Options) credential(e *Entry) string {
	kind, ref, _ := strings.Cut(e.CredRef, ":")
	switch kind {
	case "env":
		return o.envFile()[ref]
	case "secret":
		return readSecretFile(filepath.Join(o.Legacy.SecretsDir, filepath.Base(ref)), MaxCred)
	case "etc":
		return readSecretFile(filepath.Join(o.Legacy.EtcDir, filepath.Base(ref)), MaxCred)
	case "kube":
		return ""
	}
	return readSecretFile(o.credPath(e.Type, e.Name), MaxCred)
}

// RunCheck checks one source; network trouble is a status, never an error.
func (o *Options) RunCheck(e *Entry) Check {
	fn := checks[e.Type]
	if fn == nil {
		return result("unchecked", "this type is checked on its own page", "", nil, nil)
	}
	cred := o.credential(e)
	if cred == "" && !Types[e.Type].NoCred {
		d := "no credential stored"
		if e.CredWhere != "" {
			d += " (" + e.CredWhere + ")"
		}
		return result("failed", d, "", nil, nil)
	}
	u, err := CleanURL(e.URL)
	if err != nil {
		return result("unreachable", err.Error(), "", nil, nil)
	}
	c, err := fn(u, cred, e)
	cred = ""
	if err != nil {
		return result("unreachable", netError(err), "", nil, nil)
	}
	return c
}

// MayEnable is (enabled for the agent, override used): purpose agent only; read-only, or the override on develop or
// pre-release. Release and shared never take the override.
func MayEnable(e *Entry) (bool, bool) {
	if e.Purpose != "agent" {
		return false, false
	}
	if e.Status == "readonly" {
		return true, false
	}
	if e.Status == "refused" && e.OverrideRequested && in(e.Env, "develop", "pre-release") {
		return true, true
	}
	return false, false
}

func maskFacts(f map[string]any) map[string]any {
	b, _ := json.Marshal(f)
	var out map[string]any
	json.Unmarshal([]byte(Mask(string(b))), &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func (o *Options) applyCheck(e *Entry, c Check, by string) {
	e.Status, e.Detail, e.Identity = c.Status, Mask(c.Detail), Mask(c.Identity)
	e.Findings = make([]string, 0, len(c.Findings))
	for _, f := range c.Findings {
		e.Findings = append(e.Findings, Mask(f))
	}
	e.Facts = maskFacts(c.Facts)
	e.LastCheck, e.CheckedBy = o.now(), by
	e.Enabled, e.Override = MayEnable(e)
	if e.Managed != "legacy" {
		if _, err := os.Lstat(o.credPath(e.Type, e.Name)); err == nil {
			o.setEnabled(o.credPath(e.Type, e.Name), e.Enabled)
		}
	}
}

// --- migration (D5) -------------------------------------------------------------------------------------------------

var grafanaKeyRe = regexp.MustCompile(`^GRAFANA_([A-Z0-9_]{1,20})_URL$`)

// secretRef finds a credential in the secrets dir, the etc dir or the legacy env file: (ref, where), "" when none.
func (o *Options) secretRef(env map[string]string, secret, etc, envKey string) (string, string) {
	if secret != "" && o.Legacy.SecretsDir != "" && readSecretFile(filepath.Join(o.Legacy.SecretsDir, secret), MaxCred) != "" {
		return "secret:" + secret, "the " + secret + " secret in ARGUS_SECRETS_DIR"
	}
	if etc != "" && o.Legacy.EtcDir != "" && readSecretFile(filepath.Join(o.Legacy.EtcDir, etc), MaxCred) != "" {
		return "etc:" + etc, etc + " in ARGUS_ETC_DIR"
	}
	if envKey != "" && env[envKey] != "" {
		return "env:" + envKey, envKey + " in the legacy env file (ARGUS_SOURCES_LEGACY_ENV)"
	}
	return "", ""
}

func readJSONMap(p string) map[string]any {
	var m map[string]any
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// legacyEntries are the existing integrations found now, by legacy key. No secret is read beyond its presence.
func (o *Options) legacyEntries() map[string]*Entry {
	out := map[string]*Entry{}
	env := o.envFile()
	glURL := o.Legacy.GitLabURL
	if glURL == "" {
		glURL = env["GITLAB_URL"]
	}
	if ref, where := o.secretRef(env, "gitlab-ro-token", "", "GITLAB_RO_TOKEN"); glURL != "" && ref != "" {
		out["gitlab-ro"] = &Entry{Type: "gitlab", Name: "main", Env: "shared", URL: glURL, Purpose: "agent", CredRef: ref,
			CredWhere: where, Manage: "operator", UsedBy: "the agent's GitLab reads, CI/CD checks, repository checks, service docs"}
	}
	if o.Legacy.EtcDir != "" {
		gi := readJSONMap(filepath.Join(o.Legacy.EtcDir, "gitlab-issues.json"))
		if ref, _ := o.secretRef(nil, "gitlab-issues.token", "gitlab-issues.token", ""); str(gi["url"]) != "" && ref != "" {
			raw := str(gi["url"])
			if u, err := url.Parse(raw); err == nil && u.Host != "" {
				raw = u.Scheme + "://" + u.Host
			}
			out["gitlab-tracker"] = &Entry{Type: "gitlab", Name: "tracker", Env: "shared", URL: raw, Purpose: "tracker", CredRef: ref,
				CredWhere: "Settings → Integrations → GitLab issues", Manage: "/admin/outline", UsedBy: "board sync (GitLab issues)"}
		}
		ol := readJSONMap(filepath.Join(o.Legacy.EtcDir, "outline.json"))
		if ref, _ := o.secretRef(nil, "outline.token", "outline.token", ""); str(ol["url"]) != "" && ref != "" {
			out["outline"] = &Entry{Type: "outline", Name: "wiki", Env: "shared", URL: str(ol["url"]), Purpose: "docs", CredRef: ref,
				CredWhere: "Settings → Integrations → Outline", Manage: "/admin/outline", UsedBy: "service docs (publish)"}
		}
	}
	grafana := map[string]string{}
	for k, v := range env {
		if grafanaKeyRe.MatchString(k) {
			grafana[k] = v
		}
	}
	for k, v := range o.Legacy.GrafanaURLs { // the configuration wins over the legacy env file
		if grafanaKeyRe.MatchString(k) {
			grafana[k] = v
		}
	}
	keys := make([]string, 0, len(grafana))
	for k := range grafana {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		id := grafanaKeyRe.FindStringSubmatch(k)[1]
		name := strings.ToLower(strings.ReplaceAll(id, "_", "-"))
		if grafana[k] == "" || !NameRe.MatchString(name) {
			continue
		}
		ref, where := o.secretRef(env, "grafana-"+name+"-token", "", "GRAFANA_"+id+"_TOKEN")
		if ref == "" {
			continue
		}
		out["grafana-"+name] = &Entry{Type: "grafana", Name: name, Env: "release", URL: grafana[k], Purpose: "agent", CredRef: ref,
			CredWhere: where, Manage: "operator", UsedBy: "monitoring reads of site " + name + ": Prometheus, Loki, alert rules"}
	}
	if o.Legacy.KubeDir != "" {
		cl := asMap(readJSONMap(filepath.Join(o.Legacy.KubeDir, "clusters.json"))["clusters"])
		for n, v := range cl {
			if c := asMap(v); c != nil && NameRe.MatchString(n) {
				out["kube-"+n] = kubeEntry(n, c)
			}
		}
	}
	return out
}

func num(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}

// kubeEntry mirrors one cluster of Settings -> Kubernetes; its state comes from that page's last check.
func kubeEntry(n string, c map[string]any) *Entry {
	v := str(c["verdict"])
	t := asMap(c["test"])
	var st, d string
	switch {
	case c["enabled"] == true && v == "read-only":
		st, d = "readonly", "read-only: get, list and watch"
	case v == "writes" || v == "secrets":
		st, d = "refused", "this kubeconfig can write"
		if v == "secrets" {
			d = "this kubeconfig can read Secrets"
		}
		if c["enabled"] == true {
			d += " (enabled with the override)"
		}
	case str(t["result"]) != "" && str(t["result"]) != "ok":
		st, d = "unreachable", "the last check could not reach the cluster"
	default:
		st, d = "unchecked", "not checked yet"
	}
	env := str(c["env"])
	if !in(env, Envs...) {
		env = "release"
	}
	u := ""
	if h := str(c["server_host"]); h != "" {
		u = "https://" + cut(h, 120)
	}
	var findings []string
	if l, ok := c["findings"].([]any); ok {
		for _, f := range l {
			if len(findings) < 8 {
				findings = append(findings, cut(str(f), 160))
			}
		}
	}
	e := &Entry{Type: "kubernetes", Name: n, Env: env, URL: u, Purpose: "agent", CredRef: "kube:" + n,
		CredWhere: "Settings → Kubernetes", Manage: "/admin/kube", UsedBy: "argus tools kube " + n, Status: st, Detail: d,
		Identity: cut(str(c["user"]), 80), Findings: findings, Facts: map[string]any{"context": cut(str(c["context"]), 120)},
		LastCheck: num(t["at"]), Enabled: c["enabled"] == true, Override: c["override"] == true, CreatedBy: str(c["set_by"]),
		CreatedAt: num(c["set_at"])}
	e.normalise()
	return e
}

// Migrate mirrors the legacy integrations into sources (idempotent) and returns the keys added (+) or removed (-).
func (o *Options) Migrate(sources map[string]*Entry) []string {
	found := o.legacyEntries()
	var changed []string
	byLegacy := map[string]string{}
	for k, e := range sources {
		if e.Managed == "legacy" {
			byLegacy[e.Legacy] = k
		}
	}
	lks := make([]string, 0, len(found))
	for lk := range found {
		lks = append(lks, lk)
	}
	sort.Strings(lks)
	for _, lk := range lks {
		n := found[lk]
		key, ok := byLegacy[lk]
		if !ok {
			key = n.key()
			if _, taken := sources[key]; taken { // an admin already uses that name: the legacy one takes a suffix
				n.Name = cut(n.Name, 26) + "-env"
				key = n.key()
			}
			n.Legacy, n.Managed = lk, "legacy"
			if n.Type != "kubernetes" {
				n.CreatedBy, n.CreatedAt = "migration", o.now()
				n.Status, n.Detail = "unchecked", "not checked yet"
			}
			n.normalise()
			sources[key] = n
			changed = append(changed, "+"+key)
			continue
		}
		e := sources[key]
		if n.Type == "kubernetes" { // the mirror's state comes from Settings -> Kubernetes
			name := e.Name
			*e = *n
			e.Name, e.Legacy, e.Managed = name, lk, "legacy"
		} else { // others keep their own check and history
			e.Env, e.URL, e.Purpose, e.CredRef, e.CredWhere, e.Manage, e.UsedBy = n.Env, n.URL, n.Purpose, n.CredRef, n.CredWhere, n.Manage, n.UsedBy
		}
		e.normalise()
	}
	for lk, key := range byLegacy {
		if _, ok := found[lk]; !ok {
			delete(sources, key)
			changed = append(changed, "-"+key)
		}
	}
	sort.Strings(changed)
	return changed
}

// --- requests -------------------------------------------------------------------------------------------------------

func (o *Options) userRole(name string) string {
	db, err := sql.Open("sqlite", "file:"+o.AuthDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var role string
	var disabled int
	if db.QueryRow("SELECT role, disabled FROM users WHERE username=?", name).Scan(&role, &disabled) != nil || disabled != 0 {
		return ""
	}
	return role
}

// readRequest is the request's JSON; the file is wiped and deleted whatever happens.
func (o *Options) readRequest(path string) (map[string]any, error) {
	defer os.Remove(path)
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, refusal("cannot open request")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, refusal("request is not a regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != o.AppUID && int(sys.Uid) != os.Geteuid() {
		return nil, refusal("request not written by the app")
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxRequest+1))
	f.WriteAt(make([]byte, len(data)), 0)
	f.Sync()
	if len(data) > maxRequest {
		return nil, refusal("request file too large")
	}
	var req map[string]any
	if json.Unmarshal(data, &req) != nil || req == nil {
		return nil, refusal("request is not a JSON object")
	}
	return req, nil
}

func last4(v string) string {
	if len(v) >= 12 {
		return v[len(v)-4:]
	}
	return ""
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (o *Options) checkAll(sources map[string]*Entry, by string) int {
	keys := make([]string, 0, len(sources))
	for k := range sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 0
	for _, k := range keys {
		if e := sources[k]; checks[e.Type] != nil {
			o.applyCheck(e, o.RunCheck(e), by)
			n++
		}
	}
	return n
}

func (o *Options) handle(req map[string]any, usr string, sources map[string]*Entry) (string, error) {
	kind, _ := req["kind"].(string)
	if kind == "check-all" {
		n := o.checkAll(sources, usr)
		o.audit(fmt.Sprintf("%s: data sources checked (%d)", usr, n))
		return fmt.Sprintf("checked %d sources", n), nil
	}
	typ, _ := req["type"].(string)
	name, _ := req["name"].(string)
	t, ok := Types[typ]
	if !ok {
		return "", refusal("unknown data source type")
	}
	if !NameRe.MatchString(name) {
		return "", refusal("the name must be lowercase letters, digits and -, up to 31 characters")
	}
	key := typ + "/" + name
	cur := sources[key]
	if t.Managed != "" {
		return "", refusal(t.Label + " sources are added and changed in Settings → " + t.Label)
	}
	if (kind == "test" || kind == "remove") && cur == nil {
		return "", refusal("there is no " + key)
	}
	switch kind {
	case "test":
		o.applyCheck(cur, o.RunCheck(cur), usr)
		o.audit(fmt.Sprintf("%s: data source %s checked: %s", usr, key, cur.Status))
		return fmt.Sprintf("tested %s: %s (%s)", key, cur.Status, cur.Detail), nil
	case "remove":
		if cur.Managed == "legacy" {
			return "", refusal(key + " is still read by Argus's jobs from " + cur.CredWhere + "; remove it there")
		}
		b, err := o.backup(typ, name)
		if err != nil {
			return "", err
		}
		os.Remove(o.credPath(typ, name))
		delete(sources, key)
		o.audit(fmt.Sprintf("%s: data source %s removed; backup %s", usr, key, dash(b)))
		return fmt.Sprintf("removed %s; backup %s", key, dash(b)), nil
	case "save":
	default:
		return "", refusal("unknown request kind")
	}
	mode, _ := req["mode"].(string)
	if mode == "add" && cur != nil {
		return "", refusal(fmt.Sprintf("a %s source named %s exists; pick another name or edit it", t.Label, name))
	}
	if mode == "edit" && cur == nil {
		return "", refusal("there is no " + key)
	}
	if mode != "add" && mode != "edit" {
		return "", refusal("unknown save mode")
	}
	if cur != nil && cur.Managed == "legacy" {
		return "", refusal(key + " is read from " + cur.CredWhere + " until jobs move to the registry; change it there")
	}
	env, _ := req["env"].(string)
	purpose, _ := req["purpose"].(string)
	if !in(env, Envs...) {
		return "", refusal("choose develop, pre-release, release or shared")
	}
	if !in(purpose, t.Purposes...) {
		return "", refusal(fmt.Sprintf("a %s source cannot have purpose %s", t.Label, cut(purpose, 20)))
	}
	if purpose != "agent" && env == "release" {
		return "", refusal("release sources are for the agent only, read-only: production is never written")
	}
	override := req["override"] == true
	if override && !in(env, "develop", "pre-release") {
		return "", refusal("release and shared sources never take the override: they must be read-only")
	}
	rawURL, _ := req["url"].(string)
	u, err := CleanURL(rawURL)
	if err != nil {
		return "", err
	}
	username, _ := req["username"].(string)
	username = strings.TrimSpace(username)
	if username != "" && (!t.Username || !UserRe.MatchString(username)) {
		return "", refusal("the username has characters a login does not use")
	}
	cred, _ := req["credential"].(string)
	req["credential"] = nil
	cred = strings.TrimSpace(cred)
	if len(cred) > MaxCred || strings.IndexFunc(cred, func(r rune) bool { return r < 32 }) >= 0 {
		return "", refusal("the credential is too long or has line breaks")
	}
	if cred == "" && cur == nil && !t.NoCred {
		return "", refusal("paste the token or key")
	}
	e := &Entry{}
	if cur != nil {
		c := *cur
		e = &c
	}
	e.Type, e.Name, e.Env, e.URL, e.Purpose, e.Username = typ, name, env, u, purpose, username
	e.OverrideRequested, e.Managed, e.UpdatedBy, e.UpdatedAt = override, "registry", usr, o.now()
	if e.CreatedBy == "" {
		e.CreatedBy, e.CreatedAt = usr, o.now()
	}
	b := ""
	if cred != "" {
		if b, err = o.backup(typ, name); err != nil {
			return "", err
		}
		if err := o.writeCred(typ, name, cred); err != nil {
			return "", err
		}
		e.CredLast4, e.CredSetAt, e.CredSetBy = last4(cred), o.now(), usr
	}
	cred = ""
	e.normalise()
	sources[key] = e
	o.applyCheck(e, o.RunCheck(e), usr)
	host := ""
	if pu, err := url.Parse(u); err == nil {
		host = pu.Host
	}
	en := ""
	if e.Enabled {
		en = "; enabled for the agent"
	}
	o.audit(fmt.Sprintf("%s: data source %s (%s, %s, %s) %s: %s%s; backup %s", usr, key, env, purpose, host, mode, e.Status, en, dash(b)))
	return fmt.Sprintf("saved %s: %s (%s)", key, e.Status, e.Detail), nil
}

// Status is <settings-out>/sources-status.json.
type Status struct {
	Sources map[string]*Entry         `json:"sources"`
	Types   map[string]Type           `json:"types"`
	Results map[string]map[string]any `json:"results"`
	At      int64                     `json:"at"`
}

// Publish writes the status file: the sources, the types and the last 20 request results (masked).
func (o *Options) Publish(results map[string]map[string]any, sources map[string]*Entry) error {
	path := filepath.Join(o.OutDir, "sources-status.json")
	var old Status
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &old)
	}
	if sources == nil {
		sources = o.Registry()
	}
	res := old.Results
	if res == nil {
		res = map[string]map[string]any{}
	}
	for k, v := range results {
		res[k] = v
	}
	if len(res) > keepResults {
		keys := make([]string, 0, len(res))
		for k := range res {
			keys = append(keys, k)
		}
		at := func(k string) float64 {
			f, _ := res[k]["at"].(float64)
			i, _ := res[k]["at"].(int64)
			return f + float64(i)
		}
		sort.Slice(keys, func(i, j int) bool { return at(keys[i]) < at(keys[j]) })
		for _, k := range keys[:len(keys)-keepResults] {
			delete(res, k)
		}
	}
	for _, e := range sources {
		e.normalise()
	}
	b, err := json.Marshal(Status{Sources: sources, Types: Types, Results: res, At: o.now()})
	if err != nil {
		return err
	}
	var masked any
	json.Unmarshal([]byte(Mask(string(b))), &masked)
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	return writeJSON(path, masked, 0o644)
}

// Apply migrates, processes the waiting requests and publishes the status.
func Apply(o Options) error {
	entries, _ := os.ReadDir(o.ReqDir)
	sources := o.Registry()
	o.Migrate(sources)
	if err := o.saveRegistry(sources); err != nil { // the kube mirror may have changed even when no entry came or went
		return err
	}
	results := map[string]map[string]any{}
	for _, de := range entries {
		fname := de.Name()
		p := filepath.Join(o.ReqDir, fname)
		m := reqRe.FindStringSubmatch(fname)
		if m == nil {
			if !strings.HasPrefix(fname, ".") {
				os.Remove(p)
			}
			continue
		}
		usr, rid := m[1], m[2]
		var res string
		req, err := o.readRequest(p)
		if err == nil {
			switch {
			case req["user"] != usr:
				err = refusal("request user does not match its file name")
			case o.userRole(usr) != "admin":
				err = refusal("only an enabled admin can do this")
			}
		}
		if err == nil {
			res, err = o.handle(req, usr, sources)
			if err == nil {
				err = o.saveRegistry(sources)
			}
		}
		var rf refusal
		switch {
		case errors.As(err, &rf):
			res = "refused: " + string(rf)
		case err != nil:
			res = "refused: could not save (" + cut(err.Error(), 120) + ")"
		}
		res = cut(Mask(res), 400)
		var kind, typ, name string
		if req != nil {
			req["credential"] = nil
			kind, _ = req["kind"].(string)
			typ, _ = req["type"].(string)
			name, _ = req["name"].(string)
		}
		results[rid] = map[string]any{"user": usr, "kind": cut(kind, 20), "type": cut(typ, 20), "name": cut(name, 31),
			"result": res, "at": o.now()}
		if o.Log != nil {
			fmt.Fprintf(o.Log, "sources request %s %s by %s: %s\n", rid, dash(cut(kind, 20)), usr, cut(res, 200))
		}
	}
	return o.Publish(results, sources)
}

// CheckAll migrates, checks every source and publishes the status (the timer).
func CheckAll(o Options) error {
	sources := o.Registry()
	o.Migrate(sources)
	o.checkAll(sources, "timer")
	if err := o.saveRegistry(sources); err != nil {
		return err
	}
	if err := o.Publish(nil, sources); err != nil {
		return err
	}
	var bad []string
	for k, e := range sources {
		if e.Purpose == "agent" && in(e.Status, "failed", "unreachable", "refused") {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	if o.Log != nil {
		line := fmt.Sprintf("data sources: %d checked", len(sources))
		if len(bad) > 0 {
			line += ", attention: " + strings.Join(bad, ", ")
		}
		fmt.Fprintln(o.Log, line)
	}
	return nil
}
