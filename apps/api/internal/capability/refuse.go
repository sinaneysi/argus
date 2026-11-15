package capability

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Mode is ARGUS_ACTIONS_MODE (ADR-0022 D8 layer 1).
type Mode string

const (
	ModeOff    Mode = "off"     // nothing runs; every request is refused with a reason (the default)
	ModeDryRun Mode = "dry-run" // only Plan runs for each step
	ModeOn     Mode = "on"
)

// ParseMode reads ARGUS_ACTIONS_MODE; empty is off, any other value is an error (actd does not start).
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.TrimSpace(s)); m {
	case "":
		return ModeOff, nil
	case ModeOff, ModeDryRun, ModeOn:
		return m, nil
	}
	return "", fmt.Errorf("ARGUS_ACTIONS_MODE must be off, dry-run or on")
}

// Code names a refusal reason.
type Code string

const (
	CodeModeOff      Code = "mode-off"
	CodeKillSwitch   Code = "kill-switch"
	CodeAuditChain   Code = "audit-chain"
	CodeEnvironment  Code = "environment"
	CodeApproval     Code = "approval"
	CodeCapability   Code = "unknown-capability"
	CodeArguments    Code = "arguments"
	CodeAllowList    Code = "allow-list"
	CodePrecondition Code = "precondition"
	CodeLimit        Code = "limit"
	CodeLayout       Code = "layout"
	CodeStepMode     Code = "step-mode"   // a step's mode does not fit its capability's tier
	CodeInterrupted  Code = "interrupted" // an earlier apply stopped without a result: a person checks the target
)

// Refusal is a refused step or action: the D6 rule number, a code and a reason for the task page.
type Refusal struct {
	Rule   int    `json:"rule"`
	Code   Code   `json:"code"`
	Reason string `json:"reason"`
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("refused (rule %d, %s): %s", r.Rule, r.Code, r.Reason)
}

// Refuse builds a refusal.
func Refuse(rule int, code Code, format string, a ...any) *Refusal {
	return &Refusal{Rule: rule, Code: code, Reason: fmt.Sprintf(format, a...)}
}

// AsRefusal returns err as a refusal; an error that is not one becomes a rule-4 argument refusal.
func AsRefusal(err error) *Refusal {
	if err == nil {
		return nil
	}
	var r *Refusal
	if errors.As(err, &r) {
		return r
	}
	return Refuse(4, CodeArguments, "%s", err.Error())
}

// The fixed limits of D6 rule 6.
const (
	MaxSteps   = 10               // capability steps in one plan
	MaxCall    = 30 * time.Second // one capability call
	MinTTL     = 5 * time.Minute  // ARGUS_ACTIONS_APPROVAL_TTL range
	MaxTTL     = 168 * time.Hour  //
	clockSkew  = 5 * time.Minute  // an approval time this far in the future is refused
	maxPerDay  = 200              // ARGUS_ACTIONS_PER_DAY range
	minPerDay  = 1                //
	DefaultTTL = 24 * time.Hour   // O9
)

// ParseTTL reads ARGUS_ACTIONS_APPROVAL_TTL (default 24h, 5m..168h).
func ParseTTL(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultTTL, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d < MinTTL || d > MaxTTL {
		return 0, fmt.Errorf("ARGUS_ACTIONS_APPROVAL_TTL must be a duration from 5m to 168h")
	}
	return d, nil
}

// ValidPerDay checks ARGUS_ACTIONS_PER_DAY.
func ValidPerDay(n int) error {
	if n < minPerDay || n > maxPerDay {
		return fmt.Errorf("ARGUS_ACTIONS_PER_DAY must be %d..%d", minPerDay, maxPerDay)
	}
	return nil
}

// CheckStart is D6 rule 7 and D8 layer 1: actd does not start in the legacy layout or with an unknown mode.
func CheckStart(layout, mode string) (Mode, error) {
	if layout != "volume" {
		return "", Refuse(7, CodeLayout, "actd runs only in ARGUS_LAYOUT=volume")
	}
	return ParseMode(mode)
}

// Gate is the state every step is checked against first (D6 rule 1).
type Gate struct {
	Mode     Mode
	Stopped  bool  // auth_settings actions.stopped = 1 (D8 layer 2)
	StopErr  error // the switch could not be read: treated as stopped
	ChainErr error // the audit chain is broken or unreadable
}

// CheckGate is D6 rule 1.
func CheckGate(g Gate) *Refusal {
	switch {
	case g.Mode != ModeOn && g.Mode != ModeDryRun:
		return Refuse(1, CodeModeOff, "ARGUS_ACTIONS_MODE is off: no capability runs")
	case g.StopErr != nil:
		return Refuse(1, CodeKillSwitch, "the switch \"Stop all actions\" cannot be read, so actions are stopped")
	case g.Stopped:
		return Refuse(1, CodeKillSwitch, "an admin stopped all actions (Settings, Task agent)")
	case g.ChainErr != nil:
		return Refuse(1, CodeAuditChain, "the audit chain is broken: %s", g.ChainErr.Error())
	}
	return nil
}

// Approval is what the databases say about the task's approval, next to what the request claims (D4, D6 rule 3).
type Approval struct {
	PlanVersion     int    // agent_tasks.plan_version
	StoredSHA256    string // sha256 of the stored plan, computed by actd (PlanSHA256)
	ApprovedVersion int    // agent_tasks.approved_version
	HashColumn      bool   // the board database has agent_tasks.approved_sha256 (board migration, PR 2); false: refused
	ApprovedSHA256  string // agent_tasks.approved_sha256
	ApprovedBy      string
	ApprovedAt      time.Time
	ApproverAdmin   bool   // auth.db: role admin
	ApproverEnabled bool   // auth.db: not disabled
	State           string // agent_tasks.state: only approved and running tasks execute

	RequestVersion  int
	RequestSHA256   string
	RequestApprover string
}

// ApprovedStates are the task states in which an approved plan may execute.
var ApprovedStates = map[string]bool{"approved": true, "running": true}

// CheckApproval is D6 rule 3.
func CheckApproval(a Approval, ttl time.Duration, now time.Time) *Refusal {
	switch {
	case a.ApprovedVersion <= 0 || a.ApprovedBy == "" || a.ApprovedAt.IsZero():
		return Refuse(3, CodeApproval, "the plan is not approved")
	case !ApprovedStates[a.State]:
		return Refuse(3, CodeApproval, "the task is %q, not approved or running (stopped, failed or re-planned)", clipName(a.State))
	case !a.HashColumn:
		return Refuse(3, CodeApproval, "the board database records no approved plan hash (agent_tasks.approved_sha256): no step runs until it does")
	case a.RequestVersion != a.PlanVersion || a.ApprovedVersion != a.PlanVersion:
		return Refuse(3, CodeApproval, "plan version mismatch: request v%d, stored v%d, approved v%d", a.RequestVersion, a.PlanVersion, a.ApprovedVersion)
	case a.StoredSHA256 == "" || a.RequestSHA256 != a.StoredSHA256:
		return Refuse(3, CodeApproval, "plan hash mismatch: the request does not carry the hash of the stored plan")
	case a.ApprovedSHA256 != a.StoredSHA256:
		return Refuse(3, CodeApproval, "plan hash mismatch: the approved hash is not the hash of the stored plan")
	case a.RequestApprover != a.ApprovedBy:
		return Refuse(3, CodeApproval, "the request names another approver than the database")
	case !a.ApproverAdmin:
		return Refuse(3, CodeApproval, "the approver is not an admin")
	case !a.ApproverEnabled:
		return Refuse(3, CodeApproval, "the approver is disabled")
	case a.ApprovedAt.After(now.Add(clockSkew)):
		return Refuse(3, CodeApproval, "the approval time is in the future")
	case ttl <= 0 || now.Sub(a.ApprovedAt) > ttl:
		return Refuse(3, CodeApproval, "the approval is older than %s", ttl)
	}
	return nil
}

// Step is one capability step as actd checks it before it runs.
type Step struct {
	Gate     Gate
	Env      Env // the task's classification, from board.db
	Approval Approval
	TTL      time.Duration
	Now      time.Time
	Registry *Registry
	Name     string // the capability
	Args     Args
}

// CheckStep applies D6 rules 1 to 4 to one step, in this order: a Write-tier step for release or pre-release is
// refused before any other check; then the gate (mode, kill switch, chain), the environment rule, the approval, and
// the capability with its arguments and allow-lists. It returns the capability when the step may run.
func CheckStep(s Step) (Capability, *Refusal) {
	var c Capability
	if s.Registry != nil {
		c, _ = s.Registry.Get(s.Name)
	}
	if c != nil && c.Tier() == Write && (s.Env == Release || s.Env == PreRelease) {
		return nil, Refuse(2, CodeEnvironment, "a write-tier capability never runs for %s", s.Env)
	}
	if r := CheckGate(s.Gate); r != nil {
		return nil, r
	}
	if !s.Env.Known() {
		return nil, Refuse(2, CodeEnvironment, "the task's classification is unknown")
	}
	if c != nil && c.Tier() == Write && s.Env != Develop {
		return nil, Refuse(2, CodeEnvironment, "a write-tier capability runs only for develop, the task is %s", s.Env)
	}
	if r := CheckApproval(s.Approval, s.TTL, s.Now); r != nil {
		return nil, r
	}
	if c == nil {
		return nil, Refuse(4, CodeCapability, "unknown capability %q (or its allow-list is empty)", clipName(s.Name))
	}
	if err := c.Validate(s.Args, s.Env); err != nil {
		return nil, AsRefusal(err)
	}
	return c, nil
}

// Limits is D6 rule 6 for one action.
type Limits struct {
	Steps        int  // capability steps in the plan
	NewAction    bool // false: a repeated request of an action already started
	ActionsToday int  // actions started today
	PerDay       int  // ARGUS_ACTIONS_PER_DAY
}

// CheckLimits is D6 rule 6 (one action at a time and 30 s per call are enforced by actd itself).
func CheckLimits(l Limits) *Refusal {
	if l.Steps > MaxSteps {
		return Refuse(6, CodeLimit, "%d capability steps, at most %d", l.Steps, MaxSteps)
	}
	if l.NewAction && l.ActionsToday >= l.PerDay {
		return Refuse(6, CodeLimit, "%d actions today, at most %d (ARGUS_ACTIONS_PER_DAY)", l.ActionsToday, l.PerDay)
	}
	return nil
}
