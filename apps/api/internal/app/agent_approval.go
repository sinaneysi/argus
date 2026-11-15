package app

// agent_approval.go: the api signs approvals of the capability executor (ADR-0022 amendment 1, item A). The key is
// in ARGUS_APPROVAL_KEY_DIR, a folder only the api mounts; actd holds the public half and refuses a step whose
// approval the api did not sign. The worker passes the signature on but cannot make one.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

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

// errNoApprovalKey: the api holds no approval key (ARGUS_APPROVAL_KEY_DIR), so it cannot sign.
var errNoApprovalKey = errors.New("the api holds no approval key (ARGUS_APPROVAL_KEY_DIR): see CONFIG.md, signed approvals")

// agentSign signs a claim with the api's key; errNoApprovalKey without one.
func agentSign(c capability.ApprovalClaim) (string, error) {
	k := approvalSigner()
	if k == nil {
		return "", errNoApprovalKey
	}
	return capability.SignApproval(k, c)
}

// agentPlanSHA is the plan hash an approval binds to, from the stored plan (the same as the worker's and actd's).
func agentPlanSHA(a Row) string { return capability.StoredPlanSHA256(a.S("plan")) }

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

// agentSignedPayload adds the api's signature to a request payload: kind start, continue, rollback or stop. With
// optional, a missing key leaves the request unsigned (a plan that does not execute, a stop); otherwise it is an
// error and nothing is sent.
func agentSignedPayload(p *py.OMap, kind, key string, version int64, sha, by string, step int64, optional bool) error {
	at := NowInt()
	sig, err := agentSign(capability.ApprovalClaim{Kind: kind, TaskKey: key, PlanVersion: int(version), PlanSHA256: sha, Approver: by,
		At: at, SealedSHA256: capability.SealedSHA256(nil), Step: int(step)})
	if errors.Is(err, errNoApprovalKey) && optional {
		p.Set("plan_sha256", sha)
		return nil
	}
	if err != nil {
		return fmt.Errorf("the approval cannot be signed: %v", err)
	}
	p.Set("plan_sha256", sha).Set("signed_at", at).Set("signature", sig)
	return nil
}

// ACTIONS_DIR is the shared actions folder (actd writes actions/out there). Tests may change it.
var ACTIONS_DIR = filepath.Join(env("ARGUS_STATE_DIR", "/var/lib/argus"), "actions")

// agentWaitingStep is the operator step the action waits for, from actd's result file (actions/out), not from
// board.db; 0 when actd's last result for the action is not "needs the operator".
func agentWaitingStep(id string) int64 {
	if !capability.ValidActionID(id) {
		return 0
	}
	f, err := os.OpenFile(filepath.Join(ACTIONS_DIR, "out", id+".json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0
	}
	defer f.Close()
	var r struct {
		ActionID string `json:"action_id"`
		State    string `json:"state"`
		Steps    []struct {
			N      int64  `json:"n"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if json.NewDecoder(io.LimitReader(f, 256<<10)).Decode(&r) != nil || r.ActionID != id || r.State != "operator" {
		return 0
	}
	for _, s := range r.Steps {
		if s.Status == "needs the operator" {
			return s.N
		}
	}
	return 0
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
	if why := BoardWriteBlock(sess); why != "" && !strings.HasPrefix(key, "gl-") {
		return 403, "Not continued: " + why
	}
	n, _ := py.ParseInt(v)
	step, _ := py.ParseInt(st)
	if a.Str("state") != "operator" {
		return 409, "Not continued: the agent is not waiting for the operator"
	}
	if n != a.I("plan_version") || sha != agentPlanSHA(a) || sha != a.Str("approved_sha256") {
		return 409, "The plan changed: review v" + I2S(a.I("plan_version"))
	}
	id := capability.ActionID(key, int(n), sha)
	if a.Str("action_id") != id || step <= 0 || agentWaitingStep(id) != step {
		return 409, "Not continued: the executor does not wait for step " + I2S(step)
	}
	p := py.NewOMap("kind", "agent-continue", "key", key, "plan_version", n, "step", step)
	if err := agentSignedPayload(p, capability.SignContinue, key, n, sha, sess.Username, step, false); err != nil {
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
	if why := BoardWriteBlock(sess); why != "" && !strings.HasPrefix(key, "gl-") {
		return 403, "Not rolled back: " + why
	}
	step, _ := py.ParseInt(st)
	// signed against the approved hash, which must still be the stored plan's
	sha := a.Str("approved_sha256")
	if a.Str("action_id") != id || sha == "" || sha != agentPlanSHA(a) || a.I("approved_version") != a.I("plan_version") ||
		capability.ActionID(key, int(a.I("plan_version")), sha) != id || step <= 0 {
		return 409, "Not rolled back: that is not the task's last action, or the plan changed since"
	}
	p := py.NewOMap("kind", "agent-rollback", "key", key, "action_id", id, "plan_version", a.I("plan_version"), "step", step)
	if err := agentSignedPayload(p, capability.SignRollback, key, a.I("plan_version"), sha, sess.Username, step, false); err != nil {
		return 500, "Not rolled back: " + err.Error()
	}
	rid, err := BoardWriteRequest(p, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "agent", agentTaskURL(key)+"&req="+rid+"#agent")
}
