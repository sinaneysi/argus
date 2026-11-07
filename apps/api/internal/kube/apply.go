package kube

// apply.go: the root side (argus secrets apply-kube, run by argus-kube.path). The web app (no access to any secret)
// writes one request per action into <settings-in>/kube/<user>-<16 hex>.json (0600):
//
//	{"kind": "save", "name": "dev", "env": "develop", "context": "", "kubeconfig": "...", "override": false, "user", "at"}
//	{"kind": "test", "name": "dev"}       check the stored kubeconfig again
//	{"kind": "remove", "name": "dev"}     back it up and remove it
//
// Apply reads each request once (no symlinks, a regular file written by the app, size-capped), wipes and deletes it,
// checks that the user is an enabled admin, and then for a save: converts YAML to JSON with `kubectl config view
// --raw -o json` (as nobody; config view never runs a plugin), validates it (config.go), keeps only the chosen context,
// checks it with kubectl as nobody in a private temp dir (version, auth can-i --list, the cluster-wide probes, get ns;
// 15 s each), backs up the previous file (<kube dir>/backup/<name>.kubeconfig.<UTC ts>, 0600, last 5) and installs
// the new one atomically. Read-only: 0640 root:<agent group> and enabled in clusters.json; otherwise 0600 root:root and
// refused for the agent, unless the admin ticked the override and the environment is not release. Status:
// <settings-out>/kube-status.json; one audit line per action. The kubeconfig is written nowhere else.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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

const (
	keepBackups = 5
	maxRequest  = 400 * 1024
	stepTimeout = 15 * time.Second
	nobody      = 65534
)

var (
	reqRe     = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._@-]{0,63})-([0-9a-f]{16})\.json$`)
	secretish = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*|[A-Za-z0-9+/=_-]{40,}`)
	verRe     = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	spaceRe   = regexp.MustCompile(`\s+`)
)

// Options say where everything is.
type Options struct {
	ReqDir     string // <settings-in>/kube
	OutDir     string // <settings-out>
	AuthDB     string
	KubeDir    string // where kubeconfigs live (root:<group> 0750)
	Kubectl    string // the pinned kubectl
	Version    string // pinned version, sha256 and download URL ({version} is replaced)
	SHA256     string
	URL        string
	AgentGroup string // the group whose members (the agent) may read an enabled kubeconfig
	AppUID     int
	CheckUID   int // the uid kubectl runs as for the check when root (0: nobody; -1: the caller, tests)
	Now        func() time.Time
	Log        io.Writer
	HTTP       *http.Client
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

type refusal string

func (r refusal) Error() string { return string(r) }

// Mask hides anything token-like.
func Mask(s string) string { return secretish.ReplaceAllString(s, "[REDACTED]") }

func isRoot() bool { return os.Geteuid() == 0 }

// Entry is one cluster in clusters.json and kube-status.json (never a secret).
type Entry struct {
	Summary
	Env               string         `json:"env"`
	OverrideRequested bool           `json:"override_requested"`
	SetBy             string         `json:"set_by"`
	SetAt             int64          `json:"set_at"`
	Backup            string         `json:"backup"`
	Verdict           string         `json:"verdict"`
	Findings          []string       `json:"findings"`
	Enabled           bool           `json:"enabled"`
	Override          bool           `json:"override"`
	Why               string         `json:"why"`
	Test              map[string]any `json:"test"`
}

// --- kubectl -------------------------------------------------------------------------------------------------------

// KubectlState is what the page shows about the binary.
func (o *Options) KubectlState() map[string]any {
	st := map[string]any{"path": o.Kubectl, "version": o.Version, "installed": false}
	fi, err := os.Stat(o.Kubectl)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return st
	}
	st["installed"] = true
	if o.SHA256 != "" {
		if sum, err := fileSHA(o.Kubectl); err == nil {
			st["verified"] = sum == o.SHA256
		}
	}
	return st
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// InstallKubectl puts the pinned kubectl in place, verified by its sha256; a failed download keeps the old binary.
func (o *Options) InstallKubectl() map[string]any {
	if !verRe.MatchString(o.Version) || !sha256Re.MatchString(o.SHA256) || !strings.HasPrefix(o.URL, "https://") {
		st := o.KubectlState()
		st["error"] = "no valid kubectl pin (version, sha256, https url) in the configuration"
		return st
	}
	if st := o.KubectlState(); st["verified"] == true {
		return st
	}
	fail := func(err error) map[string]any {
		st := o.KubectlState()
		st["error"] = "could not install kubectl " + o.Version + ": " + cut(err.Error(), 200)
		return st
	}
	if err := os.MkdirAll(filepath.Dir(o.Kubectl), 0o755); err != nil {
		return fail(err)
	}
	c := o.HTTP
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := c.Get(strings.ReplaceAll(o.URL, "{version}", o.Version))
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fail(fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	tmp, err := os.CreateTemp(filepath.Dir(o.Kubectl), ".kubectl.")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 300<<20)); err != nil {
		tmp.Close()
		return fail(err)
	}
	tmp.Close()
	if hex.EncodeToString(h.Sum(nil)) != o.SHA256 {
		return fail(errors.New("sha256 mismatch: the download is not the pinned kubectl"))
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), o.Kubectl); err != nil {
		return fail(err)
	}
	o.audit("kubectl " + o.Version + " installed at " + o.Kubectl + " (sha256 verified)")
	return o.KubectlState()
}

type stepResult struct {
	rc     int // -1: timeout or could not start
	stdout string
	stderr string
}

// runKubectl runs each argv with cfg as the only kubeconfig, as nobody when root, in a private temp dir.
func (o *Options) runKubectl(cfg string, steps [][]string) []stepResult {
	out := make([]stepResult, 0, len(steps))
	dir, err := os.MkdirTemp("", "kube-check.")
	if err != nil {
		for range steps {
			out = append(out, stepResult{-1, "", "cannot create a temp dir"})
		}
		return out
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config")
	os.WriteFile(path, []byte(cfg), 0o600)
	os.Mkdir(filepath.Join(dir, "cache"), 0o700)
	uid := o.CheckUID
	if uid == 0 {
		uid = nobody
	}
	drop := isRoot() && uid > 0 && uid != os.Geteuid() // CheckUID -1: run as the caller (tests)
	if drop {
		filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error { return os.Lchown(p, uid, uid) })
	}
	base := []string{"--kubeconfig", path, "--cache-dir", filepath.Join(dir, "cache"), "--request-timeout=10s"}
	for _, argv := range steps {
		ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
		cmd := exec.CommandContext(ctx, o.Kubectl, append(append([]string{}, base...), argv...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "LANG=C.UTF-8"}
		cmd.Dir = dir
		if drop {
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(uid), Groups: []uint32{}}}
		}
		var so, se strings.Builder
		cmd.Stdout, cmd.Stderr = &limited{&so, 200000}, &limited{&se, 4000}
		err := cmd.Run()
		rc := 0
		if ctx.Err() != nil {
			rc, se = -1, strings.Builder{}
			se.WriteString(fmt.Sprintf("no answer within %d s", int(stepTimeout.Seconds())))
		} else if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				rc = ee.ExitCode()
			} else {
				rc = -1
				se.WriteString("cannot start kubectl")
			}
		}
		cancel()
		out = append(out, stepResult{rc, so.String(), se.String()})
	}
	return out
}

type limited struct {
	b *strings.Builder
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func first(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return cut(Mask(spaceRe.ReplaceAllString(l, " ")), 240)
		}
	}
	return ""
}

// toJSON converts a YAML kubeconfig to JSON with kubectl itself (config view reads and prints; it runs nothing).
func (o *Options) toJSON(text string) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(text), "{") {
		return text, nil
	}
	if o.KubectlState()["installed"] != true {
		return "", Invalid("kubectl is not installed on this host yet, so YAML cannot be read; paste the kubeconfig as JSON")
	}
	r := o.runKubectl(text, [][]string{{"config", "view", "--raw", "-o", "json"}})[0]
	if r.rc != 0 {
		return "", Invalid("the kubeconfig is not valid YAML")
	}
	return r.stdout, nil
}

// Check is the read-only verdict of a kubeconfig (JSON text).
func (o *Options) Check(cfg, namespace string) map[string]any {
	if o.KubectlState()["installed"] != true {
		return map[string]any{"result": "error", "verdict": "unknown", "findings": []string{},
			"message": "kubectl is not installed on this host yet"}
	}
	if namespace == "" {
		namespace = "default"
	}
	steps := [][]string{{"version", "-o", "json"}, {"auth", "can-i", "--list", "-n", namespace}}
	for _, p := range Probes {
		steps = append(steps, []string{"auth", "can-i", p.Verb, p.Resource, "--all-namespaces"})
	}
	steps = append(steps, []string{"get", "namespaces", "-o", "name"})
	res := o.runKubectl(cfg, steps)
	doc := map[string]any{"result": "ok", "findings": []string{}, "message": ""}
	if res[0].rc != 0 {
		m := first(res[0].stderr)
		if m == "" {
			m = "the cluster did not answer"
		}
		return map[string]any{"result": "error", "verdict": "unknown", "findings": []string{}, "message": m}
	}
	var ver struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	json.Unmarshal([]byte(res[0].stdout), &ver)
	doc["server_version"] = cut(ver.ServerVersion.GitVersion, 40)
	if res[1].rc != 0 {
		doc["result"], doc["verdict"], doc["message"] = "error", "unknown", "auth can-i --list failed: "+first(res[1].stderr)
		return doc
	}
	probes := map[Probe]bool{}
	for i, p := range Probes {
		r := res[2+i]
		probes[p] = r.rc == 0 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.stdout)), "yes")
	}
	v := Classify(res[1].stdout, probes)
	doc["verdict"], doc["findings"] = v.Verdict, v.Findings
	if v.Verdict == "unknown" {
		doc["result"], doc["message"] = "error", "could not tell what this login may do"
	}
	last := res[len(res)-1]
	if last.rc == 0 {
		n := 0
		for _, l := range strings.Split(last.stdout, "\n") {
			if strings.HasPrefix(l, "namespace/") {
				n++
			}
		}
		doc["namespaces"] = n
	} else {
		doc["namespaces_error"] = first(last.stderr)
	}
	return doc
}

// --- the stored files ----------------------------------------------------------------------------------------------

func (o *Options) path(name string) string { return filepath.Join(o.KubeDir, name+".kubeconfig") }

func (o *Options) gid() int {
	g, err := user.LookupGroup(o.AgentGroup)
	if err != nil {
		return -1
	}
	n, _ := strconv.Atoi(g.Gid)
	return n
}

func (o *Options) ensureDirs() error {
	if err := os.MkdirAll(filepath.Join(o.KubeDir, "backup"), 0o700); err != nil {
		return err
	}
	if g := o.gid(); isRoot() && g >= 0 {
		os.Chown(o.KubeDir, 0, g)
	}
	os.Chmod(o.KubeDir, 0o750)
	return os.Chmod(filepath.Join(o.KubeDir, "backup"), 0o700)
}

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

func (o *Options) backup(name string) (string, error) {
	src := o.path(name)
	data, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	ts := o.now().UTC().Format("20060102-150405")
	dst := filepath.Join(o.KubeDir, "backup", name+".kubeconfig."+ts)
	for n := 1; ; n++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = filepath.Join(o.KubeDir, "backup", fmt.Sprintf("%s.kubeconfig.%s-%d", name, ts, n))
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	olds, _ := filepath.Glob(filepath.Join(o.KubeDir, "backup", name+".kubeconfig.*"))
	sort.Slice(olds, func(i, j int) bool {
		a, _ := os.Stat(olds[i])
		b, _ := os.Stat(olds[j])
		if a == nil || b == nil || a.ModTime().Equal(b.ModTime()) {
			return olds[i] < olds[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	for len(olds) > keepBackups {
		os.Remove(olds[0])
		olds = olds[1:]
	}
	return filepath.Base(dst), nil
}

func (o *Options) install(name, text string) error {
	tmp := filepath.Join(o.KubeDir, fmt.Sprintf(".%s.%d.new", name, os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, o.path(name))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Registry is <kube dir>/clusters.json.
func (o *Options) Registry() map[string]*Entry {
	var doc struct {
		Clusters map[string]*Entry `json:"clusters"`
	}
	if b, err := os.ReadFile(filepath.Join(o.KubeDir, "clusters.json")); err == nil {
		json.Unmarshal(b, &doc)
	}
	if doc.Clusters == nil {
		doc.Clusters = map[string]*Entry{}
	}
	return doc.Clusters
}

func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, append(b, '\n'), mode); err != nil {
		return err
	}
	os.Chmod(tmp, mode)
	return os.Rename(tmp, path)
}

func (o *Options) saveRegistry(c map[string]*Entry) error {
	return writeJSON(filepath.Join(o.KubeDir, "clusters.json"), map[string]any{"clusters": c, "at": o.now().Unix()}, 0o644)
}

func (o *Options) applyVerdict(e *Entry, chk map[string]any, by string) bool {
	e.Verdict, _ = chk["verdict"].(string)
	if e.Verdict == "" {
		e.Verdict = "unknown"
	}
	e.Findings, _ = chk["findings"].([]string)
	if e.Findings == nil {
		e.Findings = []string{}
	}
	e.Test = map[string]any{"at": o.now().Unix(), "by": by}
	for _, k := range []string{"result", "message", "server_version", "namespaces", "namespaces_error"} {
		if v, ok := chk[k]; ok && v != nil {
			e.Test[k] = v
		}
	}
	e.Enabled, e.Override, e.Why = MayEnable(e.Env, e.Verdict, e.OverrideRequested)
	return e.Enabled
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

func (o *Options) audit(line string) {
	f, err := os.OpenFile(filepath.Join(o.OutDir, "audit.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", o.now().Format("2006-01-02 15:04:05"), Mask(line))
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "not enabled"
}

func (o *Options) handle(req map[string]any, usr string, clusters map[string]*Entry) (string, error) {
	kind, _ := req["kind"].(string)
	name, _ := req["name"].(string)
	if !NameRe.MatchString(name) {
		return "", refusal("the cluster name must be lowercase letters, digits and -, up to 31 characters")
	}
	if err := o.ensureDirs(); err != nil {
		return "", err
	}
	switch kind {
	case "remove":
		if _, err := os.Stat(o.path(name)); clusters[name] == nil && err != nil {
			return "", refusal("there is no cluster " + name)
		}
		b, err := o.backup(name)
		if err != nil {
			return "", err
		}
		os.Remove(o.path(name))
		delete(clusters, name)
		if b == "" {
			b = "-"
		}
		o.audit(fmt.Sprintf("%s: kubeconfig %s removed; backup %s", usr, name, b))
		return fmt.Sprintf("removed %s; backup %s", name, b), nil
	case "test":
		e := clusters[name]
		data, err := os.ReadFile(o.path(name))
		if e == nil || err != nil {
			return "", refusal("there is no kubeconfig for " + name)
		}
		on := o.applyVerdict(e, o.Check(string(data), e.Namespace), usr)
		if err := o.setEnabled(o.path(name), on); err != nil {
			return "", err
		}
		o.audit(fmt.Sprintf("%s: kubeconfig %s tested: %s; %s (%s)", usr, name, e.Verdict, onOff(on), e.Why))
		return fmt.Sprintf("tested %s: %s; %s (%s)", name, e.Verdict, onOff(on), e.Why), nil
	case "save":
	default:
		return "", refusal("unknown request kind")
	}
	env, _ := req["env"].(string)
	ctx, _ := req["context"].(string)
	in := false
	for _, x := range Envs {
		in = in || x == env
	}
	if !in {
		return "", refusal("choose develop, pre-release or release")
	}
	if ctx != "" && !ContextRe.MatchString(ctx) {
		return "", refusal("the context name has characters a kubeconfig context does not use")
	}
	override := req["override"] == true
	if override && env == "release" {
		return "", refusal("release never takes the override: production stays read-only")
	}
	text, _ := req["kubeconfig"].(string)
	req["kubeconfig"] = nil
	if len(text) > MaxBytes {
		return "", refusal(fmt.Sprintf("the kubeconfig is larger than %d KB", MaxBytes/1024))
	}
	js, err := o.toJSON(text)
	text = ""
	if err != nil {
		return "", refusal(err.Error())
	}
	doc, err := ParseJSON(js)
	if err != nil {
		return "", refusal(err.Error())
	}
	s, err := Validate(doc, ctx)
	if err != nil {
		return "", refusal(err.Error())
	}
	b, _ := json.MarshalIndent(Minimal(doc, s), "", " ")
	mini := string(b) + "\n"
	chk := o.Check(mini, s.Namespace)
	bk, err := o.backup(name)
	if err != nil {
		return "", err
	}
	if err := o.install(name, mini); err != nil {
		return "", err
	}
	e := &Entry{Summary: s, Env: env, OverrideRequested: override, SetBy: usr, SetAt: o.now().Unix(), Backup: bk}
	on := o.applyVerdict(e, chk, usr)
	if err := o.setEnabled(o.path(name), on); err != nil {
		return "", err
	}
	clusters[name] = e
	if bk == "" {
		bk = "-"
	}
	o.audit(fmt.Sprintf("%s: kubeconfig %s (%s, %s, user %s) saved: %s; %s (%s); backup %s", usr, name, env, s.ServerHost,
		s.User, e.Verdict, onOff(on), e.Why, bk))
	return fmt.Sprintf("saved %s: %s; %s (%s)", name, e.Verdict, onOff(on), e.Why), nil
}

// Status is <settings-out>/kube-status.json.
type Status struct {
	Clusters map[string]*Entry         `json:"clusters"`
	Kubectl  map[string]any            `json:"kubectl"`
	Results  map[string]map[string]any `json:"results"`
	At       int64                     `json:"at"`
}

// Publish writes the status file (kubectl: the install result to show, nil = the binary as it is now).
func (o *Options) Publish(results map[string]map[string]any, kubectl map[string]any) error {
	path := filepath.Join(o.OutDir, "kube-status.json")
	var old Status
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &old)
	}
	if kubectl == nil {
		kubectl = o.KubectlState()
		if kubectl["installed"] != true && old.Kubectl != nil && old.Kubectl["error"] != nil {
			kubectl["error"] = old.Kubectl["error"]
		}
	}
	res := old.Results
	if res == nil {
		res = map[string]map[string]any{}
	}
	for k, v := range results {
		res[k] = v
	}
	if len(res) > 20 {
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
		for _, k := range keys[:len(keys)-20] {
			delete(res, k)
		}
	}
	st := Status{Clusters: o.Registry(), Kubectl: kubectl, Results: res, At: o.now().Unix()}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	var masked any
	json.Unmarshal([]byte(Mask(string(b))), &masked)
	return writeJSON(path, masked, 0o644)
}

// Apply processes the waiting requests and publishes the status.
func Apply(o Options) error {
	entries, _ := os.ReadDir(o.ReqDir)
	clusters := o.Registry()
	results := map[string]map[string]any{}
	var kst map[string]any
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
		var result, kind, name string
		req, err := o.readRequest(p)
		if err == nil {
			kind, _ = req["kind"].(string)
			name, _ = req["name"].(string)
			switch {
			case req["user"] != usr:
				err = refusal("request user does not match its file name")
			case o.userRole(usr) != "admin":
				err = refusal("only an enabled admin can do this")
			}
		}
		if err == nil {
			if (kind == "save" || kind == "test") && o.KubectlState()["verified"] != true {
				kst = o.InstallKubectl()
			}
			result, err = o.handle(req, usr, clusters)
			if err == nil {
				err = o.saveRegistry(clusters)
			}
		}
		if req != nil {
			req["kubeconfig"] = nil
		}
		var rf refusal
		switch {
		case errors.As(err, &rf):
			result = "refused: " + string(rf)
		case err != nil:
			result = "refused: could not save (" + cut(err.Error(), 120) + ")"
		}
		result = cut(Mask(result), 400)
		results[rid] = map[string]any{"user": usr, "kind": cut(kind, 20), "name": cut(name, 31), "result": result, "at": o.now().Unix()}
		if o.Log != nil {
			fmt.Fprintf(o.Log, "kube request %s %s by %s: %s\n", rid, cut(kind, 20), usr, cut(result, 200))
		}
	}
	return o.Publish(results, kst)
}
