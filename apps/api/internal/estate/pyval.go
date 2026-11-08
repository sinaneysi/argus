package estate

// pyval.go: small helpers that keep the values of the earlier job's documents typed the same way (int vs float,
// None vs "") so that the files and rows the pages read stay the same.

import (
	"math"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// num is float(v) for a number or a number text; ok false for anything else, NaN and infinity.
func num(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int64:
		f = float64(x)
	case int:
		f = float64(x)
	case string:
		s := strings.TrimSpace(x)
		var err error
		f, err = strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// numOr0 is int(num(v) or 0).
func numOr0(v any) int64 {
	f, _ := num(v)
	return int64(f)
}

// fnum is num(v) as a Python value: a float64, or nil.
func fnum(v any) any {
	if f, ok := num(v); ok {
		return f
	}
	return nil
}

// truthy is Python's bool(v) for the value types used here.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case *py.OMap:
		return x != nil && x.Len() > 0
	}
	return true
}

// or is Python's `a or b`.
func or(a, b any) any {
	if truthy(a) {
		return a
	}
	return b
}

// str is Python's str(v) for the value types used here.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return py.FloatRepr(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return py.Dumps(v, -1)
}

// round is Python's round(v, n) for a float.
func round(v float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', n, 64), 64)
	return f
}

// cut keeps the first n characters.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func fmtF(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }
