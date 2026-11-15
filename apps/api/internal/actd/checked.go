package actd

// checked.go: an admin's signed verdict on a write step whose outcome was unknown (ADR-0022 PR 3). The write
// answered an error and its read-back did not show it applied, so actd kept the step's interrupted marker. A person
// checked the target and says "applied" (actd reads the target back again: the step is ok only when the read-back
// matches, then the action goes on and a rollback from the backup is possible) or "not-applied" (the step failed,
// the action ends). Either verdict clears the marker; a replay finds no marker and is refused.

import (
	"context"
	"fmt"

	"github.com/sinaneysi/argus/internal/capability"
)

func (d *Daemon) applyChecked(ctx context.Context, id string, t *task, req Request, base Record,
	audit func(Record) (Record, error)) (StepResult, *capability.Refusal) {
	ck := req.Checked
	var step *planStep
	for i := range t.steps {
		if t.steps[i].N == ck.Step && t.steps[i].Capability != "" {
			step = &t.steps[i]
		}
	}
	sr := StepResult{N: ck.Step, At: d.now().Unix()}
	if step == nil {
		return sr, capability.Refuse(4, capability.CodeCapability, "checked: step %d has no capability", ck.Step)
	}
	sr.Capability = step.Capability
	_, stored := d.store.Get(id, ck.Step)
	if stored || !d.store.Applying(id, ck.Step) {
		return sr, capability.Refuse(4, capability.CodeArguments, "checked: step %d has no unknown outcome to resolve", ck.Step)
	}
	cp, r := capability.CheckStep(capability.Step{Gate: d.gate(ctx), Env: t.env, Approval: t.approval, TTL: d.o.TTL, Now: d.now(),
		Registry: d.o.Registry, Name: step.Capability, Args: step.Args})
	if r != nil {
		return sr, r
	}
	if cp.Tier() != capability.Write {
		return sr, capability.Refuse(4, capability.CodeCapability, "checked: step %d is not a write step", ck.Step)
	}
	ref := capability.BackupRefOf(id, ck.Step)
	if _, _, err := d.vault().Get(ref); err != nil {
		ref = ""
	}
	sr.BackupRef = string(ref)
	record := func(result, detail string) bool {
		rec := base
		rec.Step, rec.Capability, rec.Args, rec.Phase, rec.Result, rec.Detail, rec.BackupRef, rec.Approver =
			ck.Step, cp.Name(), cp.Schema().Redacted(step.Args), PhaseChecked, result, detail, string(ref), ck.Approver
		out, err := audit(rec)
		if err == nil {
			sr.Audit = out.SHA256
		}
		return err == nil
	}
	sr.Phase = PhaseChecked
	// both verdicts read the target back first (expected state = the backup's plus the step's arguments)
	inputs, r := d.openInputs(id, t.approval.StoredSHA256, cp, *step, req.SealedInputs)
	if r != nil {
		return sr, r
	}
	cctx, cancel := context.WithTimeout(capability.WithStep(ctx, capability.StepInfo{ActionID: id, Step: ck.Step, Inputs: inputs}), capability.MaxCall)
	vres, verr := cp.Verify(cctx, step.Args, capability.Plan{Change: "updated"})
	cancel()
	clear(inputs)
	matches := verr == nil && (vres.Status == "ok" || vres.Status == "unchanged")
	if ck.Verdict == capability.VerdictNotApplied {
		if matches {
			return sr, capability.Refuse(5, capability.CodeInterrupted, "checked: the verdict is contradicted by the read-back: step %d's target holds the wanted state; the marker stays", ck.Step)
		}
		readBack := vres.Detail
		if verr != nil {
			readBack = verr.Error()
		}
		sr.Status = StatusFailedChecked
		sr.Detail = Clean(fmt.Sprintf("%s checked the target: not applied; the read-back agrees (%s); the action ends", clip(ck.Approver, 64), readBack))
		if !record("failed", sr.Detail) {
			return sr, capability.Refuse(1, capability.CodeAuditChain, "the audit record could not be written")
		}
		if err := d.store.Put(id, sr); err != nil {
			return sr, capability.Refuse(5, capability.CodeInterrupted, "checked: the result could not be stored: %v", err)
		}
		d.store.ClearApplying(id, ck.Step)
		return sr, nil
	}
	if !matches {
		why := vres.Detail
		if verr != nil {
			why = verr.Error()
		}
		return sr, capability.Refuse(5, capability.CodeInterrupted, "checked: the read-back does not show step %d applied (%s); the marker stays", ck.Step, Clean(why))
	}
	// applied: the admin's word is not enough, the target read back as the step wants it (checked above)
	sr.Status, sr.Detail, sr.Fields = "ok", Clean(fmt.Sprintf("%s checked the target: applied; the read-back matches. %s", clip(ck.Approver, 64), vres.Detail)), vres.Fields
	if !record("ok", sr.Detail) {
		return sr, capability.Refuse(1, capability.CodeAuditChain, "the audit record could not be written")
	}
	if err := d.store.Put(id, sr); err != nil {
		return sr, capability.Refuse(5, capability.CodeInterrupted, "checked: the result could not be stored: %v", err)
	}
	d.store.ClearApplying(id, ck.Step)
	return sr, nil
}
