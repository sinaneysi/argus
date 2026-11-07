package app

// views_agent_secret.go: temporary task secrets (ADR-0013 P3) on a task page Argus works. An admin gives Argus a
// credential for one task through a dedicated form (never a chat message). The app writes the value ONCE into
// /run/argus/secret-in/<user>-<rand>.json (tmpfs, dir root:nogroup 0730, file 0600: the app cannot read it back);
// root encrypts it on tmpfs with a per-task key and wipes it when the task ends or the TTL runs out. The value is never
// logged, echoed, stored in a database or put in a redirect; the page only ever shows metadata.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const (
	secretMaxBytes = 8192
	secretMinBytes = 8
	secretMaxTTL   = 1440 // D4: 24 h at most
	secretTTL      = 240  // D4: 4 h by default
	secretMaxHosts = 5
)

var (
	secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)
	secretHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]{0,252})$`)
	secretUserRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)
)

// AgentSecretsOn is TASK_SECRETS (Admin -> Task agent) as root publishes it in board-status.json; off by default.
func AgentSecretsOn() bool {
	st := py.AsMap(py.Or(BoardStatus().Get("agent"), py.NewOMap()))
	return py.AsMap(py.Or(st.Get("settings"), py.NewOMap())).Get("secrets") == true
}

// agentReleaseTask: D3, release tasks never get secrets (the plan's environment or kind, or the task's env).
func agentReleaseTask(a Row, taskEnv string) bool {
	if taskEnv == "release" {
		return true
	}
	plan := py.AsMap(py.Or(agentJSON(a.Get("plan"), "null"), py.NewOMap()))
	cl := py.AsMap(py.Or(plan.Get("classification"), py.NewOMap()))
	return cl.S("environment") == "release" || cl.S("kind") == "production"
}

func agentTaskEnv(key string) string {
	var r []Row
	if strings.HasPrefix(key, "gl-") {
		r = BoardBQ("SELECT env FROM tasks WHERE id=?", key[3:])
	} else {
		r = BoardBQ("SELECT env FROM tasks WHERE jira_key=?", key)
	}
	if len(r) == 0 {
		return ""
	}
	return py.OrS(r[0].Get("env"), "")
}

func secretHosts(r Row) string {
	var hs []string
	_ = json.Unmarshal([]byte(py.OrS(r.Get("hosts"), "[]")), &hs)
	return strings.Join(hs, ", ")
}

// AgentPostSecret is POST /board/task/secret (class sensitive, admins only). The value goes into one write-once file on
// tmpfs and nowhere else: not the log, not the redirect, not an error page.
func AgentPostSecret(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess == nil || sess.Role != "admin" {
		return 403, "Only admins provide credentials"
	}
	key := form.Get("key")
	if !agentKeyRe.MatchString(key) {
		return 400, "Bad request"
	}
	a := agentRow(key)
	if a == nil {
		return 404, "Argus is not working this task"
	}
	if !AgentSecretsOn() {
		return 409, "Task secrets are off"
	}
	if agentReleaseTask(a, agentTaskEnv(key)) {
		return 409, "Release tasks never get secrets"
	}
	bad := func(msg string) (int, string) {
		return 400, msg
	}
	name := strings.TrimSpace(form.Get("name"))
	if !secretNameRe.MatchString(name) {
		return bad("The name is like API_KEY: A-Z, 0-9 and _, 2 to 32 characters")
	}
	ttl, err := strconv.Atoi(strings.TrimSpace(form.Get("ttl_min")))
	if err != nil || ttl < 1 || ttl > secretMaxTTL {
		return bad("The expiry is 1 to 1440 minutes (24 h at most)")
	}
	var hosts []string
	for _, h := range strings.Split(form.Get("hosts"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			if !secretHostRe.MatchString(h) {
				return bad("A host is a DNS name or an IP address")
			}
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 || len(hosts) > secretMaxHosts {
		return bad("Name 1 to 5 hosts the credential may be used against")
	}
	value := strings.TrimRight(form.Get("value"), "\r\n")
	if len(value) < secretMinBytes || len(value) > secretMaxBytes || strings.ContainsAny(value, "\r\n\x00") {
		return bad("The value is one line of 8 to 8192 bytes")
	}
	purpose := py.Head(strings.TrimSpace(form.Get("purpose")), 300)
	if !secretUserRe.MatchString(sess.Username) {
		return bad("This user name cannot provide secrets")
	}
	payload := map[string]any{"kind": "secret", "key": key, "name": name, "value": value, "ttl_min": ttl, "hosts": hosts,
		"purpose": purpose, "by": sess.Username, "at": NowInt()}
	data, err := json.Marshal(payload)
	value = ""
	if err != nil {
		return bad("Could not encode the request")
	}
	// The directory must exist (tmpfs, created by root at boot): never created here, never a disk fallback.
	if st, err := os.Stat(SECRET_IN); err != nil || !st.IsDir() {
		return 503, "The secret store is not available"
	}
	fn := sess.Username + "-" + auth.TokenHex(8) + ".json"
	err = boardDropExcl(SECRET_IN, fn, data)
	for i := range data {
		data[i] = 0
	}
	if err != nil {
		os.Remove(filepath.Join(SECRET_IN, "."+fn+".tmp"))
		return 500, "Could not store the secret"
	}
	logf("task secret %s for %s provided by %s (value not logged)", name, key, sess.Username)
	return 303, agentTaskURL(key) + "&sec=" + strconv.FormatInt(time.Now().Unix(), 10) + "#secrets"
}
