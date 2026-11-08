package app

// Two-factor authentication: the policy, the cookies and what the /api/v1 writes share (the JSON answers are in
// api.go and api_account.go; the pages are in apps/web).
//
// The policy "actions require 2FA" is one middleware (apiMFA, called by apiPOST after the session and CSRF) driven
// by POST_ROUTES: every write with its class. Default deny: a path not listed is "sensitive".
// MFA_STEP_UP=off (default): the 2FA of the sign-in satisfies "2FA" and "2FA (recent)" for the whole session;
// only the inline routes (turn off, new recovery codes) ask again. MFA_STEP_UP=on: the time windows apply.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

// Policy is how a POST route is protected.
type Policy int

const (
	PolicyExempt    Policy = iota // no second factor: sign-in, sign-out, theme, enrolment itself, own sessions
	PolicyInline                  // verifies a current code inside the request itself (turn off, new recovery codes)
	PolicyNormal                  // 2FA on and verified on this session (MFA_STEP_UP=on: within MFA_STEPUP_MINUTES, 720)
	PolicySensitive               // ... (MFA_STEP_UP=on: within MFA_SENSITIVE_MINUTES, 10)
)

func (p Policy) String() string {
	return [...]string{"exempt", "inline code", "2FA", "2FA (recent)"}[p]
}

// POST_ROUTES is every write the server handles and its policy (the coverage test checks it against apiIsPost and the
// sign-in writes). Paths under /api/v1/board/ are PolicyNormal; anything else unlisted is
// PolicySensitive.
var POST_ROUTES = map[string]Policy{
	// exempt: sign-in, sign-out, the theme, starting 2FA, and signing yourself out (ending other sessions checks 2FA inline)
	"/api/v1/account/2fa/begin":   PolicyExempt,
	"/api/v1/account/2fa/confirm": PolicyExempt,
	"/api/v1/account/sessions":    PolicyExempt,
	"/api/v1/account/stepup":      PolicyExempt,
	"/api/v1/auth/login":          PolicyExempt,
	"/api/v1/auth/logout":         PolicyExempt,
	"/api/v1/auth/mfa":            PolicyExempt,
	"/api/v1/theme":               PolicyExempt,
	// verify a code themselves
	"/api/v1/account/2fa/disable":  PolicyInline,
	"/api/v1/account/2fa/recovery": PolicyInline,
	// actions; review/act also needs the policy of its decision kind (apiReviewPolicyRoute)
	"/api/v1/account":           PolicyNormal,
	"/api/v1/account/jira":      PolicyNormal,
	"/api/v1/account/matrix":    PolicyNormal,
	"/api/v1/backups/install":   PolicyNormal,
	"/api/v1/backups/new":       PolicyNormal,
	"/api/v1/backups/restore":   PolicyNormal,
	"/api/v1/board/agent":       PolicyNormal,
	"/api/v1/board/agent/chat":  PolicyNormal,
	"/api/v1/board/assign":      PolicyNormal,
	"/api/v1/board/comment":     PolicyNormal,
	"/api/v1/board/create":      PolicyNormal,
	"/api/v1/board/fixer":       PolicyNormal,
	"/api/v1/board/jmove":       PolicyNormal,
	"/api/v1/board/move":        PolicyNormal,
	"/api/v1/board/mxpreview":   PolicyNormal,
	"/api/v1/board/refresh":     PolicyNormal,
	"/api/v1/capacity/analyse":  PolicyNormal,
	"/api/v1/cicd/repo/check":   PolicyNormal,
	"/api/v1/ideas/act":         PolicyNormal,
	"/api/v1/ideas/generate":    PolicyNormal,
	"/api/v1/incidents/act":     PolicyNormal,
	"/api/v1/incidents/deep":    PolicyNormal,
	"/api/v1/inventory/analyse": PolicyNormal,
	"/api/v1/inventory/refresh": PolicyNormal,
	"/api/v1/problems/act":      PolicyNormal,
	"/api/v1/proposals/act":     PolicyNormal,
	"/api/v1/seo/send":          PolicyNormal,
	"/api/v1/review/act":        PolicyNormal,
	"/api/v1/scan":              PolicyNormal,
	"/api/v1/today/act":         PolicyNormal,
	// sensitive: settings, users, integrations, suppressions, accepted failures, the fixer, credentials
	"/api/v1/admin/agent":                      PolicySensitive,
	"/api/v1/admin/agent/fixer-move":           PolicySensitive,
	"/api/v1/admin/backup-targets":             PolicySensitive,
	"/api/v1/admin/board":                      PolicySensitive,
	"/api/v1/admin/claude":                     PolicySensitive,
	"/api/v1/admin/modules":                    PolicySensitive,
	"/api/v1/admin/claude-egress":              PolicySensitive,
	"/api/v1/admin/claude-token":               PolicySensitive,
	"/api/v1/admin/gitlab-issues":              PolicySensitive,
	"/api/v1/admin/jira-hosts":                 PolicySensitive,
	"/api/v1/admin/jira-shared":                PolicySensitive,
	"/api/v1/admin/kube":                       PolicySensitive,
	"/api/v1/admin/outline":                    PolicySensitive,
	"/api/v1/admin/save":                       PolicySensitive,
	"/api/v1/admin/sources":                    PolicySensitive,
	"/api/v1/admin/users/act":                  PolicySensitive,
	"/api/v1/admin/users/matrix":               PolicySensitive,
	"/api/v1/admin/writing-style":              PolicySensitive,
	"/api/v1/backups/override":                 PolicySensitive,
	"/api/v1/board/agent/spinup":               PolicySensitive,
	"/api/v1/board/task/secret":                PolicySensitive,
	"/api/v1/fix/review":                       PolicySensitive,
	"/api/v1/lessons/act":                      PolicySensitive,
	"/api/v1/lessons/settings":                 PolicySensitive,
	"/api/v1/proposals/settings":               PolicySensitive,
	"/api/v1/sources/catalogue/discover":       PolicySensitive,
	"/api/v1/sources/catalogue/routes/confirm": PolicySensitive,
	"/api/v1/sources/catalogue/routes/edit":    PolicySensitive,
	"/api/v1/sources/catalogue/routes/reject":  PolicySensitive,
	"/api/v1/sources/review/act":               PolicySensitive,
	"/api/v1/vulns/suppress":                   PolicySensitive,
}

// PolicyOf is the policy of a POST path for this session (a forced password change is exempt: it comes first).
func PolicyOf(path string, sess *Session) Policy {
	if path == "/api/v1/account" && sess != nil && sess.MustChange != 0 {
		return PolicyExempt
	}
	if p, ok := POST_ROUTES[path]; ok {
		return p
	}
	if strings.HasPrefix(path, "/api/v1/board/") {
		return PolicyNormal
	}
	return PolicySensitive
}

// MFAState says whether the session may do an action of policy p: "" (yes), "enrol" or "stepup".
func MFAState(sess *Session, p Policy) string {
	if !auth.MFAActions || p == PolicyExempt || p == PolicyInline {
		return ""
	}
	if sess.MFAVia != "idp" && !auth.Enabled(sess.Username) {
		return "enrol"
	}
	if !auth.MFAStepUpOn { // MFA_STEP_UP=off: 2FA once per session (sign-in, enrolment, trusted IdP) covers every change
		if sess.MFAAt <= 0 {
			return "stepup"
		}
		return ""
	}
	window := auth.MFAStepup
	if p == PolicySensitive {
		window = auth.MFASensitive
	}
	if sess.MFAAt <= 0 || sess.MFAAt < NowInt()-window {
		return "stepup"
	}
	return ""
}

func (h *reqCtx) ua() string { return h.r.Header.Get("User-Agent") }

func maxAgeFor(remember bool) int64 { m, _ := auth.Limits(remember); return m }

// MFA_COOKIE carries a pending login between the password (or Keycloak) and the code.
const MFA_COOKIE = "__Host-opsr_mfa"

func mfaCookie(token string) string {
	return MFA_COOKIE + "=" + token + "; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=" + Itoa(auth.PENDING_TTL)
}

const mfaCookieClear = MFA_COOKIE + "=; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=0"

// --- security events to root (audit.log + a notice for resets and recovery use) ----------------------------

// SecurityEventsHook mirrors the auth audit log to settings-in/security-events/ (bin/security_events.py, root:
// settings-out/audit.log, and a notification for mfa.reset and mfa.recovery.used). Never a secret or a code.
func SecurityEventsHook(e auth.Event) {
	d := filepath.Join(SET_IN, "security-events")
	if os.MkdirAll(d, 0o755) != nil {
		return
	}
	doc := py.NewOMap("at", NowInt(), "actor", e.Actor, "action", e.Action, "target", e.Target, "detail", e.Detail,
		"ip", e.IP, "sid", e.SID, "method", e.Method, "outcome", e.Outcome, "reason", e.Reason)
	name := time.Unix(NowInt(), 0).UTC().Format("20060102150405") + "-" + auth.TokenHex(4) + ".json"
	WriteFileAtomic(filepath.Join(d, name), filepath.Join(d, "."+name+".tmp"), []byte(py.Dumps(doc, -1)))
}

func secEvent(sess *Session, action, target, detail, ip, reason string) {
	auth.Log(auth.Event{Actor: sess.Username, Action: action, Target: target, Detail: detail, IP: ip, SID: sess.ID(),
		Method: methodOf(sess), Reason: reason})
}

func methodOf(sess *Session) string {
	if sess.MFAVia != "" && sess.MFAAt > NowInt()-auth.MFAStepup {
		return sess.MFAVia
	}
	if sess.Source == "keycloak" {
		return "keycloak"
	}
	return "password"
}

// --- QR code ---------------------------------------------------------------------------------------------

// qrPath is the code's size in modules and one SVG path of its dark modules (0 when the text does not fit).
func qrPath(text string) (int, string) {
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return 0, ""
	}
	bm := q.Bitmap()
	n := len(bm)
	var b strings.Builder
	for y, row := range bm {
		for x, on := range row {
			if on {
				b.WriteString("M" + Itoa(x) + " " + Itoa(y) + "h1v1h-1z")
			}
		}
	}
	return n, b.String()
}

func groupKey(s string) string {
	var parts []string
	for i := 0; i < len(s); i += 4 {
		j := i + 4
		if j > len(s) {
			j = len(s)
		}
		parts = append(parts, s[i:j])
	}
	return strings.Join(parts, " ")
}

// --- pages -------------------------------------------------------------------------------------------------

func humanMinutes(m int64) string {
	if m%60 == 0 && m >= 60 {
		h := m / 60
		return I2S(h) + " hour" + py.Plural(int(h))
	}
	return I2S(m) + " minute" + py.Plural(int(m))
}

// deviceOf summarises a User-Agent: "Firefox on Linux".
func deviceOf(ua string) string {
	if ua == "" {
		return "Unknown device"
	}
	b := "Browser"
	for _, x := range [][2]string{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}, {"curl/", "curl"}} {
		if strings.Contains(ua, x[0]) {
			b = x[1]
			break
		}
	}
	o := ""
	for _, x := range [][2]string{{"iPhone", "iOS"}, {"iPad", "iPadOS"}, {"Android", "Android"}, {"Windows", "Windows"}, {"Mac OS X", "macOS"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, x[0]) {
			o = x[1]
			break
		}
	}
	if o == "" {
		return b
	}
	return b + " on " + o
}

// --- POST handlers -------------------------------------------------------------------------------------------

func mfaMsg(err error) string {
	switch err {
	case auth.ErrBadCode:
		return "That code is wrong or was already used. Wait for the next code and try again."
	case auth.ErrLocked:
		return "Too many wrong codes. Wait 15 minutes and try again."
	case auth.ErrNoEnrol:
		return "This setup expired or was started in another browser. Start again."
	case auth.ErrNoKey:
		return "Two-factor authentication is not available: the key file is missing on the server."
	case auth.ErrStale:
		return "Confirm it's you first: type your password (or sign in with Keycloak again)."
	case auth.ErrHasFactor:
		return "Two-factor authentication is already on: type a current code to move to a new app."
	case auth.ErrNotActive:
		return "Two-factor authentication is not on for this account."
	}
	return "That did not work: " + err.Error()
}

var _ = json.Valid
