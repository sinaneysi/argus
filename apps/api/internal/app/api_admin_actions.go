package app

// api_admin_actions.go: Settings → Jira sync → Task agent, the capability executor block (ADR-0022 D8, PR 5). The
// mode and the allow-lists come from the configuration and are read-only here; the one switch is "Stop all actions"
// (auth_settings actions.stopped, read by actd before every step). The heartbeat is actd's status file in the
// shared volume, which holds no secret; only its listed fields reach the page.

import (
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/actd"
	"github.com/sinaneysi/argus/internal/approvalkey"
	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/py"
)

// actdHeartbeatOK / actdHeartbeatStale: the heartbeat ages (seconds) of the self-check (selfcheck.go, actd).
const (
	actdHeartbeatOK    = 120
	actdHeartbeatStale = 600
)

// actionsAllowKeys are the allow-lists shown, in order, with what each one limits.
var actionsAllowKeys = [][2]string{
	{"ARGUS_ACT_HTTP_HOSTS", "http.check hosts"},
	{"ARGUS_ACT_GITLAB_PROJECTS", "GitLab projects (CI/CD variables, scope develop)"},
	{"ARGUS_ACT_KEYCLOAK_REALMS", "Identity provider realms (read only)"},
}

// actionsAddressKeys are the targets' addresses (no credential: the tokens live only in actd's own volume).
var actionsAddressKeys = [][2]string{
	{"ARGUS_ACT_GITLAB_URL", "GitLab address"},
	{"ARGUS_ACT_KEYCLOAK_URL", "Identity provider address"},
}

// actdStatusPub is actd's status public key as the operator pinned it (tests replace it); an error when none is.
var actdStatusPub = func() (ed25519.PublicKey, error) {
	pub, err := approvalkey.LoadActdStatus(APPROVAL_KEY_DIR)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		return nil, errors.New("no status key of actd is pinned (argus approval-key trust-actd-status)")
	}
	return pub, nil
}

// actionsSetting reads an executor key of the configuration (tests replace it).
var actionsSetting = func(k string) string { return env(k, "") }

// ActionsStopped reports the admin switch "Stop all actions".
// It reads the switch as actd does: any value other than empty or "0" is stopped.
func ActionsStopped() bool {
	v := strings.TrimSpace(auth.Setting(actd.StoppedKey))
	return v != "" && v != "0"
}

func splitCSV(s string) []string {
	out := []string{}
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// apiAdminActions is the executor block of the admin page.
func apiAdminActions() map[string]any {
	mode := "off"
	if m, err := capability.ParseMode(ACTIONS_MODE); err == nil {
		mode = string(m)
	} else {
		mode = "invalid"
	}
	lists := []map[string]any{}
	for _, k := range actionsAllowKeys {
		lists = append(lists, map[string]any{"key": k[0], "label": k[1], "values": splitCSV(actionsSetting(k[0]))})
	}
	addrs := []map[string]any{}
	for _, k := range actionsAddressKeys {
		addrs = append(addrs, map[string]any{"key": k[0], "label": k[1], "value": strings.TrimSpace(actionsSetting(k[0]))})
	}
	out := map[string]any{"mode": mode, "stopped": ActionsStopped(), "lists": lists, "addresses": addrs, "heartbeat": apiActdHeartbeat(mode)}
	return out
}

// apiActdHeartbeat is actd's last status: its age and verdict, the audit chain, the capabilities it offers.
func apiActdHeartbeat(mode string) map[string]any {
	st, ok := actd.ReadStatus(filepath.Join(ACTIONS_DIR, "actd-status.json"))
	if !ok || st.TS == 0 {
		kind, detail := "neutral", "actd has not written its status: the actions profile is not running."
		if mode != "off" {
			kind, detail = "fail", "Mode "+mode+", but actd never wrote its status. Start the actions profile."
		}
		return map[string]any{"seen": false, "kind": kind, "text": "Not running", "detail": detail, "capabilities": []string{}}
	}
	age := NowInt() - st.TS
	// the worker can write the shared volume: a status counts only when actd's signature checks with the key the
	// operator pinned in the api's own folder, and its time is fresh (ADR-0022 PR 5 review)
	pub, perr := actdStatusPub()
	verr := perr
	if verr == nil {
		verr = actd.VerifyStatus(st, pub)
	}
	if verr == nil && (age > actdHeartbeatStale || age < -300) {
		verr = errors.New("the signed status is " + If(age < 0, "dated in the future", "older than "+I2S(actdHeartbeatStale)+" seconds"))
	}
	if verr != nil {
		kind := "warn"
		if mode != "off" {
			kind = "fail"
		}
		return map[string]any{"seen": false, "verified": false, "kind": kind, "text": "Not verified", "capabilities": []string{},
			"detail": "actd's status cannot be trusted: " + py.Head(verr.Error(), 200) + ". Nothing it claims is shown."}
	}
	if age < 0 {
		age = 0
	}
	kind, text := "ok", "Verified"
	switch {
	case age > actdHeartbeatOK:
		kind, text = "warn", "Verified, late"
	}
	if !st.Chain.OK {
		kind, text = "fail", "Chain broken"
	}
	caps := append([]string{}, st.Capabilities...)
	sort.Strings(caps)
	chain := "OK, " + I2S(int64(st.Chain.Records)) + " records"
	if !st.Chain.OK {
		chain = py.Head(st.Chain.Error, 200)
	}
	return map[string]any{"seen": true, "verified": true, "kind": kind, "text": text, "at": st.TS, "mode": st.Mode, "version": st.Version, "chain": chain,
		"chainOk": st.Chain.OK, "capabilities": caps, "backupsKept": st.BackupsKept, "approvalKey": st.ApprovalKey != "", "sealKey": st.SealKey != ""}
}

// ActionsStopSave is POST /api/v1/admin/actions {stopped: "1"|"0"} (sensitive: a recent 2FA code). It sets the
// switch actd reads before every step; the change is in the audit log.
func ActionsStopSave(v string, sess *Session, ip string) (string, *apiErr) {
	if v != "1" && v != "0" {
		return "", &apiErr{400, "Choose stop or resume."}
	}
	if err := auth.SetSetting(actd.StoppedKey, v); err != nil {
		panic(err)
	}
	what := If(v == "1", "stopped", "resumed")
	auth.Audit(sess.Username, "actions."+what, "actd", "Stop all actions: "+If(v == "1", "on", "off"), ip)
	logf("capability actions %s by %s", what, sess.Username)
	if v == "1" {
		return "All capability actions are stopped. actd refuses every step from the next one on.", nil
	}
	return "Capability actions are allowed again. The mode and the approvals still apply.", nil
}
