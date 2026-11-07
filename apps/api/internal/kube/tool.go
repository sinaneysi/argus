package kube

// tool.go: the agent's read-only Kubernetes tool (argus tools kube). One fixed kubectl read per call:
//
//	argus tools kube list                          the clusters the agent may use and the command catalogue
//	argus tools kube <cluster> <command> [args]    get, describe, logs, top, events, api-resources, version, can-i
//
// The cluster must be in root's clusters.json with "enabled": true (root sets it only for a kubeconfig whose check
// came back read-only, or overridden by an admin outside release); its kubeconfig is root-owned and readable by the
// agent's group only while enabled. Every argument is checked against a per-command flag list before kubectl starts.
// Never: Secrets, ConfigMap contents (names only), exec, cp, port-forward, proxy, attach, edit, apply, create, delete,
// patch, replace, scale, rollout, drain, cordon, label, annotate, --raw, --watch, another kubeconfig, context,
// server, token or impersonation. Output is redacted and capped; 20 s request timeout, 45 s overall; one audit line
// per call, refused or not.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

// Tool says where the tool finds things (fixed by the configuration, never by the caller's arguments).
type Tool struct {
	KubeDir  string
	Kubectl  string
	AuditLog string
	CacheDir string
	OwnerUID int // the owner clusters.json and the kubeconfigs must have (0: root)
	Host     string
	Timeout  time.Duration
	Now      func() time.Time
}

const (
	maxLines    = 400
	maxLogLines = 500
	maxBytes    = 65536
	maxTail     = 500
	defaultTail = 200
	maxSince    = 6 * time.Hour
)

var (
	objRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$`)
	typeRe  = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,80}$`)
	nsRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	selRe   = regexp.MustCompile(`^[A-Za-z0-9._/=!,()\s-]{1,200}$`)
	fselRe  = regexp.MustCompile(`^[A-Za-z0-9._/=!,-]{1,200}$`)
	sortRe  = regexp.MustCompile(`^[A-Za-z0-9._\[\]{}-]{1,100}$`)
	durRe   = regexp.MustCompile(`^(\d{1,5})(s|m|h)$`)
	forRe   = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,60}/[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$`)
	typesRe = regexp.MustCompile(`^(Warning|Normal)(,(Warning|Normal))?$`)
	groupRe = regexp.MustCompile(`^[a-z0-9.-]{0,100}$`)
	cleanRe = regexp.MustCompile(`[^A-Za-z0-9@._/:=,+-]`)
	wordsRe = regexp.MustCompile(`[^A-Za-z0-9@._/:=,+ -]`)
	neverFl = set("--raw", "--kubeconfig", "--context", "--cluster", "--server", "-s", "--token", "--user", "--username",
		"--password", "--certificate-authority", "--client-certificate", "--client-key", "--insecure-skip-tls-verify",
		"--watch", "-w", "--watch-only", "-f", "--filename", "-k", "--kustomize", "--follow", "--cache-dir",
		"--request-timeout", "--template", "--profile", "--profile-output")
	secretTy = set("secret", "secrets")
	cmTy     = set("cm", "configmap", "configmaps")
	logKinds = set("pod", "pods", "po", "deployment", "deployments", "deploy", "statefulset", "statefulsets", "sts",
		"daemonset", "daemonsets", "ds", "job", "jobs", "replicaset", "replicasets", "rs")
)

// Usage is the catalogue the agent sees.
const Usage = `usage: argus tools kube list
       argus tools kube <cluster> <command> [args]
commands (read-only; anything else is refused):
  get <type>[,<type>] [name...] [-n ns | -A] [-l selector] [--field-selector f] [-o wide|name|json|yaml]
                          [--show-labels] [--sort-by path]
  describe <type> [name] [-n ns | -A] [-l selector]
  logs <pod | type/name> [-c container | --all-containers] [-n ns] [--tail N (<= 500, default 200)]
                          [--since 30m (<= 6h)] [--previous] [--timestamps] [-l selector]
  top pods|nodes [name] [-n ns | -A] [-l selector] [--containers] [--sort-by cpu|memory]
  events [-n ns | -A] [--for kind/name] [--types Warning,Normal]
  api-resources [--namespaced=true|false] [--api-group g] [-o wide|name]
  version
  can-i [-n ns]           what this login may do (auth can-i --list)
never: Secrets (any form), ConfigMap contents (names only: get configmaps [-o name|wide]), exec, cp, port-forward,
       proxy, attach, debug, edit, apply, create, delete, patch, replace, scale, rollout, drain, cordon, label,
       annotate, --raw, --watch/-w, --kubeconfig/--context/--server/--token, impersonation.
`

func clean(s string, n int) string { return cleanRe.ReplaceAllString(cut(s, n), "?") }
func words(s string, n int) string { return wordsRe.ReplaceAllString(cut(s, n), "?") }

// --- redaction ------------------------------------------------------------------------------------------------------

type rx struct {
	re  *regexp.Regexp
	rep string
}

var redactions = []rx{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|\z)`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`(?i)((?:proxy-)?authorization)([" ]*[:=][" ]*)[^"\n]*`), "${1}${2}[REDACTED]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic|token)[ \t]+[A-Za-z0-9._~+/=-]{8,}`), "${1} [REDACTED]"},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/:@\s"]+:[^@\s"]+@`), "${1}[REDACTED]@"},
	{regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:passw(?:or)?d|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|` +
		`client[_-]?secret|credentials?|-data)[A-Za-z0-9_.-]*)(["']?[ \t]*[:=][ \t]*["']?)([^"'\s,;&]+)`), ""},
	{regexp.MustCompile(`\b(glpat-|gldt-|glrt-|ghp_|gho_|ghs_|github_pat_|xox[abpr]-|sk-)[A-Za-z0-9_-]{8,}`), "${1}[REDACTED]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED-AWS-KEY]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[REDACTED-JWT]"},
}

var (
	secretName   = regexp.MustCompile(`(?i)(pass|secret|token|key|cred|auth|cookie|session|dsn|conn|private)`)
	envNameYAML  = regexp.MustCompile(`^(\s*)-?\s*name:\s*["']?([A-Za-z_][A-Za-z0-9_.-]*)["']?\s*$`)
	envValueYAML = regexp.MustCompile(`^(\s*)value:\s*(.+)$`)
	envNameJSON  = regexp.MustCompile(`^\s*"name":\s*"([A-Za-z_][A-Za-z0-9_.-]*)",?\s*$`)
	envValueJSON = regexp.MustCompile(`^(\s*"value":\s*)"(?:[^"\\]|\\.)*"(,?)\s*$`)
)

// Redact hides secret-looking strings; an env entry whose name looks secret loses its value (yaml/json).
func Redact(text string) string {
	lines := strings.Split(text, "\n")
	pending := 0
	for i, line := range lines {
		if m := envNameYAML.FindStringSubmatch(line); m != nil {
			pending = 0
			if secretName.MatchString(m[2]) {
				pending = 3
			}
			continue
		}
		if m := envNameJSON.FindStringSubmatch(line); m != nil {
			pending = 0
			if secretName.MatchString(m[1]) {
				pending = 3
			}
			continue
		}
		if pending > 0 {
			pending--
			if m := envValueJSON.FindStringSubmatch(line); m != nil {
				lines[i], pending = m[1]+`"[REDACTED]"`+m[2], 0
			} else if m := envValueYAML.FindStringSubmatch(line); m != nil {
				lines[i], pending = m[1]+"value: '[REDACTED]'", 0
			}
		}
	}
	text = strings.Join(lines, "\n")
	for _, r := range redactions {
		if r.rep == "" { // the assignment rule: keep an existing [REDACTED]
			text = r.re.ReplaceAllStringFunc(text, func(s string) string {
				m := r.re.FindStringSubmatch(s)
				if strings.HasPrefix(m[3], "[REDACTED") {
					return s
				}
				return m[1] + m[2] + "[REDACTED]"
			})
			continue
		}
		text = r.re.ReplaceAllString(text, r.rep)
	}
	return text
}

// --- argument checks ------------------------------------------------------------------------------------------------

type check func(v, flag string) (string, error)

func reCheck(re *regexp.Regexp, what string) check {
	return func(v, flag string) (string, error) {
		if !re.MatchString(v) {
			return "", refusal(flag + ": bad " + what)
		}
		return v, nil
	}
}

func choice(vals ...string) check {
	return func(v, flag string) (string, error) {
		for _, x := range vals {
			if v == x {
				return v, nil
			}
		}
		return "", refusal(flag + " takes one of " + strings.Join(vals, ", "))
	}
}

func durCheck(v, flag string) (string, error) {
	m := durRe.FindStringSubmatch(v)
	if m == nil {
		return "", refusal(flag + " takes a duration like 30m or 2h")
	}
	n, _ := strconv.Atoi(m[1])
	d := time.Duration(n) * map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour}[m[2]]
	if d < time.Second || d > maxSince {
		return "", refusal(flag + " is at most 6h")
	}
	return v, nil
}

func tailCheck(v, flag string) (string, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || len(v) > 6 {
		return "", refusal(flag + " takes a number of lines")
	}
	if n > maxTail {
		n = maxTail
	}
	return strconv.Itoa(n), nil
}

func types(spec string) ([]string, error) {
	t, obj, has := strings.Cut(spec, "/")
	if has && !objRe.MatchString(obj) {
		return nil, refusal("bad object name " + clean(spec, 120))
	}
	ts := strings.Split(t, ",")
	for _, x := range ts {
		if !typeRe.MatchString(x) {
			return nil, refusal("bad resource type " + clean(x, 120))
		}
		if secretTy[strings.SplitN(x, ".", 2)[0]] {
			return nil, refusal("Secrets are never read by this tool")
		}
	}
	return ts, nil
}

func merge(ms ...map[string]check) map[string]check {
	out := map[string]check{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

var (
	nsFlags  = map[string]check{"-n": reCheck(nsRe, "namespace"), "--namespace": reCheck(nsRe, "namespace"), "-A": nil, "--all-namespaces": nil}
	selFlags = map[string]check{"-l": reCheck(selRe, "selector"), "--selector": reCheck(selRe, "selector")}
)

// parseFlags walks args: flags in allowed (nil check = a boolean), the rest positional.
func parseFlags(args []string, allowed map[string]check, posMax int) (pos, out []string, ns string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" || len(a) > 260 || strings.ContainsAny(a, "\n\x00") {
			return nil, nil, "", refusal("an argument is empty, too long or has a line break")
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		name, inline, hasEq := strings.Cut(a, "=")
		if neverFl[name] || strings.HasPrefix(name, "--as") {
			return nil, nil, "", refusal(clean(name, 60) + " is never allowed")
		}
		chk, ok := allowed[name]
		if !ok {
			return nil, nil, "", refusal("unknown or not allowed flag " + clean(name, 60))
		}
		if chk == nil {
			if hasEq {
				return nil, nil, "", refusal(clean(name, 60) + " takes no value")
			}
			out = append(out, name)
			continue
		}
		v := inline
		if !hasEq {
			if i+1 >= len(args) {
				return nil, nil, "", refusal(name + " needs a value")
			}
			i++
			v = args[i]
		}
		if v, err = chk(v, name); err != nil {
			return nil, nil, "", err
		}
		if name == "-n" || name == "--namespace" {
			ns = v
		}
		out = append(out, name, v)
	}
	if len(pos) > posMax {
		return nil, nil, "", refusal("too many arguments")
	}
	return pos, out, ns, nil
}

func flagVal(fl []string, names ...string) string {
	for i := 0; i+1 < len(fl); i++ {
		for _, n := range names {
			if fl[i] == n {
				return fl[i+1]
			}
		}
	}
	return ""
}

func has(fl []string, name string) bool {
	for _, x := range fl {
		if x == name {
			return true
		}
	}
	return false
}

// Build is (kubectl argv after the global flags, resource for the audit, namespace, line cap).
func Build(verb string, args []string) ([]string, string, string, int, error) {
	switch verb {
	case "get":
		out := choice("wide", "name", "json", "yaml")
		pos, fl, ns, err := parseFlags(args, merge(nsFlags, selFlags, map[string]check{
			"--field-selector": reCheck(fselRe, "field selector"), "-o": out, "--output": out, "--show-labels": nil,
			"--sort-by": reCheck(sortRe, "sort path"), "--no-headers": nil}), 20)
		if err != nil {
			return nil, "", "", 0, err
		}
		if len(pos) == 0 {
			return nil, "", "", 0, refusal("get needs a resource type")
		}
		ts, err := types(pos[0])
		if err != nil {
			return nil, "", "", 0, err
		}
		for _, p := range pos[1:] {
			if strings.Contains(p, "/") {
				if _, err := types(p); err != nil {
					return nil, "", "", 0, err
				}
			} else if !objRe.MatchString(p) {
				return nil, "", "", 0, refusal("bad object name " + clean(p, 120))
			}
		}
		o := flagVal(fl, "-o", "--output")
		for _, t := range ts {
			if cmTy[strings.SplitN(t, ".", 2)[0]] && (o == "json" || o == "yaml") {
				return nil, "", "", 0, refusal("ConfigMaps: names only (get configmaps, -o name or -o wide); their contents are not read")
			}
		}
		return append(append([]string{"get"}, pos...), fl...), pos[0], ns, maxLines, nil
	case "describe":
		pos, fl, ns, err := parseFlags(args, merge(nsFlags, selFlags), 2)
		if err != nil {
			return nil, "", "", 0, err
		}
		if len(pos) == 0 {
			return nil, "", "", 0, refusal("describe needs a resource type")
		}
		ts, err := types(pos[0])
		if err != nil {
			return nil, "", "", 0, err
		}
		for _, t := range ts {
			if cmTy[strings.SplitN(t, ".", 2)[0]] {
				return nil, "", "", 0, refusal("ConfigMaps: names only (describe shows their contents); use get configmaps")
			}
		}
		if len(pos) == 2 && !objRe.MatchString(pos[1]) {
			return nil, "", "", 0, refusal("bad object name " + clean(pos[1], 120))
		}
		return append(append([]string{"describe"}, pos...), fl...), pos[0], ns, maxLines, nil
	case "logs":
		pos, fl, ns, err := parseFlags(args, merge(nsFlags, selFlags, map[string]check{
			"-c": reCheck(objRe, "container"), "--container": reCheck(objRe, "container"), "--all-containers": nil,
			"--tail": tailCheck, "--since": durCheck, "--previous": nil, "-p": nil, "--timestamps": nil, "--prefix": nil}), 1)
		if err != nil {
			return nil, "", "", 0, err
		}
		if len(pos) == 0 && flagVal(fl, "-l", "--selector") == "" {
			return nil, "", "", 0, refusal("logs needs a pod (or -l selector)")
		}
		res := "-l"
		if len(pos) == 1 {
			res = pos[0]
			if strings.Contains(res, "/") {
				ts, err := types(res)
				if err != nil {
					return nil, "", "", 0, err
				}
				if !logKinds[strings.SplitN(ts[0], ".", 2)[0]] {
					return nil, "", "", 0, refusal("logs reads pods or their controllers only")
				}
			} else if !objRe.MatchString(res) {
				return nil, "", "", 0, refusal("bad pod name " + clean(res, 120))
			}
		}
		if !has(fl, "--tail") {
			fl = append(fl, "--tail", strconv.Itoa(defaultTail))
		}
		argv := append(append([]string{"logs"}, pos...), fl...)
		return append(argv, "--limit-bytes", strconv.Itoa(4*maxBytes)), res, ns, maxLogLines, nil
	case "top":
		pos, fl, ns, err := parseFlags(args, merge(nsFlags, selFlags, map[string]check{"--containers": nil,
			"--sort-by": choice("cpu", "memory"), "--no-headers": nil}), 2)
		if err != nil {
			return nil, "", "", 0, err
		}
		if len(pos) == 0 || !set("pod", "pods", "po", "node", "nodes", "no")[pos[0]] {
			return nil, "", "", 0, refusal("top takes pods or nodes")
		}
		if len(pos) == 2 && !objRe.MatchString(pos[1]) {
			return nil, "", "", 0, refusal("bad object name " + clean(pos[1], 120))
		}
		return append(append([]string{"top"}, pos...), fl...), pos[0], ns, maxLines, nil
	case "events":
		_, fl, ns, err := parseFlags(args, merge(nsFlags, map[string]check{"--for": reCheck(forRe, "kind/name"),
			"--types": reCheck(typesRe, "event types")}), 0)
		if err != nil {
			return nil, "", "", 0, err
		}
		res := "events"
		if f := flagVal(fl, "--for"); f != "" {
			if _, err := types(f); err != nil {
				return nil, "", "", 0, err
			}
			res = f
		}
		return append([]string{"events"}, fl...), res, ns, maxLines, nil
	case "api-resources":
		o := choice("wide", "name")
		_, fl, _, err := parseFlags(args, map[string]check{"--namespaced": choice("true", "false"),
			"--api-group": reCheck(groupRe, "group"), "-o": o, "--output": o}, 0)
		if err != nil {
			return nil, "", "", 0, err
		}
		return append([]string{"api-resources"}, fl...), "api-resources", "", maxLines, nil
	case "version":
		if _, _, _, err := parseFlags(args, map[string]check{}, 0); err != nil {
			return nil, "", "", 0, err
		}
		return []string{"version"}, "version", "", maxLines, nil
	case "can-i", "auth":
		a := args
		if verb == "auth" {
			if len(a) == 0 || a[0] != "can-i" {
				return nil, "", "", 0, refusal("auth: only auth can-i --list")
			}
			a = a[1:]
		}
		var rest []string
		for _, x := range a {
			if x != "--list" {
				rest = append(rest, x)
			}
		}
		_, fl, ns, err := parseFlags(rest, map[string]check{"-n": nsFlags["-n"], "--namespace": nsFlags["--namespace"]}, 0)
		if err != nil {
			return nil, "", "", 0, err
		}
		return append([]string{"auth", "can-i", "--list"}, fl...), "can-i", ns, maxLines, nil
	}
	return nil, "", "", 0, refusal("'" + clean(verb, 40) + "' is not a read-only command here; run 'argus tools kube list'")
}

// --- the cluster ---------------------------------------------------------------------------------------------------

func (t *Tool) clusters() (map[string]*Entry, error) {
	p := filepath.Join(t.KubeDir, "clusters.json")
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Entry{}, nil
	}
	if err != nil {
		return nil, refusal(p + " is not readable")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != t.OwnerUID || fi.Mode().Perm()&0o022 != 0 {
		return nil, refusal(p + " is not owned by root or is writable by others")
	}
	b, err := os.ReadFile(p)
	var doc struct {
		Clusters map[string]*Entry `json:"clusters"`
	}
	if err != nil || json.Unmarshal(b, &doc) != nil {
		return nil, refusal(p + " is not readable")
	}
	if doc.Clusters == nil {
		doc.Clusters = map[string]*Entry{}
	}
	return doc.Clusters, nil
}

func (t *Tool) kubeconfig(name string, cl map[string]*Entry) (*Entry, string, error) {
	e := cl[name]
	if e == nil {
		names := make([]string, 0, len(cl))
		for k := range cl {
			names = append(names, k)
		}
		sort.Strings(names)
		tail := " (none configured yet)"
		if len(names) > 0 {
			tail = " (configured: " + strings.Join(names, ", ") + ")"
		}
		return nil, "", refusal("no kubeconfig for cluster '" + clean(name, 40) + "': an admin adds one in Settings -> Kubernetes" + tail)
	}
	if !e.Enabled {
		why := e.Why
		if why == "" {
			why = e.Verdict
		}
		return nil, "", refusal("cluster '" + name + "' is not enabled for the agent (" + words(why, 60) + "); an admin checks it in Settings -> Kubernetes")
	}
	p := filepath.Join(t.KubeDir, name+".kubeconfig")
	fi, err := os.Stat(p)
	if err != nil {
		return nil, "", refusal("cluster '" + name + "' has no kubeconfig file on " + t.Host + "; an admin saves it again in Settings -> Kubernetes")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != t.OwnerUID || fi.Mode().Perm()&0o007 != 0 {
		return nil, "", refusal("the kubeconfig of '" + name + "' is not root-owned or is readable by others")
	}
	if f, err := os.Open(p); err != nil {
		return nil, "", refusal("the kubeconfig of '" + name + "' is not readable by this user (not enabled)")
	} else {
		f.Close()
	}
	return e, p, nil
}

func (t *Tool) audit(cluster, verb, res, ns, rc string) {
	usr := strconv.Itoa(os.Getuid())
	if u, err := user.Current(); err == nil {
		usr = u.Username
	}
	run := os.Getenv("ARGUS_RUN_ID")
	if run == "" {
		run = filepath.Base(os.Getenv("CLAUDE_CONFIG_DIR"))
	}
	if run == "" || run == "." {
		run = "-"
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	line := fmt.Sprintf("%s user=%s cluster=%s verb=%s resource=%s ns=%s run=%s exit=%s\n", now().UTC().Format("2006-01-02T15:04:05Z"),
		usr, clean(dash(cluster), 40), clean(dash(verb), 20), clean(dash(res), 80), clean(dash(ns), 63), clean(run, 60), rc)
	if f, err := os.OpenFile(t.AuditLog, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o640); err == nil {
		f.WriteString(line)
		f.Close()
	}
}

func (t *Tool) auditWritable() bool {
	f, err := os.OpenFile(t.AuditLog, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func (t *Tool) list(stdout io.Writer) error {
	cl, err := t.clusters()
	if err != nil {
		return err
	}
	if len(cl) == 0 {
		fmt.Fprintln(stdout, "# no Kubernetes clusters configured yet (an admin adds a read-only kubeconfig in Settings -> Kubernetes)")
	} else {
		fmt.Fprintln(stdout, "# cluster  env  verdict  agent  server  context  namespace")
		names := make([]string, 0, len(cl))
		for k := range cl {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, n := range names {
			e := cl[n]
			agent := "enabled"
			if !e.Enabled {
				agent = "refused (" + words(e.Why, 40) + ")"
			}
			ns := e.Namespace
			if ns == "" {
				ns = "-"
			}
			fmt.Fprintf(stdout, "%s  %s  %s  %s  %s  %s  %s\n", clean(n, 31), clean(e.Env, 12), clean(e.Verdict, 12), agent,
				clean(e.ServerHost, 80), clean(e.Context, 60), clean(ns, 63))
		}
	}
	if _, err := os.Stat(t.Kubectl); err != nil {
		fmt.Fprintf(stdout, "# kubectl is not installed on %s yet: every call is refused until root installs it\n", t.Host)
	}
	fmt.Fprint(stdout, Usage)
	return nil
}

// Run is one call: argus tools kube <args>. Exit 2 = refused before kubectl started.
func (t *Tool) Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, Usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "list" {
		if err := t.list(stdout); err != nil {
			fmt.Fprintln(stderr, "kube: REFUSED:", err)
			return 2
		}
		t.audit("-", "list", "-", "-", "0")
		return 0
	}
	cluster, verb, rest := args[0], "", []string{}
	if len(args) > 1 {
		verb, rest = args[1], args[2:]
	}
	var e *Entry
	var path, res, ns string
	var kargs []string
	var lineCap int
	err := func() error {
		if !NameRe.MatchString(cluster) {
			return refusal("bad cluster name; run 'argus tools kube list'")
		}
		if verb == "" {
			return refusal("no command; run 'argus tools kube list'")
		}
		var err error
		if kargs, res, ns, lineCap, err = Build(verb, rest); err != nil {
			return err
		}
		cl, err := t.clusters()
		if err != nil {
			return err
		}
		if e, path, err = t.kubeconfig(cluster, cl); err != nil {
			return err
		}
		if fi, err := os.Stat(t.Kubectl); err != nil || fi.Mode().Perm()&0o111 == 0 {
			return refusal("kubectl is not installed on " + t.Host + " yet (root installs the pinned version)")
		}
		return nil
	}()
	if err != nil {
		fmt.Fprintln(stderr, "kube: REFUSED:", err)
		t.audit(cluster, verb, cut(strings.Join(rest, " "), 80), "-", "refused")
		return 2
	}
	if !t.auditWritable() {
		fmt.Fprintf(stderr, "kube: REFUSED: audit log %s not writable (no audit, no kubectl)\n", t.AuditLog)
		return 2
	}
	os.MkdirAll(t.CacheDir, 0o700)
	to := t.Timeout
	if to == 0 {
		to = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	argv := append([]string{"--kubeconfig", path, "--request-timeout=20s", "--cache-dir", filepath.Join(t.CacheDir, cluster)}, kargs...)
	cmd := exec.CommandContext(ctx, t.Kubectl, argv...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.CacheDir, "LANG=C.UTF-8", "KUBECONFIG=" + path}
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &limited{&so, 4 << 20}, &limited{&se, 64 << 10}
	rc := 0
	if err := cmd.Run(); ctx.Err() != nil {
		rc = 124
		se.Reset()
		se.WriteString(fmt.Sprintf("no answer within %d s", int(to.Seconds())))
	} else if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rc = ee.ExitCode()
		} else {
			rc = 126
			se.WriteString("cannot start kubectl")
		}
	}
	t.audit(cluster, verb, res, ns, strconv.Itoa(rc))
	fmt.Fprintf(stdout, "# %s (%s) %s %s — exit %d\n", cluster, clean(e.Env, 12), verb, clean(res, 80), rc)
	text := Redact(strings.ReplaceAll(so.String(), "\x00", ""))
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	shown := lines
	if len(shown) > lineCap {
		shown = shown[:lineCap]
	}
	body := cut(strings.Join(shown, "\n"), maxBytes)
	if body != "" {
		fmt.Fprintln(stdout, body)
	}
	if len(lines) > lineCap {
		fmt.Fprintf(stdout, "[kube: output capped at %d of %d lines]\n", lineCap, len(lines))
	} else if len(text) > maxBytes {
		fmt.Fprintf(stdout, "[kube: output capped at %d bytes]\n", maxBytes)
	}
	if s := strings.TrimSpace(se.String()); s != "" {
		el := strings.Split(s, "\n")
		if len(el) > 20 {
			el = el[:20]
		}
		fmt.Fprintln(stderr, cut(Redact(strings.Join(el, "\n")), 4000))
	}
	return rc
}
