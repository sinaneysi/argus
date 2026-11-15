package board

// agent_act.go: the task agent's execute path (ADR-0022 D5, PR 2). An approved develop plan whose WRITE steps all
// name a valid capability goes to the capability executor (argus actd) instead of the model: the worker writes
// actions/in/<action-id>.json, actd checks the refusal rules and runs the steps, its redacted results come back in
// actions/out/<action-id>.json. An operator step stops the action (state operator) until an admin's signed
// agent-continue; then a read-only model run reports the results, and the comment's capability table is built here
// from actions/out, never from the model's text. The api signs every approval; the worker only passes the
// signature on, and actd refuses a step without a valid one.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/actd"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/notify"
)

// storedPlanSHA is the plan hash of a stored plan (capability.PlanSHA256 of its classification and steps); "" when
// the plan is not readable. The api and actd compute it the same way from the same text.
func storedPlanSHA(plan string) string { return capability.StoredPlanSHA256(plan) }

// executable: at least one capability step, and every WRITE step has a valid capability (validatePlanWith kept it).
func executable(plan map[string]any) bool {
	caps := 0
	for _, x := range asList(plan["plan"]) {
		s := asMap(x)
		c := orStr(s["capability"])
		if c != "" {
			caps++
		}
		if s["mode"] == "WRITE" && c == "" {
			return false
		}
	}
	return caps > 0
}

// checkCapability is the worker's validation of one step's capability (actd validates again before it runs): known,
// the arguments a flat object of the schema, on the allow-lists, the tier fits the step's mode, no secret-looking
// value. It returns the cleaned arguments, or the reason the step keeps no capability.
func checkCapability(reg *capability.Registry, name string, rawArgs any, mode string, env capability.Env) (capability.Args, string) {
	switch {
	case mode == "operator":
		return nil, "an operator step names no capability"
	case name == "":
		return nil, "no capability: a person does this step"
	}
	c, ok := reg.Get(name)
	if !ok {
		return nil, fmt.Sprintf("unknown capability %q, or its allow-list is empty", head(name, 64))
	}
	if c.Tier() == capability.Write && mode != "WRITE" {
		return nil, "a write-tier capability must be in a WRITE step"
	}
	b, err := json.Marshal(rawArgs)
	if err != nil || rawArgs == nil {
		return nil, "the step has no arguments object"
	}
	args, err := capability.ParseArgs(b)
	if err != nil {
		return nil, capability.AsRefusal(err).Reason
	}
	if _, hits := notify.Redact(string(b), nil); len(hits) > 0 {
		return nil, "an argument looks like a secret; secrets enter only as sealed inputs"
	}
	if err := c.Validate(args, env); err != nil {
		return nil, capability.AsRefusal(err).Reason
	}
	return args, ""
}

// signedAt is the api's signing time of a request (the approval time actd checks the signature against); a time
// more than 5 minutes ahead of the worker's clock, or older than 30 minutes, is refused. A request without one (the
// api holds no key) gets now.
func (r *run) signedAt(req map[string]any) int64 {
	at, ok := asInt(req["signed_at"])
	if !ok {
		return r.now()
	}
	if d := at - r.now(); d > 300 || d < -1800 {
		panic(refused("the approval is older than 30 minutes or its time is ahead of this clock: approve again (check the clocks of the api and the worker)"))
	}
	return at
}

// signature is the request's approval signature ("" when the api holds no key: actd then refuses every step).
func signature(req map[string]any) string {
	s, _ := req["signature"].(string)
	if len(s) > 200 {
		panic(refused("bad approval signature"))
	}
	if s != "" {
		if _, err := base64.StdEncoding.DecodeString(s); err != nil {
			panic(refused("bad approval signature"))
		}
	}
	return s
}

func (r *run) actPath(dir, id string) string {
	return filepath.Join(r.o.Agent.ActionsDir, dir, id+".json")
}

// sealedInputs are the approval's sealed inputs ("<step>/<NAME>" -> ciphertext, sealed by the api to actd's key)
// as JSON for agent_tasks.sealed_inputs; nil (NULL) when there are none. The worker checks only their form: it
// cannot open them, and the api's signature covers their hash.
func sealedInputs(req map[string]any) any {
	raw, ok := req["sealed_inputs"]
	if !ok || raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok || len(m) > capability.MaxSealedInputs {
		panic(refused("bad sealed inputs"))
	}
	out := map[string]string{}
	for k, v := range m {
		s, ok := v.(string)
		if _, _, kok := capability.ParseInputKey(k); !ok || !kok || !capability.ValidSealed(s) {
			panic(refused("bad sealed inputs"))
		}
		out[k] = s
	}
	if len(out) == 0 {
		return nil
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// storedSealed reads agent_tasks.sealed_inputs; an empty map when there are none.
func storedSealed(row row) map[string]string {
	out := map[string]string{}
	if s := row.s("sealed_inputs"); s != "" {
		json.Unmarshal([]byte(s), &out)
	}
	return out
}

// actRequest is the request file of the task's action: the stored plan's capability and operator steps, in order.
func actRequest(row row, plan map[string]any, id, sha string) actd.Request {
	req := actd.Request{ActionID: id, TaskKey: row.s("key"), PlanVersion: int(row.i("plan_version")), PlanSHA256: sha,
		Approver: row.s("approved_by"), Steps: []actd.ReqStep{}, SealedInputs: storedSealed(row)}
	for _, x := range asList(plan["plan"]) {
		s := asMap(x)
		n, _ := capability.AsInt(s["n"])
		mode, name := orStr(s["mode"]), orStr(s["capability"])
		if name == "" && mode != "operator" {
			continue
		}
		st := actd.ReqStep{N: n, Mode: mode, Capability: name}
		if name != "" {
			st.Args, _ = json.Marshal(s["args"])
		}
		req.Steps = append(req.Steps, st)
	}
	return req
}

// dispatchAct writes the action request for actd (the approved plan, or its continue after an operator step).
func (r *run) dispatchAct(row row, plan map[string]any, resume bool) string {
	key := row.s("key")
	sha := storedPlanSHA(row.s("plan"))
	if sha == "" || sha != row.s("approved_sha256") {
		r.must(`UPDATE agent_tasks SET state='ready', error=? WHERE key=?`, fmt.Sprintf("the plan changed: review v%d and spin up again", row.i("plan_version")), key)
		r.commit()
		return "plan changed"
	}
	id := capability.ActionID(key, int(row.i("plan_version")), sha)
	if id == "" {
		r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, error='the task key cannot name an action' WHERE key=?`, key)
		r.commit()
		return "act not started"
	}
	req := actRequest(row, plan, id, sha)
	if resume {
		req.Continue = &actd.Signed{Step: int(row.i("continue_step")), Approver: row.s("continue_by"), At: row.i("continue_at"), Signature: row.s("continue_sig")}
	}
	b, _ := json.MarshalIndent(req, "", " ")
	os.Remove(r.actPath("out", id)) // the result of an earlier round of the same action
	err := os.MkdirAll(filepath.Dir(r.actPath("in", id)), 0o700)
	if err == nil {
		err = writeAtomic(r.actPath("in", id), b, 0o600)
	}
	if err != nil {
		r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, error='could not start the action', error_detail=? WHERE key=?`,
			r.errorDetail("run", "could not start the action", nil, "the request file cannot be written: "+err.Error()), key)
		r.commit()
		return "act not started"
	}
	if resume {
		r.must(`UPDATE agent_tasks SET state='running', phase='act', action_id=?, action_state=NULL, job_started=?, error=NULL WHERE key=?`, id, r.now(), key)
		r.alog(key, "act", fmt.Sprintf("Continue after step %d (the operator's step, confirmed by %s): the remaining capability steps run", row.i("continue_step"), row.s("continue_by")))
	} else {
		r.must(`UPDATE agent_tasks SET state='running', phase='act', action_id=?, action_state=NULL, run_started=?, job_started=?, error=NULL WHERE key=?`,
			id, r.now(), r.now(), key)
		r.bump("run")
		r.alog(key, "act", fmt.Sprintf("Capability steps of plan v%d sent to the executor (approved by %s, action %s)", row.i("plan_version"), row.s("approved_by"), id))
	}
	r.commit()
	return "act " + key
}

// readAct is actd's result for the action, nil while there is none.
func (r *run) readAct(id string) (*actd.Response, string) {
	d := readOut(r.actPath("out", id))
	if d == nil {
		return nil, ""
	}
	if e := orStr(d["_error"]); e != "" {
		return nil, e
	}
	b, _ := json.Marshal(d)
	var resp actd.Response
	if json.Unmarshal(b, &resp) != nil || resp.ActionID != id {
		return nil, "the executor's result is not for this action"
	}
	return &resp, ""
}

// storeActions keeps the step results of the action (agent_actions) for the task page and the comment.
func (r *run) storeActions(key string, resp *actd.Response) {
	for _, s := range resp.Steps {
		detail := s.Detail
		if s.Refusal != nil && detail == "" {
			detail = s.Refusal.Reason
		}
		detail, _ = notify.Redact(head(detail, 300), r.o.Allow)
		r.must(`INSERT OR REPLACE INTO agent_actions (action_id, step, key, capability, phase, status, summary, detail, backup_ref, audit_sha256, at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, resp.ActionID, s.N, key, head(s.Capability, 64), head(s.Phase, 20), head(s.Status, 40),
			head(s.Summary, 300), detail, head(s.BackupRef, 200), head(s.Audit, 64), s.At)
	}
	if resp.Refusal != nil && len(resp.Steps) == 0 { // refused before any step: one row for the action
		detail, _ := notify.Redact(head(resp.Refusal.Reason, 300), r.o.Allow)
		r.must(`INSERT OR REPLACE INTO agent_actions (action_id, step, key, capability, phase, status, summary, detail, at) VALUES (?,0,?,'','refused','refused','',?,?)`,
			resp.ActionID, key, detail, resp.Finished)
	}
}

// actIngest reads actd's result: operator -> state operator (the comment names the step); anything else -> the
// read-only run reports the results (capability_results in its job file).
func (r *run) actIngest(row row) {
	key, id := row.s("key"), row.s("action_id")
	resp, bad := r.readAct(id)
	if resp == nil || resp.Finished < row.i("job_started") {
		if bad == "" && r.now()-row.i("job_started") <= int64(r.o.Agent.ActTO/time.Second) {
			return
		}
		why := "no result from the executor (timeout): is actd running (compose profile actions)?"
		if bad != "" {
			why = "the executor's result is not usable: " + bad
		}
		os.Remove(r.actPath("in", id))
		r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, error=?, error_detail=?, updated=? WHERE key=?`,
			head(why, 300), r.errorDetail("run", why, nil, ""), r.now(), key)
		r.alog(key, "failed", why)
		r.notice("agent-failed", r.taskIDOf(key), r.actor(), key)
		return
	}
	r.storeActions(key, resp)
	var plan map[string]any
	json.Unmarshal([]byte(row.s("plan")), &plan)
	if resp.State == actd.StateOperator {
		step, text := 0, ""
		for _, s := range resp.Steps {
			if s.Status == "needs the operator" {
				step = s.N
				break
			}
		}
		for _, x := range asList(plan["plan"]) {
			if n, _ := capability.AsInt(asMap(x)["n"]); n == step {
				text = orStr(asMap(x)["text"])
			}
		}
		r.must(`UPDATE agent_tasks SET state='operator', action_state=?, job_started=NULL, continue_step=NULL, continue_by=NULL, continue_at=NULL,
			continue_sig=NULL, updated=? WHERE key=?`, resp.State, r.now(), key)
		r.alog(key, "operator", fmt.Sprintf("Needs the operator: step %d. Then an admin presses Continue", step))
		lines := []string{fmt.Sprintf("[%s] Task needs the operator: step %d: %s", r.o.Name, step, head(text, 600)),
			"When it is done, an admin presses Continue on the task page; the steps after it run then.", r.capabilityTable(id),
			"Full report: " + r.agentTaskURL(key)}
		body, _ := notify.Redact(strings.Join(lines, "\n\n"), r.o.Allow)
		r.agentComment(key, head(body, maxComment), row.s("approved_by"))
		r.notice("agent-blocked", r.taskIDOf(key), row.s("approved_by"), key)
		return
	}
	sc, inScope := r.agentScope(r.agentSettings())[key]
	task := sc.task
	if !inScope {
		task = r.one(`SELECT * FROM tasks WHERE id=?`, r.taskIDOf(key))
	}
	results, _ := json.Marshal(resp)
	var capRes any
	json.Unmarshal(results, &capRes)
	ctxd := r.agentContext(task, key, plan)
	ctxd["phase"], ctxd["approved_plan"], ctxd["plan_version"], ctxd["approved_by"] = "run", plan, row.i("plan_version"), row.s("approved_by")
	ctxd["production"], ctxd["board_columns"], ctxd["write_steps_for_a_person"] = false, r.boardColumns(), false
	ctxd["capability_results"] = capRes
	if err := r.writeJob(key+".run.json", ctxd); err != nil {
		r.must(`UPDATE agent_tasks SET state='failed', action_state=?, job_started=NULL, error='could not start the run', error_detail=? WHERE key=?`,
			resp.State, r.errorDetail("run", "could not start the run", nil, "the job file cannot be written: "+err.Error()), key)
		return
	}
	r.must(`UPDATE agent_tasks SET phase='run', action_state=?, job_started=?, updated=? WHERE key=?`, resp.State, r.now(), r.now(), key)
	if resp.Refusal != nil { // the executor's reason, on the task page
		reason, _ := notify.Redact(head("The executor refused: "+resp.Refusal.Reason, 300), r.o.Allow)
		r.must(`UPDATE agent_tasks SET error=? WHERE key=?`, reason, key)
		r.alog(key, "refused", reason)
	}
	r.alog(key, "act", fmt.Sprintf("Capability steps: %s (%d steps); a read-only run writes the report", resp.State, len(resp.Steps)))
}

// capabilityTable is the comment's table of the action's steps, built from actions/out (agent_actions): never from
// the model's report.
func (r *run) capabilityTable(id string) string {
	if id == "" {
		return ""
	}
	rows := r.rows(`SELECT * FROM agent_actions WHERE action_id=? ORDER BY step`, id)
	if len(rows) == 0 {
		return ""
	}
	lines := []string{"Capability steps (run by " + r.o.Name + ", not by the model; action " + id + "):", "| step | capability | result | audit |", "|---|---|---|---|"}
	cell := func(s string) string { return strings.NewReplacer("|", "/", "\n", " ", "\r", " ").Replace(s) }
	for _, x := range rows {
		res := x.s("status")
		if d := x.s("detail"); d != "" {
			res += ": " + d
		} else if sm := x.s("summary"); sm != "" {
			res += ": " + sm
		}
		audit := x.s("audit_sha256")
		if len(audit) > 12 {
			audit = audit[:12]
		}
		lines = append(lines, fmt.Sprintf("| %d | %s | %s | %s |", x.i("step"), orDash(cell(x.s("capability"))), cell(head(res, 200)), orDash(audit)))
	}
	return strings.Join(lines, "\n")
}

// agentContinue is agent-continue {key, plan_version, plan_sha256, step, signed_at, signature}: the operator did the
// operator step; the api signed "continue after step N" for exactly this plan version and hash.
func (r *run) agentContinue(row row, req map[string]any, by string) string {
	key := row.s("key")
	if row.s("state") != "operator" {
		panic(refused("the agent is not waiting for the operator"))
	}
	ver, ok := asInt(req["plan_version"])
	sha := storedPlanSHA(row.s("plan"))
	if !ok || ver != row.i("plan_version") || sha == "" || orStr(req["plan_sha256"]) != sha || sha != row.s("approved_sha256") {
		panic(refused(fmt.Sprintf("the plan changed: review v%d", row.i("plan_version"))))
	}
	step, ok := asInt(req["step"])
	if !ok || r.one(`SELECT step FROM agent_actions WHERE action_id=? AND step=? AND status='needs the operator'`, row.s("action_id"), step) == nil {
		panic(refused("that is not the operator step the action waits for"))
	}
	at, sig := r.signedAt(req), signature(req)
	r.must(`UPDATE agent_tasks SET state='approved', continue_step=?, continue_by=?, continue_at=?, continue_sig=?, error=NULL, updated=? WHERE key=?`,
		step, by, at, sig, r.now(), key)
	r.alog(key, "continue", fmt.Sprintf("Step %d done by the operator, confirmed by %s: continuing", step, by))
	return fmt.Sprintf("continuing after step %d", step)
}

// agentRollback is agent-rollback {key, action_id, step, signed_at, signature} (ADR-0022 D7): the request goes to
// actd, which runs the capability's Rollback of that step under the refusal rules.
func (r *run) agentRollback(row row, req map[string]any, by string) string {
	key, id := row.s("key"), orStr(req["action_id"])
	a := r.o.Agent
	if a.ActionsDir == "" {
		panic(refused("no capability executor in this installation"))
	}
	if id == "" || id != row.s("action_id") {
		panic(refused("that is not the task's last action"))
	}
	if contains([]string{"planning", "running", "approved"}, row.s("state")) || !row.null("rollback_started") {
		panic(refused("a job is running for this task; wait for it to finish"))
	}
	step, ok := asInt(req["step"])
	if !ok || r.one(`SELECT step FROM agent_actions WHERE action_id=? AND step=? AND COALESCE(backup_ref,'') != ''`, id, step) == nil {
		panic(refused("that step has no backup to roll back"))
	}
	var plan map[string]any
	json.Unmarshal([]byte(row.s("plan")), &plan)
	sha := storedPlanSHA(row.s("plan"))
	if sha == "" || capability.ActionID(key, int(row.i("plan_version")), sha) != id {
		panic(refused("the plan changed since the action"))
	}
	at, sig := r.signedAt(req), signature(req)
	areq := actRequest(row, plan, id, sha)
	areq.Rollback = &actd.Signed{Step: int(step), Approver: by, At: at, Signature: sig}
	b, _ := json.MarshalIndent(areq, "", " ")
	os.Remove(r.actPath("out", id))
	err := os.MkdirAll(filepath.Dir(r.actPath("in", id)), 0o700)
	if err == nil {
		err = writeAtomic(r.actPath("in", id), b, 0o600)
	}
	if err != nil {
		panic(refused("the rollback request cannot be written"))
	}
	r.must(`UPDATE agent_tasks SET rollback_started=?, updated=? WHERE key=?`, r.now(), r.now(), key)
	r.alog(key, "rollback", fmt.Sprintf("Rollback of step %d requested by %s", step, by))
	return fmt.Sprintf("rollback of step %d requested", step)
}

// rollbackIngest reads the result of a requested rollback.
func (r *run) rollbackIngest() {
	if r.o.Agent.ActionsDir == "" {
		return
	}
	for _, row := range r.rows(`SELECT * FROM agent_tasks WHERE rollback_started IS NOT NULL`) {
		key, id := row.s("key"), row.s("action_id")
		resp, bad := r.readAct(id)
		if resp == nil || resp.Finished < row.i("rollback_started") {
			if bad == "" && r.now()-row.i("rollback_started") <= int64(r.o.Agent.ActTO/time.Second) {
				continue
			}
			r.must(`UPDATE agent_tasks SET rollback_started=NULL WHERE key=?`, key)
			r.alog(key, "failed", "Rollback: no usable result from the executor")
			continue
		}
		r.storeActions(key, resp)
		r.must(`UPDATE agent_tasks SET rollback_started=NULL, updated=? WHERE key=?`, r.now(), key)
		what := resp.State
		if resp.Refusal != nil {
			what += ": " + resp.Refusal.Reason
		}
		r.alog(key, "rollback", "Rollback: "+head(what, 300))
		body, _ := notify.Redact(fmt.Sprintf("[%s] Rollback %s\n\n%s", r.o.Name, head(what, 300), r.capabilityTable(id)), r.o.Allow)
		r.agentComment(key, head(body, maxComment), row.s("approved_by"))
	}
}

// stopAct removes the task's request that actd has not read yet; a step already running finishes, and the state
// change refuses the next one (actd reads the task again before every step).
func (r *run) stopAct(row row) {
	if r.o.Agent == nil || r.o.Agent.ActionsDir == "" || row.s("action_id") == "" || !capability.ValidActionID(row.s("action_id")) {
		return
	}
	os.Remove(r.actPath("in", row.s("action_id")))
}

// forwardStop passes an admin's signed stop to actd, which then refuses every approval of the task signed before it
// (the worker could otherwise set the task to approved again and replay the old approval).
func (r *run) forwardStop(row row, req map[string]any, by string) {
	a := r.o.Agent
	if a.ActionsDir == "" {
		return
	}
	key := row.s("key")
	// never silent: a stop that cannot reach actd for a task whose action reached it is an alert on the task and in
	// the log. The task stays stopped here, so this worker does no more work on it.
	alert := func(why string) {
		if row.s("action_id") == "" {
			return
		}
		msg := "ALERT: the stop was not passed to the executor (" + why + "); its earlier approval stays valid there until it is used or expires. " +
			"Check the approval key of the api, then stop again."
		r.alog(key, "alert", msg)
		r.must(`UPDATE agent_tasks SET error=? WHERE key=?`, head(msg, 300), key)
		r.log().Warn("task agent: stop not passed to actd", "task", key, "reason", why)
	}
	sig, _ := req["signature"].(string)
	if sig == "" {
		alert("the api did not sign it")
		return
	}
	at, ok := asInt(req["signed_at"])
	sha := storedPlanSHA(row.s("plan"))
	if !ok || sha == "" || orStr(req["plan_sha256"]) != sha {
		alert("it is not for the stored plan")
		return
	}
	id := capability.ActionID(row.s("key"), int(row.i("plan_version")), sha)
	if id == "" {
		return
	}
	var plan map[string]any
	json.Unmarshal([]byte(row.s("plan")), &plan)
	areq := actRequest(row, plan, id, sha)
	areq.Approver = by
	areq.SealedInputs = map[string]string{} // a stop is signed over no sealed inputs: it only revokes
	areq.Stop = &actd.Signed{Approver: by, At: at, Signature: signature(req)}
	b, _ := json.MarshalIndent(areq, "", " ")
	err := os.MkdirAll(filepath.Dir(r.actPath("in", id)), 0o700)
	if err == nil {
		err = writeAtomic(r.actPath("in", id), b, 0o600)
	}
	if err != nil {
		alert("the request file cannot be written: " + err.Error())
	}
}
