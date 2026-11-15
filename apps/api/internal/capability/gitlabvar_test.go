package capability_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/capability/gitlabtest"
)

// secretValue is the synthetic variable value every test checks is never in a plan, a result or an error.
var secretValue = "s3cr3t-" + "VALUE-" + "do-not-leak-0042"

const actionID = "OPS-9.v1.abcdefabcdef"

type glFx struct {
	stub *gitlabtest.Stub
	bk   *gitlabtest.Backups
	reg  *capability.Registry
	gl   *capability.GitLab
}

func newGL(t *testing.T, projects ...string) *glFx {
	t.Helper()
	if len(projects) == 0 {
		projects = []string{"shop/checkout"}
	}
	f := &glFx{stub: gitlabtest.New(t)}
	f.bk = &gitlabtest.Backups{Stub: f.stub}
	reg, err := capability.Default(capability.Options{GitLab: capability.GitLabOptions{URL: gitlabtest.URL, Projects: projects, AllowPlain: true,
		Token: func() (string, error) { return gitlabtest.Token, nil }, Backups: f.bk, Transport: f.stub.Transport()}})
	if err != nil {
		t.Fatal(err)
	}
	f.reg, f.gl = reg, reg.GitLab
	return f
}

func (f *glFx) cap(t *testing.T, name string) capability.Capability {
	c, ok := f.reg.Get(name)
	if !ok {
		t.Fatal("not registered:", name)
	}
	return c
}

func stepCtx(step int, inputs map[string]string) context.Context {
	return capability.WithStep(context.Background(), capability.StepInfo{ActionID: actionID, Step: step, Inputs: inputs})
}

func setArgs(extra ...any) capability.Args {
	a := capability.Args{"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop", "value_ref": "input:NEW_TOKEN"}
	for i := 0; i+1 < len(extra); i += 2 {
		a[extra[i].(string)] = extra[i+1]
	}
	return a
}

// noLeak fails when the value appears in any of the things.
func noLeak(t *testing.T, things ...any) {
	t.Helper()
	for _, x := range things {
		b, _ := json.Marshal(x)
		if e, ok := x.(error); ok && e != nil {
			b = []byte(e.Error())
		}
		if strings.Contains(string(b), secretValue) || strings.Contains(string(b), "s3cr3t") {
			t.Fatalf("the value leaked: %s", b)
		}
	}
}

func TestGitLabScopeIsDevelopOnly(t *testing.T) {
	f := newGL(t)
	for _, name := range []string{capability.GitLabVarGet, capability.GitLabVarSet, capability.GitLabVarDelete} {
		c := f.cap(t, name)
		for _, sc := range []any{"*", "", "pre-release", "release", "Develop", "develop ", nil} {
			a := capability.Args{"project": "shop/checkout", "key": "K", "environment_scope": sc}
			if name == capability.GitLabVarSet {
				a["value_ref"] = "input:NEW_TOKEN"
			}
			if sc == nil {
				delete(a, "environment_scope")
			}
			err := c.Validate(a, capability.Develop)
			if r := capability.AsRefusal(err); err == nil || r.Rule != 4 || !strings.Contains(r.Reason, "environment_scope") {
				t.Fatalf("%s scope %q: %v", name, sc, err)
			}
			if _, err := c.Plan(stepCtx(1, map[string]string{"NEW_TOKEN": secretValue}), a); err == nil {
				t.Fatalf("%s scope %q planned", name, sc)
			}
		}
	}
	if n := len(f.stub.Calls()); n != 0 {
		t.Fatalf("%d calls before the scope was refused", n)
	}
	// a write capability for another classification is refused (O4)
	if err := f.cap(t, capability.GitLabVarSet).Validate(setArgs(), capability.PreRelease); capability.AsRefusal(err).Rule != 2 {
		t.Fatal(err)
	}
	if err := f.cap(t, capability.GitLabVarGet).Validate(capability.Args{"project": "shop/checkout", "key": "K", "environment_scope": "develop"}, capability.Release); err != nil {
		t.Fatal("a read is allowed for every classification (O8):", err)
	}
	// a literal value is refused: a secret enters only as a sealed input
	if err := f.cap(t, capability.GitLabVarSet).Validate(setArgs("value_ref", "hunter2-literal"), capability.Develop); err == nil {
		t.Fatal("a literal value was accepted")
	}
}

func TestGitLabProjectAllowList(t *testing.T) {
	f := newGL(t, "shop/checkout")
	get := f.cap(t, capability.GitLabVarGet)
	args := func(p string) capability.Args {
		return capability.Args{"project": p, "key": "K", "environment_scope": "develop"}
	}
	if err := get.Validate(args("shop/catalog"), capability.Develop); capability.AsRefusal(err).Code != capability.CodeAllowList {
		t.Fatal("a project not on the list:", err)
	}
	if err := get.Validate(args("102"), capability.Develop); capability.AsRefusal(err).Code != capability.CodeAllowList {
		t.Fatal("an id not on the list:", err)
	}
	if err := get.Validate(args("Shop/Checkout"), capability.Develop); err != nil {
		t.Fatal("a path is case-insensitive:", err)
	}
	// the id of the listed path is the same project, once the list is resolved
	if err := f.reg.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := get.Validate(args("101"), capability.Develop); err != nil {
		t.Fatal("the id of a listed path:", err)
	}
	f.stub.Put(101, "K", gitlabtest.Var{Value: secretValue})
	f.stub.Reset()
	r, _, err := get.Apply(stepCtx(1, nil), args("101"), capability.Plan{})
	if err != nil || r.Fields["exists"] != true || r.Fields["project_id"] != int64(101) {
		t.Fatal(r, err)
	}
	for _, c := range f.stub.Calls() {
		if !strings.HasPrefix(c.Path, gitlabtest.Prefix+"/api/v4/projects/101/variables/") {
			t.Fatal("a call to another path:", c.Path)
		}
	}
	noLeak(t, r)
	// and the other way round: an id on the list, the path in the step
	g := newGL(t, "101")
	if err := g.reg.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.cap(t, capability.GitLabVarGet).Validate(args("shop/checkout"), capability.Develop); err != nil {
		t.Fatal("the path of a listed id:", err)
	}
	if err := g.cap(t, capability.GitLabVarGet).Validate(args("shop/catalog"), capability.Develop); err == nil {
		t.Fatal("another project")
	}
	if _, err := capability.ParseProjectList([]string{"../x"}); err == nil {
		t.Fatal("a bad allow-list entry")
	}
}

func TestGitLabSetCreateUpdateUnchanged(t *testing.T) {
	f := newGL(t)
	set := f.cap(t, capability.GitLabVarSet)
	in := map[string]string{"NEW_TOKEN": secretValue}
	ctx := stepCtx(1, in)
	if err := set.Validate(setArgs(), capability.Develop); err != nil {
		t.Fatal(err)
	}
	// create
	p, err := set.Plan(ctx, setArgs("masked", true))
	if err != nil || p.Change != "created" || p.Digest == "" {
		t.Fatal(p, err)
	}
	r, ref, err := set.Apply(ctx, setArgs("masked", true), p)
	if err != nil || r.Status != "ok" || ref != capability.BackupRefOf(actionID, 1) {
		t.Fatal(r, ref, err)
	}
	if v, ok := f.stub.Get(101, "API_TOKEN"); !ok || v.Value != secretValue || !v.Masked {
		t.Fatal(v)
	}
	vr, err := set.Verify(ctx, setArgs("masked", true), p)
	if err != nil || vr.Status != "ok" {
		t.Fatal(vr, err)
	}
	noLeak(t, p, r, vr)
	// unchanged: no write call
	f.stub.Reset()
	p2, err := set.Plan(stepCtx(2, in), setArgs("masked", true))
	if err != nil || p2.Change != "unchanged" {
		t.Fatal(p2, err)
	}
	r2, ref2, err := set.Apply(stepCtx(2, in), setArgs("masked", true), p2)
	if err != nil || r2.Status != "unchanged" || ref2 != "" || len(f.stub.Writes()) != 0 {
		t.Fatal(r2, ref2, err, f.stub.Writes())
	}
	// update: the backup is written before the write
	f.stub.Reset()
	in3 := map[string]string{"NEW_TOKEN": secretValue + "-rotated"}
	p3, err := set.Plan(stepCtx(3, in3), setArgs("protected", true))
	if err != nil || p3.Change != "updated" || !strings.Contains(p3.Summary, "protected false -> true") {
		t.Fatal(p3, err)
	}
	r3, ref3, err := set.Apply(stepCtx(3, in3), setArgs("protected", true), p3)
	if err != nil || r3.Status != "ok" || ref3 == "" {
		t.Fatal(r3, err)
	}
	if got := f.bk.Order[len(f.bk.Order)-1]; got != "backup "+string(ref3)+" after 0 writes" {
		t.Fatal("the backup was not written before the update:", got)
	}
	w := f.stub.Writes()
	if len(w) != 1 || w[0].Method != http.MethodPut || !strings.Contains(w[0].Query, "filter%5Benvironment_scope%5D=develop") {
		t.Fatal(w)
	}
	if v, _ := f.stub.Get(101, "API_TOKEN"); v.Value != secretValue+"-rotated" || !v.Protected || !v.Masked {
		t.Fatal("masked is kept, protected set:", v)
	}
	// rollback restores the old value and attributes
	rb, err := set.Rollback(stepCtx(3, nil), ref3)
	if err != nil || rb.Status != "ok" {
		t.Fatal(rb, err)
	}
	if v, _ := f.stub.Get(101, "API_TOKEN"); v.Value != secretValue || v.Protected || !v.Masked {
		t.Fatal("not restored:", v)
	}
	// rollback of the create deletes the variable
	rb1, err := set.Rollback(stepCtx(1, nil), ref)
	if _, ok := f.stub.Get(101, "API_TOKEN"); err != nil || rb1.Status != "ok" || ok {
		t.Fatal("the created variable is still there", rb1, err)
	}
	noLeak(t, p3, r3, rb, rb1)
}

func TestGitLabVerifyMismatch(t *testing.T) {
	f := newGL(t)
	set := f.cap(t, capability.GitLabVarSet)
	ctx := stepCtx(1, map[string]string{"NEW_TOKEN": secretValue})
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old-" + secretValue})
	p, _ := set.Plan(ctx, setArgs())
	f.stub.AfterW = func(s *gitlabtest.Stub) { // someone else writes right after Argus
		s.AfterW = nil
		s.Put(101, "API_TOKEN", gitlabtest.Var{Value: "other"})
	}
	if _, _, err := set.Apply(ctx, setArgs(), p); err != nil {
		t.Fatal(err)
	}
	vr, err := set.Verify(ctx, setArgs(), p)
	if err != nil || vr.Status != "mismatch" {
		t.Fatal(vr, err)
	}
	noLeak(t, vr)
}

func TestGitLabPrecondition(t *testing.T) {
	f := newGL(t)
	set, del := f.cap(t, capability.GitLabVarSet), f.cap(t, capability.GitLabVarDelete)
	ctx := stepCtx(1, map[string]string{"NEW_TOKEN": secretValue})
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "a"})
	p, _ := set.Plan(ctx, setArgs())
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "b"}) // changed between plan and apply
	f.stub.Reset()
	if _, ref, err := set.Apply(ctx, setArgs(), p); !errors.Is(err, capability.ErrPrecondition) || ref != "" || len(f.stub.Writes()) != 0 {
		t.Fatal("set after a change:", err, f.stub.Writes())
	}
	da := capability.Args{"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop"}
	dp, _ := del.Plan(ctx, da)
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "b", Protected: true})
	if _, _, err := del.Apply(ctx, da, dp); !errors.Is(err, capability.ErrPrecondition) || len(f.stub.Writes()) != 0 {
		t.Fatal("delete after a change:", err)
	}
	if len(f.bk.Order) != 0 {
		t.Fatal("a backup was written for a refused apply")
	}
	// a hidden value cannot be backed up: refused before any write
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "x", Hidden: true})
	if _, err := set.Plan(ctx, setArgs()); err == nil {
		t.Fatal("a hidden variable was planned")
	}
}

func TestGitLabDeleteAndRollback(t *testing.T) {
	f := newGL(t)
	del := f.cap(t, capability.GitLabVarDelete)
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: secretValue, Type: "file", Protected: true, Masked: true, Raw: true})
	ctx := stepCtx(2, nil)
	a := capability.Args{"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop"}
	p, err := del.Plan(ctx, a)
	if err != nil || p.Change != "deleted" {
		t.Fatal(p, err)
	}
	f.stub.Reset()
	r, ref, err := del.Apply(ctx, a, p)
	if err != nil || r.Status != "ok" || ref == "" {
		t.Fatal(r, err)
	}
	if got := f.bk.Order[0]; got != "backup "+string(ref)+" after 0 writes" {
		t.Fatal("no backup before the delete:", got)
	}
	vr, err := del.Verify(ctx, a, p)
	if err != nil || vr.Status != "ok" {
		t.Fatal(vr, err)
	}
	last := f.stub.Calls()[len(f.stub.Calls())-1]
	if last.Method != http.MethodGet {
		t.Fatal(last)
	}
	rb, err := del.Rollback(ctx, ref)
	if err != nil || rb.Status != "ok" {
		t.Fatal(rb, err)
	}
	if v, ok := f.stub.Get(101, "API_TOKEN"); !ok || v.Value != secretValue || v.Type != "file" || !v.Protected || !v.Masked || !v.Raw {
		t.Fatal("not recreated with the value and the attributes:", v, ok)
	}
	noLeak(t, p, r, vr, rb)
	// deleting a variable that does not exist: unchanged, no write
	f.stub.Reset()
	b := capability.Args{"project": "shop/checkout", "key": "NONE", "environment_scope": "develop"}
	p2, _ := del.Plan(ctx, b)
	r2, ref2, err := del.Apply(ctx, b, p2)
	if p2.Change != "unchanged" || r2.Status != "unchanged" || ref2 != "" || err != nil || len(f.stub.Writes()) != 0 {
		t.Fatal(p2, r2, err)
	}
}

func TestGitLabGetMatches(t *testing.T) {
	f := newGL(t)
	get := f.cap(t, capability.GitLabVarGet)
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: secretValue, Masked: true})
	a := capability.Args{"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop", "matches": "input:WANT"}
	r, _, err := get.Apply(stepCtx(1, map[string]string{"WANT": secretValue}), a, capability.Plan{})
	if err != nil || r.Status != "ok" || r.Fields["matches"] != true || r.Fields["masked"] != true {
		t.Fatal(r, err)
	}
	r2, _, err := get.Apply(stepCtx(1, map[string]string{"WANT": "other"}), a, capability.Plan{})
	if err != nil || r2.Status != "mismatch" || r2.Fields["matches"] != false {
		t.Fatal(r2, err)
	}
	// the result has fixed fields only: never the value, its length or a hash
	for k := range r.Fields {
		switch k {
		case "exists", "project_id", "variable_type", "protected", "masked", "raw", "matches":
		default:
			t.Fatal("an unexpected result field:", k)
		}
	}
	if _, _, err := get.Apply(stepCtx(1, nil), a, capability.Plan{}); err == nil {
		t.Fatal("matches without the sealed input")
	}
	noLeak(t, r, r2)
}

// The guard refuses pipelines, jobs, triggers and deployments even when the capability's own client asks for them.
func TestGitLabGuardRefusesPipelinesAndJobs(t *testing.T) {
	f := newGL(t)
	client := capability.GitLabClientForTest(f.gl)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v4/projects/101/pipeline"},
		{"POST", "/api/v4/projects/101/pipelines"},
		{"POST", "/api/v4/projects/101/jobs/7/play"},
		{"POST", "/api/v4/projects/101/jobs/7/retry"},
		{"GET", "/api/v4/projects/101/jobs"},
		{"POST", "/api/v4/projects/101/trigger/pipeline"},
		{"POST", "/api/v4/projects/101/deployments"},
		{"GET", "/api/v4/projects/101/variables/../pipeline"},
		{"GET", "/api/v4/projects/101/repository/files/x"},
		{"POST", "/api/v4/projects/101/variables/jobs/x"},
		{"GET", "/api/v4/projects/102"}, // a project lookup off the list
	} {
		req, _ := http.NewRequest(c.method, gitlabtest.URL+c.path, nil)
		if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "guard") {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
	}
	if n := len(f.stub.Calls()); n != 0 {
		t.Fatalf("%d calls reached GitLab", n)
	}
	// a variable may be named like a denied word
	f.stub.Put(101, "jobs", gitlabtest.Var{Value: "v"})
	r, _, err := f.cap(t, capability.GitLabVarGet).Apply(stepCtx(1, nil), capability.Args{"project": "shop/checkout", "key": "jobs", "environment_scope": "develop"}, capability.Plan{})
	if err != nil || r.Fields["exists"] != true {
		t.Fatal(r, err)
	}
}

// An attribute the step does not name must keep its old value: a write that drops masked is a mismatch.
func TestGitLabVerifyAttributeDrift(t *testing.T) {
	f := newGL(t)
	set := f.cap(t, capability.GitLabVarSet)
	ctx := stepCtx(1, map[string]string{"NEW_TOKEN": secretValue})
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old-" + secretValue, Masked: true, Protected: true})
	p, _ := set.Plan(ctx, setArgs())
	f.stub.AfterW = func(s *gitlabtest.Stub) {
		s.AfterW = nil
		v, _ := s.Get(101, "API_TOKEN")
		v.Masked = false // GitLab (or someone) dropped masked; the value is right
		s.Put(101, "API_TOKEN", v)
	}
	if _, _, err := set.Apply(ctx, setArgs(), p); err != nil {
		t.Fatal(err)
	}
	if vr, err := set.Verify(ctx, setArgs(), p); err != nil || vr.Status != "mismatch" {
		t.Fatal("drift not seen:", vr, err)
	}
	// without drift the same step verifies
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old2", Masked: true, Protected: true})
	ctx2 := stepCtx(2, map[string]string{"NEW_TOKEN": secretValue})
	p2, _ := set.Plan(ctx2, setArgs())
	set.Apply(ctx2, setArgs(), p2)
	if vr, err := set.Verify(ctx2, setArgs(), p2); err != nil || vr.Status != "ok" {
		t.Fatal(vr, err)
	}
}

// The description is kept by a set and restored by a rollback of a delete.
func TestGitLabDescriptionRestored(t *testing.T) {
	f := newGL(t)
	del := f.cap(t, capability.GitLabVarDelete)
	f.stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: secretValue, Desc: "the deploy token of the app"})
	a := capability.Args{"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop"}
	ctx := stepCtx(1, nil)
	p, _ := del.Plan(ctx, a)
	_, ref, err := del.Apply(ctx, a, p)
	if err != nil {
		t.Fatal(err)
	}
	if rb, err := del.Rollback(ctx, ref); err != nil || rb.Status != "ok" {
		t.Fatal(rb, err)
	}
	if v, _ := f.stub.Get(101, "API_TOKEN"); v.Desc != "the deploy token of the app" || v.Value != secretValue {
		t.Fatal("not restored:", v.Desc)
	}
	set := f.cap(t, capability.GitLabVarSet)
	sctx := stepCtx(2, map[string]string{"NEW_TOKEN": "new-" + secretValue})
	sp, _ := set.Plan(sctx, setArgs())
	set.Apply(sctx, setArgs(), sp)
	if v, _ := f.stub.Get(101, "API_TOKEN"); v.Desc != "the deploy token of the app" {
		t.Fatal("a set dropped the description:", v.Desc)
	}
}

// A literal where an input reference belongs is redacted in the audit form of the arguments.
func TestGitLabLiteralRedacted(t *testing.T) {
	f := newGL(t)
	set := f.cap(t, capability.GitLabVarSet)
	red := set.Schema().Redacted(setArgs("value_ref", secretValue))
	if red["value_ref"] != capability.Redacted || red["key"] != "API_TOKEN" {
		t.Fatal(red)
	}
	if r := set.Schema().Redacted(setArgs()); r["value_ref"] != "input:NEW_TOKEN" {
		t.Fatal("a reference is kept:", r)
	}
}
