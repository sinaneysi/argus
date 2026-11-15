package capability

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func goodApproval() Approval {
	return Approval{PlanVersion: 2, StoredSHA256: sha, ApprovedVersion: 2, HashColumn: true, ApprovedSHA256: sha,
		ApprovedBy: "ada", ApprovedAt: now.Add(-time.Hour), ApproverAdmin: true, ApproverEnabled: true, State: "approved",
		RequestVersion: 2, RequestSHA256: sha, RequestApprover: "ada"}
}

func testRegistry(t *testing.T) *Registry {
	ts := newTestServer(t)
	h := newCheck(t, ts)
	r, err := NewRegistry(h, writeCap{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(h, h); err == nil {
		t.Fatal("a duplicate name was registered")
	}
	return r
}

func goodStep(r *Registry) Step {
	return Step{Gate: Gate{Mode: ModeOn}, Env: Develop, Approval: goodApproval(), TTL: DefaultTTL, Now: now, Registry: r,
		Name: HTTPCheckName, Args: Args{"url": "http://svc.example.test/ok", "expect_status": 200}}
}

// TestRefusalRules has one case per D6 rule (1-4); rule 5 and the action-level checks are in internal/actd.
func TestRefusalRules(t *testing.T) {
	r := testRegistry(t)
	write := func(s *Step) { s.Name, s.Args = "test.write", Args{"key": "K"} }
	cases := []struct {
		name string
		mut  func(*Step)
		rule int
		code Code
	}{
		{"mode off", func(s *Step) { s.Gate.Mode = ModeOff }, 1, CodeModeOff},
		{"mode empty", func(s *Step) { s.Gate.Mode = "" }, 1, CodeModeOff},
		{"kill switch", func(s *Step) { s.Gate.Stopped = true }, 1, CodeKillSwitch},
		{"kill switch unreadable", func(s *Step) { s.Gate.StopErr = errors.New("no db") }, 1, CodeKillSwitch},
		{"broken chain", func(s *Step) { s.Gate.ChainErr = errors.New("line 3 changed") }, 1, CodeAuditChain},
		{"write tier on release", func(s *Step) { write(s); s.Env = Release }, 2, CodeEnvironment},
		{"write tier on pre-release", func(s *Step) { write(s); s.Env = PreRelease }, 2, CodeEnvironment},
		{"write tier on release before the gate", func(s *Step) { write(s); s.Env = Release; s.Gate.Mode = ModeOff }, 2, CodeEnvironment},
		{"write tier on none", func(s *Step) { write(s); s.Env = None }, 2, CodeEnvironment},
		{"unknown classification", func(s *Step) { s.Env = "staging" }, 2, CodeEnvironment},
		{"not approved", func(s *Step) { s.Approval.ApprovedVersion, s.Approval.ApprovedBy = 0, "" }, 3, CodeApproval},
		{"request version mismatch", func(s *Step) { s.Approval.RequestVersion = 1 }, 3, CodeApproval},
		{"approved version mismatch", func(s *Step) { s.Approval.ApprovedVersion = 1 }, 3, CodeApproval},
		{"request hash mismatch", func(s *Step) { s.Approval.RequestSHA256 = strings.Repeat("f", 64) }, 3, CodeApproval},
		{"approved hash mismatch", func(s *Step) { s.Approval.ApprovedSHA256 = strings.Repeat("f", 64) }, 3, CodeApproval},
		{"approver differs", func(s *Step) { s.Approval.RequestApprover = "mallory" }, 3, CodeApproval},
		{"approver not admin", func(s *Step) { s.Approval.ApproverAdmin = false }, 3, CodeApproval},
		{"approver disabled", func(s *Step) { s.Approval.ApproverEnabled = false }, 3, CodeApproval},
		{"approval too old", func(s *Step) { s.Approval.ApprovedAt = now.Add(-25 * time.Hour) }, 3, CodeApproval},
		{"no approved hash column", func(s *Step) { s.Approval.HashColumn, s.Approval.ApprovedSHA256 = false, "" }, 3, CodeApproval},
		{"task stopped", func(s *Step) { s.Approval.State = "stopped" }, 3, CodeApproval},
		{"task re-planning", func(s *Step) { s.Approval.State = "planning" }, 3, CodeApproval},
		{"approval in the future", func(s *Step) { s.Approval.ApprovedAt = now.Add(time.Hour) }, 3, CodeApproval},
		{"unknown capability", func(s *Step) { s.Name = "shell.exec" }, 4, CodeCapability},
		{"bad arguments", func(s *Step) { s.Args = Args{"url": 5, "expect_status": 200} }, 4, CodeArguments},
		{"unknown argument", func(s *Step) { s.Args["method"] = "DELETE" }, 4, CodeArguments},
		{"host not on the list", func(s *Step) { s.Args["url"] = "http://elsewhere.example.test/" }, 4, CodeAllowList},
		{"IP literal", func(s *Step) { s.Args["url"] = "http://10.1.2.3/" }, 4, CodeAllowList},
	}
	for _, c := range cases {
		s := goodStep(r)
		c.mut(&s)
		_, ref := CheckStep(s)
		if ref == nil {
			t.Errorf("%s: not refused", c.name)
			continue
		}
		if ref.Rule != c.rule || ref.Code != c.code || ref.Reason == "" {
			t.Errorf("%s: got rule %d %s (%s), want rule %d %s", c.name, ref.Rule, ref.Code, ref.Reason, c.rule, c.code)
		}
	}
}

func TestStepAllowed(t *testing.T) {
	r := testRegistry(t)
	for _, env := range []Env{Develop, PreRelease, Release, None} { // read tier: every environment (O8)
		s := goodStep(r)
		s.Env = env
		if c, ref := CheckStep(s); ref != nil || c == nil {
			t.Errorf("read tier on %s refused: %v", env, ref)
		}
	}
	s := goodStep(r)
	s.Name, s.Args = "test.write", Args{"key": "K"}
	if _, ref := CheckStep(s); ref != nil {
		t.Errorf("write tier on develop refused: %v", ref)
	}
	s = goodStep(r)
	s.Gate.Mode = ModeDryRun
	if _, ref := CheckStep(s); ref != nil {
		t.Errorf("dry-run refused: %v", ref)
	}
	s = goodStep(r)
	s.Approval.State = "running"
	if _, ref := CheckStep(s); ref != nil {
		t.Errorf("a running task refused: %v", ref)
	}
}

func TestLimits(t *testing.T) {
	if r := CheckLimits(Limits{Steps: 11, NewAction: true, PerDay: 20}); r == nil || r.Rule != 6 {
		t.Fatal("11 steps accepted")
	}
	if r := CheckLimits(Limits{Steps: 10, NewAction: true, ActionsToday: 19, PerDay: 20}); r != nil {
		t.Fatal(r)
	}
	if r := CheckLimits(Limits{Steps: 1, NewAction: true, ActionsToday: 20, PerDay: 20}); r == nil || r.Code != CodeLimit {
		t.Fatal("daily cap not applied")
	}
	if r := CheckLimits(Limits{Steps: 1, NewAction: false, ActionsToday: 20, PerDay: 20}); r != nil {
		t.Fatal("a repeated request of a started action counted again", r)
	}
}

func TestStart(t *testing.T) {
	if _, err := CheckStart("legacy", "on"); err == nil || AsRefusal(err).Rule != 7 {
		t.Fatal("the legacy layout started actd", err)
	}
	if _, err := CheckStart("", "off"); err == nil {
		t.Fatal("an unset layout started actd")
	}
	if m, err := CheckStart("volume", ""); err != nil || m != ModeOff {
		t.Fatal("the default mode is not off", m, err)
	}
	for _, bad := range []string{"enforce", "ON", "yes"} {
		if _, err := CheckStart("volume", bad); err == nil {
			t.Errorf("mode %q accepted", bad)
		}
	}
	for s, ok := range map[string]bool{"": true, "24h": true, "5m": true, "168h": true, "4m": false, "169h": false, "x": false} {
		if _, err := ParseTTL(s); (err == nil) != ok {
			t.Errorf("ParseTTL(%q): %v", s, err)
		}
	}
	if ValidPerDay(0) == nil || ValidPerDay(201) == nil || ValidPerDay(20) != nil {
		t.Fatal("ARGUS_ACTIONS_PER_DAY range")
	}
}
