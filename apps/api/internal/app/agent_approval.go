package app

// agent_approval.go: the api signs approvals of the capability executor (ADR-0022 amendment 1, item A). The key is
// in ARGUS_APPROVAL_KEY_DIR, a folder only the api mounts; actd holds the public half and refuses a step whose
// approval the api did not sign. The worker passes the signature on but cannot make one.

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
// sealedSHA is the hash of the sealed inputs the request carries to actd (capability.SealedSHA256): the start's own,
// for a continue or rollback the one the api signed at the start (agentStartSealed), {} for a stop.
func agentSignedPayload(p *py.OMap, kind, key string, version int64, sha, by string, step int64, optional bool, sealedSHA string) error {
	at := NowInt()
	sig, err := agentSign(capability.ApprovalClaim{Kind: kind, TaskKey: key, PlanVersion: int(version), PlanSHA256: sha, Approver: by,
		At: at, SealedSHA256: sealedSHA, Step: int(step)})
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
	if err := agentSignedPayload(p, capability.SignContinue, key, n, sha, sess.Username, step, false, agentStartSealed(key, n, sha)); err != nil {
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
	if err := agentSignedPayload(p, capability.SignRollback, key, a.I("plan_version"), sha, sess.Username, step, false, agentStartSealed(key, a.I("plan_version"), sha)); err != nil {
		return 500, "Not rolled back: " + err.Error()
	}
	rid, err := BoardWriteRequest(p, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "agent", agentTaskURL(key)+"&req="+rid+"#agent")
}

// agentStartSealed is the sealed inputs' hash of the start the api signed for this plan (kept in the api's own key
// folder, not in the worker-writable board.db): a continue or a rollback is signed over it, so a ciphertext the
// worker swapped in board.db is refused by actd. No record: the hash of no inputs.
func agentStartSealed(key string, version int64, sha string) string {
	if h, ok := approvalkey.StartSealed(APPROVAL_KEY_DIR, key, int(version), sha); ok {
		return h
	}
	return capability.SealedSHA256(nil)
}

// errNoSealKey: no sealing key of actd is pinned in the api's key folder.
var errNoSealKey = errors.New("no sealing key of the executor is pinned: run `actd seal-key public | api approval-key trust-actd` (CONFIG.md, sealed inputs)")

// agentSealKey is actd's sealing public key as the operator pinned it (ARGUS_APPROVAL_KEY_DIR/actd-seal.pub). The
// copy actd publishes in the shared volume (actions/actd.pub) must be the same key: a different one means actd
// has a new key (pin it again) or someone replaced the shared copy; either way nothing is sealed.
func agentSealKey() (*ecdh.PublicKey, error) {
	pin, err := approvalkey.LoadActd(APPROVAL_KEY_DIR)
	if err != nil && !errors.Is(err, approvalkey.ErrNoDir) {
		return nil, err
	}
	if pin == nil {
		return nil, errNoSealKey
	}
	if b, err := os.ReadFile(filepath.Join(ACTIONS_DIR, "actd.pub")); err == nil {
		pub, perr := capability.ParseSealPublic(b)
		if perr != nil || !pub.Equal(pin) {
			return nil, errors.New("actd's published sealing key is not the pinned one: compare `actd seal-key show` with the pin, then pin again")
		}
	}
	return pin, nil
}

// agentPlanInputs are the sealed inputs the stored plan's capability steps refer to (step -> names).
func agentPlanInputs(a Row) map[int][]string {
	_, p, err := capability.DecodePlan([]byte(a.S("plan")))
	if err != nil {
		return nil
	}
	return capability.PlanInputs(p)
}

// agentInputNames are the distinct names of agentPlanInputs, sorted, for the approval form.
func agentInputNames(in map[int][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, names := range in {
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// agentSealInputs seals each value the admin typed (form field input_<NAME>) once per step that refers to it, for
// this action. The values are never stored, logged or echoed; an error names the input, never its value.
func agentSealInputs(form py.QS, in map[int][]string, actionID, planSHA string) (map[string]string, error) {
	out := map[string]string{}
	if len(in) == 0 {
		return out, nil
	}
	pub, err := agentSealKey()
	if err != nil {
		return nil, err
	}
	for step, names := range in {
		for _, n := range names {
			v := form.Get("input_" + n)
			if v == "" {
				return nil, fmt.Errorf("type the value of %s", n)
			}
			if len(v) > capability.MaxInputBytes {
				return nil, fmt.Errorf("the value of %s is longer than %d bytes", n, capability.MaxInputBytes)
			}
			sv, err := capability.SealInput(pub, actionID, planSHA, step, n, v)
			if err != nil {
				return nil, fmt.Errorf("the value of %s cannot be sealed", n)
			}
			out[capability.InputKey(step, n)] = sv
		}
	}
	if len(out) > capability.MaxSealedInputs {
		return nil, fmt.Errorf("more than %d sealed inputs", capability.MaxSealedInputs)
	}
	return out, nil
}
