package demo

import (
	"bytes"
	"database/sql"
	"fmt"
	imgpng "image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

// TestParityWithPython compares this generator with the output of the original Python generator (demo/generate.py
// in the git history). It runs only when ARGUS_DEMO_PARITY_DIR names a directory with the Python output in data/
// and auth/, written with ARGUS_DEMO_PARITY_NOW (epoch seconds) and the default seed, under TZ=UTC.
// demo/parity.sh does all of that.
func TestParityWithPython(t *testing.T) {
	ref := os.Getenv("ARGUS_DEMO_PARITY_DIR")
	if ref == "" {
		t.Skip("set ARGUS_DEMO_PARITY_DIR (see demo/parity.sh)")
	}
	now, err := strconv.ParseFloat(os.Getenv("ARGUS_DEMO_PARITY_NOW"), 64)
	if err != nil {
		t.Fatal("ARGUS_DEMO_PARITY_NOW:", err)
	}
	out := t.TempDir()
	if err := Generate(filepath.Join(out, "data"), filepath.Join(out, "auth"), now, DefaultSeed); err != nil {
		t.Fatal(err)
	}
	diffs := compareTrees(t, ref, out)
	for _, d := range diffs {
		t.Error(d)
	}
	if len(diffs) == 0 {
		t.Logf("parity: identical tables, rows and JSON documents")
	}
}

func listFiles(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func compareTrees(t *testing.T, a, b string) []string {
	var diffs []string
	fa, fb := listFiles(a), listFiles(b)
	if !reflect.DeepEqual(fa, fb) {
		diffs = append(diffs, fmt.Sprintf("file lists differ:\n python: %v\n go:     %v", fa, fb))
	}
	for _, rel := range fa {
		pa, pb := filepath.Join(a, rel), filepath.Join(b, rel)
		if _, err := os.Stat(pb); err != nil {
			continue
		}
		switch filepath.Ext(rel) {
		case ".db":
			diffs = append(diffs, compareDB(pa, pb, rel)...)
		case ".json":
			ja, _ := os.ReadFile(pa)
			jb, _ := os.ReadFile(pb)
			va, ea := py.ParseJSON(ja)
			vb, eb := py.ParseJSON(jb)
			if ea != nil || eb != nil {
				diffs = append(diffs, fmt.Sprintf("%s: parse %v %v", rel, ea, eb))
			} else if d := compareValue(va, vb, rel); d != "" {
				diffs = append(diffs, d)
			}
		case ".png":
			diffs = append(diffs, comparePNG(pa, pb, rel)...)
		default:
			ba, _ := os.ReadFile(pa)
			bb, _ := os.ReadFile(pb)
			if !bytes.Equal(ba, bb) {
				diffs = append(diffs, rel+": content differs")
			}
		}
	}
	return diffs
}

func comparePNG(a, b, rel string) []string {
	open := func(p string) ([]byte, int, int, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, 0, 0, err
		}
		defer f.Close()
		img, err := imgpng.Decode(f)
		if err != nil {
			return nil, 0, 0, err
		}
		r := img.Bounds()
		var px []byte
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				c1, c2, c3, _ := img.At(x, y).RGBA()
				px = append(px, byte(c1>>8), byte(c2>>8), byte(c3>>8))
			}
		}
		return px, r.Dx(), r.Dy(), nil
	}
	pa, wa, ha, ea := open(a)
	pb, wb, hb, eb := open(b)
	if ea != nil || eb != nil || wa != wb || ha != hb || !bytes.Equal(pa, pb) {
		return []string{fmt.Sprintf("%s: images differ (%v %v %dx%d %dx%d)", rel, ea, eb, wa, ha, wb, hb)}
	}
	return nil
}

// compareValue compares two parsed JSON values; an int and a float with the same value count as equal.
func compareValue(a, b any, where string) string {
	switch x := a.(type) {
	case *py.OMap:
		y, ok := b.(*py.OMap)
		if !ok {
			return fmt.Sprintf("%s: object vs %T", where, b)
		}
		if !reflect.DeepEqual(x.Keys(), y.Keys()) {
			return fmt.Sprintf("%s: keys differ\n python: %v\n go:     %v", where, x.Keys(), y.Keys())
		}
		for _, k := range x.Keys() {
			if d := compareValue(x.Get(k), y.Get(k), where+"."+k); d != "" {
				return d
			}
		}
		return ""
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return fmt.Sprintf("%s: list differs (%d vs %v)", where, len(x), b)
		}
		for i := range x {
			if d := compareValue(x[i], y[i], fmt.Sprintf("%s[%d]", where, i)); d != "" {
				return d
			}
		}
		return ""
	}
	if fa, ok := asNum(a); ok {
		if fb, ok := asNum(b); ok && (fa == fb || math.Abs(fa-fb) < 1e-9*math.Max(1, math.Abs(fa))) {
			return ""
		}
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Sprintf("%s: %#v vs %#v", where, a, b)
	}
	return ""
}

func asNum(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func compareDB(a, b, rel string) []string {
	var diffs []string
	da, err := sql.Open("sqlite", a)
	if err != nil {
		return []string{err.Error()}
	}
	defer da.Close()
	db, err := sql.Open("sqlite", b)
	if err != nil {
		return []string{err.Error()}
	}
	defer db.Close()
	schema := func(d *sql.DB) map[string]string {
		m := map[string]string{}
		rows, err := d.Query("SELECT name, sql FROM sqlite_master WHERE type IN ('table','index') AND sql IS NOT NULL AND name <> 'argus_schema_migrations'")
		if err != nil {
			return m
		}
		defer rows.Close()
		for rows.Next() {
			var n, s string
			rows.Scan(&n, &s)
			m[n] = s
		}
		return m
	}
	sa, sb := schema(da), schema(db)
	if !reflect.DeepEqual(sa, sb) {
		diffs = append(diffs, fmt.Sprintf("%s: schemas differ", rel))
	}
	var tables []string
	for n, s := range sa {
		if strings.HasPrefix(s, "CREATE TABLE") {
			tables = append(tables, n)
		}
	}
	sort.Strings(tables)
	for _, tb := range tables {
		ra, ca := dump(da, tb)
		rb, cb := dump(db, tb)
		if len(ra) != len(rb) {
			diffs = append(diffs, fmt.Sprintf("%s %s: %d rows vs %d", rel, tb, len(ra), len(rb)))
			continue
		}
		if !reflect.DeepEqual(ca, cb) {
			diffs = append(diffs, fmt.Sprintf("%s %s: columns differ", rel, tb))
			continue
		}
		n := 0
		for i := range ra {
			for j := range ra[i] {
				if !sameCell(ra[i][j], rb[i][j]) {
					if n < 3 {
						diffs = append(diffs, fmt.Sprintf("%s %s row %d %s: %#v vs %#v", rel, tb, i, ca[j], short(ra[i][j]), short(rb[i][j])))
					}
					n++
				}
			}
		}
		if n > 3 {
			diffs = append(diffs, fmt.Sprintf("%s %s: %d differing cells in all", rel, tb, n))
		}
	}
	return diffs
}

func short(v any) any {
	if s, ok := v.(string); ok && len(s) > 160 {
		return s[:160] + "..."
	}
	return v
}

func sameCell(a, b any) bool {
	if fa, ok := asNum(a); ok {
		if fb, ok := asNum(b); ok {
			return fa == fb
		}
	}
	// a JSON text stored in a column: compare it parsed (spacing and int/float writing may differ)
	if sa, ok := a.(string); ok {
		if sb, ok := b.(string); ok {
			if sa == sb {
				return true
			}
			if strings.HasPrefix(sa, "{") || strings.HasPrefix(sa, "[") {
				va, ea := py.ParseJSON([]byte(sa))
				vb, eb := py.ParseJSON([]byte(sb))
				return ea == nil && eb == nil && compareValue(va, vb, "") == ""
			}
			return false
		}
	}
	return reflect.DeepEqual(a, b)
}

// dump returns every row of a table with each cell's SQLite type and value, in rowid order (or primary key order).
func dump(d *sql.DB, table string) ([][]any, []string) {
	rows, err := d.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
	if err != nil {
		rows, err = d.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			return nil, nil
		}
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
	}
	return out, cols
}
