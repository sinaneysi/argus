package capability

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
)

var testSchema = Schema{Fields: []Field{
	{Name: "key", Type: String, Required: true, MaxLen: 10, Pattern: regexp.MustCompile(`^[A-Z_]+$`)},
	{Name: "n", Type: Int, Min: 1, Max: 5},
	{Name: "codes", Type: IntOrList, MaxLen: 3, Min: 100, Max: 599},
	{Name: "flag", Type: Bool},
	{Name: "token", Type: String, Secret: true},
}}

func refusal(t *testing.T, err error, code Code) {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal %s, got %v", code, err)
	}
	if r.Code != code {
		t.Fatalf("want code %s, got %s (%s)", code, r.Code, r.Reason)
	}
}

func TestSchemaCheck(t *testing.T) {
	ok := []string{`{"key":"A_B"}`, `{"key":"A","n":5,"codes":[200,204],"flag":true,"token":"x"}`, `{"key":"A","codes":301}`}
	for _, s := range ok {
		a, err := ParseArgs([]byte(s))
		if err != nil {
			t.Fatal(s, err)
		}
		if err := testSchema.Check(a); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		"unknown key":    `{"key":"A","extra":1}`,
		"missing":        `{"n":1}`,
		"wrong type":     `{"key":5}`,
		"int as string":  `{"key":"A","n":"3"}`,
		"float":          `{"key":"A","n":2.5}`,
		"range":          `{"key":"A","n":6}`,
		"too long":       `{"key":"ABCDEFGHIJK"}`,
		"pattern":        `{"key":"a-b"}`,
		"newline":        `{"key":"A\nB"}`,
		"list too long":  `{"key":"A","codes":[200,201,202,203]}`,
		"list range":     `{"key":"A","codes":[200,99]}`,
		"empty list":     `{"key":"A","codes":[]}`,
		"nested object":  `{"key":"A","codes":{"a":1}}`,
		"bool as string": `{"key":"A","flag":"true"}`,
	}
	for name, s := range bad {
		a, err := ParseArgs([]byte(s))
		if err == nil {
			err = testSchema.Check(a)
		}
		if err == nil {
			t.Errorf("%s: %s accepted", name, s)
			continue
		}
		refusal(t, err, CodeArguments)
	}
}

func TestParseArgsLimits(t *testing.T) {
	big := `{"key":"` + strings.Repeat("A", MaxArgsBytes) + `"}`
	if _, err := ParseArgs([]byte(big)); err == nil {
		t.Fatal("arguments over 2 KB accepted")
	}
	for _, s := range []string{`[1,2]`, `"x"`, `{"a":1} {"b":2}`, `{bad`} {
		if _, err := ParseArgs([]byte(s)); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
	if a, err := ParseArgs(nil); err != nil || len(a) != 0 {
		t.Fatal("empty arguments", a, err)
	}
	// built in code (not parsed) and too large
	if err := testSchema.Check(Args{"key": "A", "token": strings.Repeat("x", MaxArgsBytes)}); err == nil {
		t.Fatal("Check accepted arguments over 2 KB")
	}
}

func TestRedactedNeverHoldsSecret(t *testing.T) {
	fake := "gl" + "pat-" + "supersecretvalue1234"
	a := Args{"key": "A", "token": fake, "unknown": "maybe-secret"}
	r := testSchema.Redacted(a)
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "supersecret") || strings.Contains(string(b), "maybe-secret") {
		t.Fatalf("a secret or unknown value leaked: %s", b)
	}
	if r["token"] != Redacted || r["key"] != "A" || r["unknown"] != Redacted {
		t.Fatalf("redacted = %v", r)
	}
	if a["token"] != fake {
		t.Fatal("Redacted changed its input")
	}
}

func TestActionID(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	if got := ActionID("OPS-12", 3, sha); got != "OPS-12.v3."+sha[:12] || !ValidActionID(got) {
		t.Fatal(got)
	}
	for _, k := range []string{"", "../x", "a/b", "a b", strings.Repeat("a", 70)} {
		if ActionID(k, 1, sha) != "" {
			t.Errorf("key %q accepted", k)
		}
	}
	if ActionID("OPS-1", 0, sha) != "" || ActionID("OPS-1", 1, "abc") != "" {
		t.Fatal("bad version or hash accepted")
	}
}

func TestPlanSHA256(t *testing.T) {
	cl := map[string]any{"environment": "develop", "kind": "change"}
	p1 := []any{map[string]any{"n": 1, "capability": "http.check", "args": map[string]any{"url": "https://a.example.test/", "expect_status": 200}}}
	p2 := []any{map[string]any{"n": 1, "capability": "http.check", "args": map[string]any{"url": "https://a.example.test/", "expect_status": 201}}}
	a, _ := PlanSHA256(cl, p1)
	b, _ := PlanSHA256(cl, p1)
	c, _ := PlanSHA256(cl, p2)
	if a != b || a == c || len(a) != 64 {
		t.Fatal(a, b, c)
	}
}
