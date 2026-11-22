// Package capability is the contract of the capability executor (ADR-0022 D3): fixed write and read capabilities run
// by Go code, never by the model. The worker uses it to validate a plan, `argus actd` uses it to validate again and
// to run each step: Validate, Plan, Apply, Verify and, for a Write-tier capability, Rollback. Standard library only.
//
// The refusal rules (ADR-0022 D6) are in refuse.go; they are fixed in code and not configurable.
package capability

import (
	"context"
	"errors"
)

// Tier is how much a capability may change.
type Tier int

const (
	Read  Tier = iota + 1 // reads only; may run for every environment (O8)
	Write                 // changes a target; runs only when the task's classification is develop (O4)
)

func (t Tier) String() string {
	switch t {
	case Read:
		return "read"
	case Write:
		return "write"
	}
	return "unknown"
}

// Env is the task's classification (its environment).
type Env string

// The environments a task can be classified as.
const (
	Develop    Env = "develop"
	PreRelease Env = "pre-release"
	Release    Env = "release"
	None       Env = "none"
)

// Known reports whether e is one of the task classifications.
func (e Env) Known() bool { return e == Develop || e == PreRelease || e == Release || e == None }

// Args are the arguments of one step: a flat JSON object, at most MaxArgsBytes (schema.go).
type Args map[string]any

// Plan is what a capability found before it changes anything.
type Plan struct {
	Change  string `json:"change"`           // none (read-only) | created | updated | unchanged | deleted
	Summary string `json:"summary"`          // one line, no secret value
	Digest  string `json:"digest,omitempty"` // precondition digest; Apply refuses when the target no longer matches it
}

// Result is the redacted outcome of one phase. Fields holds the capability's fixed, non-secret result fields.
type Result struct {
	Status string         `json:"status"` // ok | mismatch | unchanged | failed
	Detail string         `json:"detail,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// BackupRef names the sealed old value of a Write-tier step ("backup:<action-id>/<step>"); "" for a Read-tier one.
type BackupRef string

// Capability is one fixed operation (ADR-0022 D3).
type Capability interface {
	Name() string   // "http.check"
	Tier() Tier     // Read | Write
	Schema() Schema // the fields: type, required, pattern, max length, secret, allow-list key
	// Validate checks the schema, the allow-lists and the environment rule. It never uses the network.
	Validate(args Args, env Env) error
	// Plan is read-only: the current state, what would change, the precondition digest.
	Plan(ctx context.Context, args Args) (Plan, error)
	// Apply runs the step; it returns ErrPrecondition when the target changed since Plan.
	Apply(ctx context.Context, args Args, plan Plan) (Result, BackupRef, error)
	Verify(ctx context.Context, args Args, plan Plan) (Result, error)
	// Rollback restores a backup; a Read-tier capability returns ErrNoRollback.
	Rollback(ctx context.Context, ref BackupRef) (Result, error)
}

var (
	// ErrPrecondition: the target changed between Plan and Apply (D6 rule 5). Nothing was written.
	ErrPrecondition = errors.New("the target changed between plan and apply")
	// ErrNoRollback: a Read-tier capability has nothing to roll back.
	ErrNoRollback = errors.New("a read-tier capability has nothing to roll back")
)
