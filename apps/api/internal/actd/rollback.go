package actd

import (
	"context"

	"github.com/sinaneysi/argus/internal/capability"
)

// rollback is the manual rollback (D7, request agent-rollback {action_id, step}): an admin's signed rollback of one
// write-tier step that this action applied. It runs under D6 (the gate was checked by Process), with the stored
// plan unchanged, and is audited as phase rollback. The task's state does not matter here: the action is over.
func (d *Daemon) rollback(ctx context.Context, req Request, t *task, c *confirmed) *Response {
	id, rb := req.ActionID, req.Rollback
	if t.approval.PlanVersion != req.PlanVersion || t.approval.StoredSHA256 != req.PlanSHA256 {
		return d.refuseAll(c, id, capability.Refuse(3, capability.CodeApproval, "rollback: the stored plan is not the plan of this action"), true)
	}
	if r := d.checkSigned(ctx, capability.SignRollback, t, req, rb); r != nil {
		return d.refuseAll(c, id, r, true)
	}
	var step *planStep
	for i := range t.steps {
		if t.steps[i].N == rb.Step && t.steps[i].Capability != "" {
			step = &t.steps[i]
		}
	}
	var cp capability.Capability
	if step != nil {
		cp, _ = d.o.Registry.Get(step.Capability)
	}
	if cp == nil || cp.Tier() != capability.Write {
		return d.refuseAll(c, id, capability.Refuse(4, capability.CodeCapability, "rollback: step %d has no write-tier capability, so nothing to roll back", rb.Step), true)
	}
	if t.env != capability.Develop {
		return d.refuseAll(c, id, capability.Refuse(2, capability.CodeEnvironment, "rollback: a write-tier capability runs only for develop"), true)
	}
	prev, ok := d.store.Get(id, rb.Step)
	switch {
	case !ok || prev.BackupRef == "":
		return d.refuseAll(c, id, capability.Refuse(4, capability.CodeArguments, "rollback: step %d has no stored backup", rb.Step), true)
	case prev.Phase == PhaseRollback:
		return d.refuseAll(c, id, capability.Refuse(4, capability.CodeArguments, "rollback: step %d is already rolled back (%s)", rb.Step, prev.Status), true)
	}
	resp := &Response{ActionID: id, TaskKey: c.TaskKey, Mode: string(d.o.Mode), State: StateDone, Steps: []StepResult{}, Started: d.now().Unix()}
	cctx, cancel := context.WithTimeout(ctx, capability.MaxCall)
	res, err := cp.Rollback(cctx, capability.BackupRef(prev.BackupRef))
	cancel()
	result, detail := res.Status, res.Detail
	if err != nil || result == "" {
		result = "failed"
	}
	if err != nil {
		detail = err.Error()
	}
	sr := StepResult{N: rb.Step, Capability: cp.Name(), Phase: PhaseRollback, Status: "rolled-back", Detail: Clean(detail), BackupRef: prev.BackupRef, At: d.now().Unix()}
	if result == "failed" {
		sr.Status, resp.State = "rollback-failed", StateFailed
	}
	var events []Record
	rec := Record{ActionID: id, TaskKey: c.TaskKey, Approver: rb.Approver, PlanVersion: c.PlanVersion, PlanSHA256: c.PlanSHA256, Step: rb.Step,
		Capability: cp.Name(), Args: cp.Schema().Redacted(step.Args), Phase: PhaseRollback, Result: result, Detail: sr.Detail, BackupRef: prev.BackupRef}
	if d.audit == nil {
		sr.Status, sr.Refusal, resp.State = StateRefused, capability.Refuse(1, capability.CodeAuditChain, "the audit record could not be written"), StateFailed
	} else if out, err := d.audit.Append(rec, d.now()); err != nil {
		sr.Detail, resp.State = "the rollback ran but its audit record could not be written", StateFailed
	} else {
		sr.Audit = out.SHA256
		events = append(events, out)
	}
	if err := d.store.Put(id, sr); err != nil {
		d.log().Error("rollback result not stored", "action", id, "step", rb.Step, "error", err.Error())
	}
	resp.Steps = append(resp.Steps, sr)
	if d.audit != nil {
		resp.AuditHead, _ = d.audit.Head()
	}
	d.finish(resp, c, events)
	d.log().Info("rollback", "action", id, "step", rb.Step, "state", resp.State)
	return resp
}
