package capability

import (
	"encoding/json"
	"testing"
)

func TestStrictJSON(t *testing.T) {
	for in, ok := range map[string]bool{
		`{"a": 1, "b": [1, 2, {"c": 9007199254740992}]}`: true,
		`{"a": -9007199254740992, "f": 1.5}`:             true,
		`{"a": 1, "a": 2}`:                               false,
		`{"x": {"a": 1, "a": 2}}`:                        false,
		`{"a": 9007199254740993}`:                        false,
		`{"a": -9007199254740993}`:                       false,
		`{"a": 1e300}`:                                   false,
		`{"a": 1} {"b": 2}`:                              false,
		"{\"a\": \"\xff\"}":                              false,
		`{"a": `:                                         false,
	} {
		if err := StrictJSON([]byte(in)); (err == nil) != ok {
			t.Errorf("%s: %v", in, err)
		}
	}
}

// TestStoredPlanHashIsExact: the hash reads numbers exactly, so two plans that differ only past float64 precision
// hash differently, and the stored text round-trips to the same hash.
func TestStoredPlanHashIsExact(t *testing.T) {
	a := `{"classification": {"environment": "develop"}, "plan": [{"n": 1, "args": {"x": 9007199254740993}}]}`
	b := `{"classification": {"environment": "develop"}, "plan": [{"n": 1, "args": {"x": 9007199254740992}}]}`
	if StoredPlanSHA256(a) == StoredPlanSHA256(b) || StoredPlanSHA256(a) == "" {
		t.Fatal("numbers past 2^53 were rounded")
	}
	c, p, _ := DecodePlan([]byte(a))
	re, _ := json.Marshal(map[string]any{"classification": c, "plan": p})
	if StoredPlanSHA256(string(re)) != StoredPlanSHA256(a) {
		t.Fatal("the stored text does not round-trip")
	}
	if StoredPlanSHA256("") != "" || StoredPlanSHA256("{") != "" {
		t.Fatal("an unreadable plan has a hash")
	}
}
