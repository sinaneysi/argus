package capability

// canon.go: one reading of a stored plan for the page, the hash and the execution. Numbers are decoded as
// json.Number (exact), so the hash an admin approves is the hash of the arguments actd runs. The model's intake
// output is checked strictly first: valid UTF-8, no duplicate keys, integers within ±2^53.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"
)

// MaxSafeInt is the largest integer every JSON reader keeps exactly (2^53).
const MaxSafeInt = 1 << 53

// StrictJSON checks a JSON document: valid UTF-8, exactly one value, no duplicate key in any object, every number
// within ±2^53 (integers) or finite and within that range (other numbers).
func StrictJSON(b []byte) error {
	if !utf8.Valid(b) {
		return errors.New("the output is not valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := strictValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("the output is more than one JSON value")
	}
	return nil
}

func strictValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("the output is nested too deeply")
	}
	tok, err := d.Token()
	if err != nil {
		return fmt.Errorf("the output is not valid JSON: %v", err)
	}
	switch x := tok.(type) {
	case json.Delim:
		switch x {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return fmt.Errorf("the output is not valid JSON: %v", err)
				}
				k, _ := kt.(string)
				if seen[k] {
					return fmt.Errorf("the output has the key %q twice in one object", clipName(k))
				}
				seen[k] = true
				if err := strictValue(d, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := strictValue(d, depth+1); err != nil {
					return err
				}
			}
		}
		if _, err := d.Token(); err != nil {
			return fmt.Errorf("the output is not valid JSON: %v", err)
		}
	case json.Number:
		return safeNumber(x)
	}
	return nil
}

func safeNumber(n json.Number) error {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		i, ok := new(big.Int).SetString(s, 10)
		if !ok || i.CmpAbs(big.NewInt(MaxSafeInt)) > 0 {
			return fmt.Errorf("the number %s is outside ±2^53", clipName(s))
		}
		return nil
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) > MaxSafeInt {
		return fmt.Errorf("the number %s is outside ±2^53", clipName(s))
	}
	return nil
}

// DecodePlan reads a stored plan's classification and steps with exact numbers (json.Number).
func DecodePlan(stored []byte) (classification, plan any, err error) {
	var raw struct {
		Classification any `json:"classification"`
		Plan           any `json:"plan"`
	}
	d := json.NewDecoder(bytes.NewReader(stored))
	d.UseNumber()
	if err := d.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("the stored plan is not readable: %v", err)
	}
	return raw.Classification, raw.Plan, nil
}

// StoredPlanSHA256 is the plan hash of a stored plan (PlanSHA256 of DecodePlan); "" when it is not readable. The
// worker, the api and actd all use it.
func StoredPlanSHA256(stored string) string {
	if stored == "" {
		return ""
	}
	c, p, err := DecodePlan([]byte(stored))
	if err != nil {
		return ""
	}
	s, err := PlanSHA256(c, p)
	if err != nil {
		return ""
	}
	return s
}
