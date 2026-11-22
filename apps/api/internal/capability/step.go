package capability

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// StepInfo is what actd tells a capability about the step it runs: the action, the step number and the opened
// sealed inputs this step refers to (by name). A capability keeps the input values in memory only: never in a
// result, a detail, an error or a log.
type StepInfo struct {
	ActionID string
	Step     int
	Inputs   map[string]string
}

type stepKey struct{}

// WithStep is ctx carrying the step's information (actd sets it before Plan, Apply, Verify and Rollback).
func WithStep(ctx context.Context, s StepInfo) context.Context {
	return context.WithValue(ctx, stepKey{}, s)
}

// StepFrom is the step's information; false outside actd.
func StepFrom(ctx context.Context) (StepInfo, bool) {
	s, ok := ctx.Value(stepKey{}).(StepInfo)
	return s, ok && ValidActionID(s.ActionID) && s.Step > 0
}

// Input is the opened value of the sealed input a reference "input:<NAME>" names.
func Input(ctx context.Context, ref string) (string, error) {
	if !InputRefRe.MatchString(ref) {
		return "", Refuse(4, CodeArguments, "not an input reference")
	}
	s, ok := StepFrom(ctx)
	name := strings.TrimPrefix(ref, "input:")
	v, have := s.Inputs[name]
	if !ok || !have {
		return "", Refuse(3, CodeApproval, "the sealed input %s was not given at approval", name)
	}
	return v, nil
}

// BackupRefOf is "backup:<action-id>/<step>".
func BackupRefOf(actionID string, step int) BackupRef {
	return BackupRef(fmt.Sprintf("backup:%s/%d", actionID, step))
}

var backupRefRe = regexp.MustCompile(`^backup:([A-Za-z0-9][A-Za-z0-9_-]{0,63}\.v[0-9]{1,9}\.[0-9a-f]{12})/([1-9][0-9]{0,3})$`)

// ParseBackupRef reads "backup:<action-id>/<step>".
func ParseBackupRef(ref BackupRef) (string, int, error) {
	m := backupRefRe.FindStringSubmatch(string(ref))
	if m == nil {
		return "", 0, errors.New("not a backup reference")
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], n, nil
}

// Backups keeps the sealed old values of write steps (argus-act/backups, sealed with the executor key). meta is
// stored next to the sealed value and bound to it (additional data), so it cannot be changed; it holds no secret.
type Backups interface {
	Put(ref BackupRef, meta string, plain []byte) error
	Get(ref BackupRef) (meta string, plain []byte, err error)
}
