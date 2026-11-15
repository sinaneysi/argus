package app

// agent_approval.go: the api signs approvals of the capability executor (ADR-0022 amendment 1, item A). The key is
// in ARGUS_APPROVAL_KEY_DIR, a folder only the api mounts; actd holds the public half and refuses a step whose
// approval the api did not sign. The worker passes the signature on but cannot make one.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sinaneysi/argus/internal/approvalkey"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/py"
)

// The capability executor's settings the api reads.
var (
	APPROVAL_KEY_DIR = env("ARGUS_APPROVAL_KEY_DIR", "/var/lib/argus-approve")
	ACTIONS_MODE     = env("ARGUS_ACTIONS_MODE", "off")
)

var approvalKey struct {
	sync.Mutex
	k    ed25519.PrivateKey
	err  error
	done bool
}

// InitApprovalKey loads (or at the first start creates) the approval key; the api calls it once at start. A missing
// folder is not an error: approvals are then not signed, and actd refuses every step.
func InitApprovalKey() (string, error) {
	approvalKey.Lock()
	defer approvalKey.Unlock()
	approvalKey.done = true
	approvalKey.k, approvalKey.err = approvalkey.Load(APPROVAL_KEY_DIR, !DEMO)
	if errors.Is(approvalKey.err, approvalkey.ErrNoDir) {
		return "", nil
	}
	if approvalKey.err != nil {
		return "", approvalKey.err
	}
	return capability.KeyFingerprint(approvalKey.k.Public().(ed25519.PublicKey)), nil
}

// SetApprovalKey sets the signing key (tests); nil: no key.
func SetApprovalKey(k ed25519.PrivateKey) {
	approvalKey.Lock()
	defer approvalKey.Unlock()
	approvalKey.k, approvalKey.err, approvalKey.done = k, nil, true
}

func approvalSigner() ed25519.PrivateKey {
	approvalKey.Lock()
	done := approvalKey.done
	approvalKey.Unlock()
	if !done {
		InitApprovalKey()
	}
	approvalKey.Lock()
	defer approvalKey.Unlock()
	return approvalKey.k
}

// agentSign signs a claim with the api's key; "" when the api holds no key (actd then refuses the step).
func agentSign(c capability.ApprovalClaim) (string, error) {
	k := approvalSigner()
	if k == nil {
		return "", nil
	}
	return capability.SignApproval(k, c)
}

// agentPlanSHA is the plan hash an approval binds to, from the stored plan (the same as the worker's and actd's).
func agentPlanSHA(a Row) string {
	var raw struct {
		Classification any `json:"classification"`
		Plan           any `json:"plan"`
	}
	if a.S("plan") == "" || json.Unmarshal([]byte(a.S("plan")), &raw) != nil {
		return ""
	}
	s, err := capability.PlanSHA256(raw.Classification, raw.Plan)
	if err != nil {
		return ""
	}
	return s
}

// agentExecutes reports whether Spin up runs the plan's capability steps (the worker's spinUp path execute):
// classification develop, ARGUS_ACTIONS_MODE not off, at least one capability step and every WRITE step with one.
func agentExecutes(plan *py.OMap) bool {
	if strings.TrimSpace(ACTIONS_MODE) == "" || ACTIONS_MODE == "off" {
		return false
	}
	cl := py.AsMap(py.Or(plan.Get("classification"), py.NewOMap()))
	if py.OrS(cl.Get("environment"), "") != "develop" || cl.Get("kind") == "production" {
		return false
	}
	caps := 0
	for _, sv := range py.AsList(py.Or(plan.Get("plan"), []any{})) {
		s := py.AsMap(sv)
		c := py.OrS(s.Get("capability"), "")
		if c != "" {
			caps++
		}
		if s.Get("mode") == "WRITE" && c == "" {
			return false
		}
	}
	return caps > 0
}

// agentSignedPayload adds the api's signature to a request payload: kind start, continue or rollback.
func agentSignedPayload(p *py.OMap, kind, key string, version int64, sha, by string, step int64) error {
	at := NowInt()
	sig, err := agentSign(capability.ApprovalClaim{Kind: kind, TaskKey: key, PlanVersion: int(version), PlanSHA256: sha, Approver: by,
		At: at, SealedSHA256: capability.SealedSHA256(nil), Step: int(step)})
	if err != nil {
		return fmt.Errorf("the approval cannot be signed: %v", err)
	}
	p.Set("plan_sha256", sha).Set("signed_at", at)
	if sig != "" {
		p.Set("signature", sig)
	}
	return nil
}

var hex64 = func(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// AgentPostContinue is POST /board/agent/continue (sensitive): the operator did the operator step; an admin signs
// "continue after step N" for exactly the plan version and hash shown, and the remaining steps run.
func AgentPostContinue(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Only admins continue the agent"
	}
	key, v, sha, st := form.Get("key"), form.Get("plan_version"), form.Get("plan_sha256"), form.Get("step")
	if !agentKeyRe.MatchString(key) || !py.IsDigit(v) || len(v) > 9 || !hex64(sha) || !py.IsDigit(st) || len(st) > 4 {
		return 400, "Bad request"
	}
	a := agentRow(key)
	if a == nil {
		return 404, "Argus is not working this task"
	}
	n, _ := py.ParseInt(v)
	step, _ := py.ParseInt(st)
	if a.Str("state") != "operator" {
		return 409, "Not continued: the agent is not waiting for the operator"
	}
	if n != a.I("plan_version") || sha != agentPlanSHA(a) {
		return 409, "The plan changed: review v" + I2S(a.I("plan_version"))
	}
	p := py.NewOMap("kind", "agent-continue", "key", key, "plan_version", n, "step", step)
	if err := agentSignedPayload(p, capability.SignContinue, key, n, sha, sess.Username, step); err != nil {
		return 500, "Not continued: " + err.Error()
	}
	rid, err := BoardWriteRequest(p, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "agent", agentTaskURL(key)+"&req="+rid+"#agent")
}

// AgentPostRollback is POST /board/agent/rollback (sensitive): an admin signs the manual rollback of one write step
// of the task's last action (ADR-0022 D7); actd runs the capability's Rollback under the refusal rules.
func AgentPostRollback(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Only admins roll back"
	}
	key, id, st := form.Get("key"), form.Get("action_id"), form.Get("step")
	if !agentKeyRe.MatchString(key) || !capability.ValidActionID(id) || !py.IsDigit(st) || len(st) > 4 {
		return 400, "Bad request"
	}
	a := agentRow(key)
	if a == nil {
		return 404, "Argus is not working this task"
	}
	step, _ := py.ParseInt(st)
	sha := agentPlanSHA(a)
	if a.Str("action_id") != id || sha == "" || capability.ActionID(key, int(a.I("plan_version")), sha) != id {
		return 409, "Not rolled back: that is not the task's last action, or the plan changed since"
	}
	p := py.NewOMap("kind", "agent-rollback", "key", key, "action_id", id, "plan_version", a.I("plan_version"), "step", step)
	if err := agentSignedPayload(p, capability.SignRollback, key, a.I("plan_version"), sha, sess.Username, step); err != nil {
		return 500, "Not rolled back: " + err.Error()
	}
	rid, err := BoardWriteRequest(p, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "agent", agentTaskURL(key)+"&req="+rid+"#agent")
}
