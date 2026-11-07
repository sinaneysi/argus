package app

// kube.go: Settings -> Kubernetes (GET /admin/kube, POST /admin/kube). An admin adds a kubeconfig per cluster; the
// agent then reads that cluster through `argus tools kube` (get, describe, logs, top, events; never Secrets, never a
// write), but only when root's check says the login is read-only.
//
// The app never stores or shows a kubeconfig. "Save and check" writes it once into settings-in/kube/<user>-<16 hex>.json
// (0600, O_EXCL) and forgets it; root's `argus secrets apply-kube` (argus-kube.path, internal/kube) validates it again,
// keeps only the chosen context, checks it with kubectl (auth can-i) as nobody, backs up the previous file and stores
// it under ARGUS_KUBE_DIR. This module reads only settings-out/kube-status.json (per cluster: environment, server host,
// context, user, verdict, enabled, who and when, the last check; kubectl's state; per-request results).

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const (
	kubeMaxBytes = 64 * 1024 // same as internal/kube MaxBytes
	kubeFID      = "kube-add"
)

var (
	kubeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)             // kube.NameRe
	kubeCtxRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,120}$`) // kube.ContextRe
	kubeKeyRe  = regexp.MustCompile(`(?m)^[ \t]*(?:-[ \t]+)?["']?([A-Za-z-]+)["']?[ \t]*:`)
	kubeJSONRe = regexp.MustCompile(`"([A-Za-z-]+)"[ \t\r\n]*:`)
	kubeEnvs   = []string{"develop", "pre-release", "release"}
	// The keys root refuses (internal/kube Why): a plugin runs a program, a path reads a file, "as" impersonates.
	kubeForbidden = map[string]string{
		"exec":                  "it uses an exec plugin, which would run a program on " + ARGUS_HOSTNAME,
		"auth-provider":         "it uses an auth-provider plugin",
		"client-certificate":    "it points to a client certificate file; embed it (client-certificate-data)",
		"client-key":            "it points to a client key file; embed it (client-key-data)",
		"certificate-authority": "it points to a CA file; embed it (certificate-authority-data)",
		"tokenFile":             "it points to a token file; put the token inline",
		"token-file":            "it points to a token file; put the token inline",
		"as":                    "it impersonates another user (as)",
		"as-groups":             "it impersonates groups (as-groups)",
		"as-uid":                "it impersonates a user ID (as-uid)",
		"as-user-extra":         "it impersonates (as-user-extra)",
	}
)

// KubeStatus is settings-out/kube-status.json ({} when root has not published it yet).
func KubeStatus() *py.OMap { return ReadJSONMap(filepath.Join(SET_OUT, "kube-status.json")) }

// kubeCheckText mirrors root's first checks so an obvious mistake is refused before anything is written. Root checks
// again and is the authority (YAML is parsed there). "" = looks fine.
func kubeCheckText(text string) string {
	if strings.TrimSpace(text) == "" {
		return "paste the kubeconfig or choose its file."
	}
	if len(text) > kubeMaxBytes {
		return "the kubeconfig is larger than 64 KB."
	}
	if strings.ContainsRune(text, 0) {
		return "the kubeconfig contains a NUL byte; paste it as text."
	}
	keys := map[string]bool{}
	for _, m := range kubeKeyRe.FindAllStringSubmatch(text, -1) {
		keys[m[1]] = true
	}
	for _, m := range kubeJSONRe.FindAllStringSubmatch(text, -1) {
		keys[m[1]] = true
	}
	names := make([]string, 0, len(kubeForbidden))
	for k := range kubeForbidden {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if keys[k] {
			return "not accepted: " + kubeForbidden[k] + ". Only inline credentials are stored, so nothing runs here."
		}
	}
	for _, k := range []string{"clusters", "contexts", "users"} {
		if !keys[k] {
			return "that does not look like a kubeconfig: it has no " + k + "."
		}
	}
	return ""
}

func kubeDrop(sess *Session, payload *py.OMap) (string, error) {
	rid := auth.TokenHex(8)
	p := payload.Copy()
	p.Set("user", sess.Username)
	p.Set("at", NowInt())
	if err := boardDropExcl(filepath.Join(SET_IN, "kube"), sess.Username+"-"+rid+".json", []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("kube %s %s requested by %s: %s", py.Str(payload.Get("kind")), py.Str(payload.Get("name")), sess.Username, rid) // never the kubeconfig
	return rid, nil
}

// KubePost is POST /admin/kube: save (add or replace a cluster's kubeconfig), test (check it again), remove.
func KubePost(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	name := py.Strip(form.Get("name"))
	action := form.Get("action")
	if action != "save" && action != "test" && action != "remove" {
		return 400, "Unknown action"
	}
	if !kubeNameRe.MatchString(name) {
		if action != "save" {
			return 400, "Unknown cluster"
		}
		return 400, kubeRefused(c, "name", "Use lowercase letters, digits and -, up to 31 characters, for example dev.")
	}
	var rid string
	var err error
	switch action {
	case "save":
		env := form.Get("env")
		if !In(env, kubeEnvs...) {
			return 400, kubeRefused(c, "env", "Choose the environment this cluster belongs to.")
		}
		ctx := py.Strip(form.Get("context"))
		if ctx != "" && !kubeCtxRe.MatchString(ctx) {
			return 400, kubeRefused(c, "context", "A context name uses letters, digits and . _ : @ / - only.")
		}
		override := form.Get("override") == "1"
		if override && env == "release" {
			return 400, kubeRefused(c, "override", "Release never takes the override: production stays read-only.")
		}
		text := form.Get("kubeconfig")
		if strings.TrimSpace(text) == "" {
			text = form.Get("kubeconfig_file")
		}
		if msg := kubeCheckText(text); msg != "" {
			return 400, kubeRefused(c, "kubeconfig", strings.ToUpper(msg[:1])+msg[1:])
		}
		rid, err = kubeDrop(sess, py.NewOMap("kind", "save", "name", name, "env", env, "context", ctx,
			"kubeconfig", text, "override", override))
	default:
		rid, err = kubeDrop(sess, py.NewOMap("kind", action, "name", name))
	}
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/kube?kr=" + rid
}
