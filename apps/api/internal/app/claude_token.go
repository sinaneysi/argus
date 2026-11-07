package app

// claude_token.go: Settings -> Claude. The Claude usage switch, the Claude token panel and the egress-country guard
// on one page.
//
// Token: the app never holds or shows the token. "Replace and test" writes it once into
// settings-in/claude-token/<user>-<16 hex>.json (0600, O_EXCL) and forgets it; root's `argus secrets apply-claude`
// (argus-claude-token.path, examples/systemd) backs up ARGUS_CLAUDE_ENV_FILE, replaces CLAUDE_CODE_OAUTH_TOKEN, runs
// one test call as ARGUS_CLAUDE_RUN_AS and restores the backup when the test fails (unless the admin chose "save
// anyway"). This module reads only settings-out/claude-token-status.json (set or not, last 4 characters, who replaced
// it and when, the last test).
//
// Egress guard (internal/claudegate, `argus claude check` before every Claude run): no run while the public egress
// country is on the blocked list (default IR) or, fail-closed, unknown. The settings go to root as an
// "egress-settings" request (root writes ARGUS_CLAUDE_EGRESS_FILE and the live copy settings-out/claude-egress.json);
// the last check is ARGUS_CLAUDE_EGRESS_CHECK.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var (
	// Same shape as internal/claudetoken TokenRe (`claude setup-token` prints sk-ant-oat01-…).
	claudeOAuthRe   = regexp.MustCompile(`^sk-ant-oat01-[A-Za-z0-9_-]{20,300}$`)
	claudeCountryRe = regexp.MustCompile(`^[A-Z]{2}$`)

	CLAUDE_EGRESS_CHECK = env("ARGUS_CLAUDE_EGRESS_CHECK", "/var/lib/argus/claude-egress-check.json")
	// Same defaults as internal/claudegate Defaults.
	claudeEgressDefaultBlocked = []string{"IR"}
)

const claudeTokenVar = "CLAUDE_CODE_OAUTH_TOKEN"

// ClaudeTokenStatus is settings-out/claude-token-status.json ({} when missing).
func ClaudeTokenStatus() *py.OMap {
	return ReadJSONMap(filepath.Join(SET_OUT, "claude-token-status.json"))
}

// ClaudeEgressCheck is the gate's last egress check ({} when none yet).
func ClaudeEgressCheck() *py.OMap { return ReadJSONMap(CLAUDE_EGRESS_CHECK) }

// ClaudeEgressSettings is the guard as Argus sees it: the live copy root wrote, else the defaults (guard on, IR,
// fail closed).
func ClaudeEgressSettings() (guard bool, blocked []string, fail string) {
	guard, blocked, fail = true, append([]string(nil), claudeEgressDefaultBlocked...), "closed"
	src := ReadJSONMap(filepath.Join(SET_OUT, "claude-egress.json"))
	if src == nil || src.Len() == 0 {
		return
	}
	if b, ok := src.Get("guard").(bool); ok {
		guard = b
	}
	if l := src.List("blocked"); l != nil {
		blocked = nil
		for _, x := range l {
			if s, ok := x.(string); ok && claudeCountryRe.MatchString(s) {
				blocked = append(blocked, s)
			}
		}
	}
	if f := src.S("fail"); f == "open" || f == "closed" {
		fail = f
	}
	return
}

// claudeEgressStopped: the last check stopped AI (shown on Overview with the "Claude paused" notice).
func claudeEgressStopped() (bool, string) {
	g, _, _ := ClaudeEgressSettings()
	ch := ClaudeEgressCheck()
	if !g || ch.Len() == 0 {
		return false, ""
	}
	if a, ok := ch.Get("allowed").(bool); ok && !a {
		return true, ch.S("reason")
	}
	return false, ""
}

// --- the page ---------------------------------------------------------------------------------------------------

func claudeDrop(sess *Session, payload *py.OMap) (string, error) {
	rid := auth.TokenHex(8)
	p := payload.Copy()
	p.Set("user", sess.Username)
	p.Set("at", NowInt())
	if err := boardDropExcl(filepath.Join(SET_IN, "claude-token"), sess.Username+"-"+rid+".json", []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("claude-token %s requested by %s: %s", py.Str(payload.Get("kind")), sess.Username, rid) // never the token
	return rid, nil
}

// ClaudeTokenPost is POST /admin/claude-token: replace (a new token), test (the token in place), egress (check now).
func ClaudeTokenPost(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	var rid string
	var err error
	switch form.Get("action") {
	case "replace":
		token := py.Strip(form.Get("token"))
		if form.GetD("kind", "oauth") != "oauth" { // only the OAuth token is offered (an API key bills per use)
			return 400, "Only the OAuth token can be replaced here"
		}
		if !claudeOAuthRe.MatchString(token) {
			msg := "It starts with sk-ant-oat01- (from claude setup-token): letters, digits, - and _ only."
			c.Rej = Reject("claude-token", "token", msg, py.QS{})
			return 400, msg
		}
		rid, err = claudeDrop(sess, py.NewOMap("kind", "replace", "var", claudeTokenVar, "token", token,
			"save_anyway", form.Get("save_anyway") == "1"))
	case "test", "egress":
		rid, err = claudeDrop(sess, py.NewOMap("kind", form.Get("action")))
	default:
		return 400, "Unknown action"
	}
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/claude?ct=" + rid
}

func claudeEgressRefused(c *Ctx, field, msg string) string {
	c.Rej = Reject("claude-egress", field, msg, py.QS{})
	return msg
}

// ClaudeEgressPost is POST /admin/claude-egress: send the guard settings to root (argus secrets apply-claude).
func ClaudeEgressPost(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	fail := form.GetD("fail", "closed")
	if fail != "closed" && fail != "open" {
		return 400, claudeEgressRefused(c, "fail", "choose stop or let the run start for a failed lookup.")
	}
	seen := map[string]bool{}
	var blocked []string
	for _, p := range strings.FieldsFunc(strings.ToUpper(form.Get("blocked")), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';' }) {
		if !claudeCountryRe.MatchString(p) {
			return 400, claudeEgressRefused(c, "blocked", "blocked countries are two-letter codes such as IR, comma separated.")
		}
		if !seen[p] {
			seen[p] = true
			blocked = append(blocked, p)
		}
	}
	if len(blocked) > 50 {
		return 400, claudeEgressRefused(c, "blocked", "at most 50 blocked countries.")
	}
	sort.Strings(blocked)
	bl := make([]any, len(blocked))
	for i, x := range blocked {
		bl[i] = x
	}
	guard := form.Get("guard") == "1"
	rid, err := claudeDrop(sess, py.NewOMap("kind", "egress-settings", "guard", guard, "blocked", bl, "fail", fail))
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/claude?ct=" + rid
}
