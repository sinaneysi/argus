package app

// Port of the parts of app/backup_plan.py the web app uses: the reference data (load_ref), the cron schedules
// (parse_cron, Sched.next_runs), secretish, set_name, human_bytes and the constants. The Python module stays for
// root's bin/backup_plans.py, which validates every request again.

import (
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

// BPTZ is backup_plan.TZ: the site's zone as a fixed offset (no DST).
var BPTZ = func() *time.Location { // SiteTZ as a fixed offset (site.go; today +03:30)
	_, off := time.Now().In(SiteTZ).Zone()
	return time.FixedZone("", off)
}()

// BPEnvs is backup_plan.ENVS.
var BPEnvs = []string{"develop", "pre-release", "release"}

// BPEngineLabel is backup_plan.ENGINE_LABEL.
var BPEngineLabel = map[string]string{"mongodb": "MongoDB", "mariadb-mysql": "MariaDB / MySQL", "postgres": "PostgreSQL",
	"redis": "Redis / Valkey", "mssql": "SQL Server", "files": "Files / GitLab"}

// bpEngineLabelOr is ENGINE_LABEL.get(engine, def) with a JSON value as the key.
func bpEngineLabelOr(engine any, def string) string {
	if s, ok := engine.(string); ok {
		if l, ok := BPEngineLabel[s]; ok {
			return l
		}
	}
	return def
}

// BPSecretName is backup_plan.SECRET_NAME.
var BPSecretName = regexp.MustCompile(`(?i)pass|secret|token|key|credential|pwd|private|identity`)

var (
	bpSecretLits = []*regexp.Regexp{regexp.MustCompile(`AGE-SECRET-KEY-1`), regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`),
		regexp.MustCompile(`glpat-[0-9A-Za-z_-]{20}`), regexp.MustCompile(`(?i)MC_HOST_[\p{L}\p{N}_]+=`),
		regexp.MustCompile(`/ping/[0-9a-f]{8}-[0-9a-f]{4}-`)}
	bpSecretWord   = regexp.MustCompile(`(?i)^(?:password|passwd|pwd|secret|token)`)
	bpSecretAssign = regexp.MustCompile(`^[` + wsClass + `]*[=:][` + wsClass + `]*[^` + wsClass + `]{4}`)
	bpAWSKey       = regexp.MustCompile(`(?i)^(?:AKIA|ASIA)[0-9A-Z]{16}`)
)

// bpURICredential is the search r"://[^/\s:@]+:(?!\*\*\*@)[^@\s/]{3,}@" (Python \s is Unicode whitespace).
func bpURICredential(s string) bool {
	rs := []rune(s)
	n := len(rs)
	for i := 0; i+3 <= n; i++ {
		if rs[i] != ':' || rs[i+1] != '/' || rs[i+2] != '/' {
			continue
		}
		j := i + 3
		for j < n && rs[j] != '/' && rs[j] != ':' && rs[j] != '@' && !py.IsSpaceRune(rs[j]) {
			j++
		}
		if j == i+3 || j >= n || rs[j] != ':' {
			continue
		}
		k := j + 1
		if k+4 <= n && string(rs[k:k+4]) == "***@" {
			continue
		}
		m := k
		for m < n && rs[m] != '@' && rs[m] != '/' && !py.IsSpaceRune(rs[m]) {
			m++
		}
		if m-k >= 3 && m < n && rs[m] == '@' {
			return true
		}
	}
	return false
}

// bpBoundedSearch finds an anchored match of rx at a Unicode word boundary (Python's \b ... \b around it).
func bpBoundedSearch(s string, rx *regexp.Regexp, after func(rest string) bool) bool {
	prevWord := false
	for i := 0; i <= len(s); {
		if !prevWord {
			if loc := rx.FindStringIndex(s[i:]); loc != nil {
				end := i + loc[1]
				r, _ := utf8.DecodeRuneInString(s[end:])
				if end == len(s) || !isWordRune(r) {
					if after == nil || after(s[end:]) {
						return true
					}
				}
			}
		}
		if i == len(s) {
			break
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		prevWord = isWordRune(r)
		i += w
	}
	return false
}

// BPSecretish is backup_plan.secretish(text): does the text look like a secret?
func BPSecretish(text string) bool {
	for _, rx := range bpSecretLits {
		if rx.MatchString(text) {
			return true
		}
	}
	if bpURICredential(text) {
		return true
	}
	if bpBoundedSearch(text, bpSecretWord, func(rest string) bool { return bpSecretAssign.MatchString(rest) }) {
		return true
	}
	return bpBoundedSearch(text, bpAWSKey, nil)
}

// BPSetName is backup_plan.set_name(bucket, prefix).
func BPSetName(bucket, prefix string) string {
	b := strings.TrimPrefix(bucket, "backup-")
	return b + "_" + strings.ReplaceAll(prefix, "/", "_")
}

// BPHumanBytes is backup_plan.human_bytes(n).
func BPHumanBytes(v any) string {
	n := py.Num(py.Or(v, int64(0)))
	for _, unit := range []string{"B", "KB", "MB", "GB", "TB"} {
		if n < 1024 || unit == "TB" {
			if unit == "B" {
				return py.F(n, 0) + " " + unit
			}
			return py.F(n, 1) + " " + unit
		}
		n /= 1024
	}
	return py.F(n, 1) + " TB"
}

// --- reference data ---------------------------------------------------------------------------------------------

// BPRef is load_ref(): the directory, the discovery facts and the templates' catalog.
type BPRef struct {
	Dir       string
	Discovery *py.OMap
	Catalog   *py.OMap
}

// bpHere is the directory of the Python module (app/): the parent of the static directory.
func bpConfigDir() string {
	return filepath.Join(filepath.Dir(filepath.Dir(STATIC)), "config")
}

func bpIsFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// BackupRefDir is ARGUS_BACKUP_REF_DIR (old name OPS_BACKUP_REF), tried first by BPRefDir. Tests may change it.
var BackupRefDir = env("ARGUS_BACKUP_REF_DIR", "")

// BPRefDir is backup_plan.ref_dir().
func BPRefDir() string {
	for _, d := range []string{BackupRefDir, "/data/backup-plans/ref", bpConfigDir()} {
		if d != "" && bpIsFile(filepath.Join(d, "backup-discovery.json")) {
			return d
		}
	}
	return bpConfigDir()
}

// BPLoadRef is backup_plan.load_ref(d); d "" is None.
func BPLoadRef(d string) BPRef {
	if d == "" {
		d = BPRefDir()
	}
	disc, ok := py.ReadJSON(filepath.Join(d, "backup-discovery.json"), nil).(*py.OMap)
	if !ok {
		if py.ReadJSON(filepath.Join(d, "backup-discovery.json"), nil) == nil {
			disc = py.NewOMap("sets", []any{}, "buckets", []any{}, "families", []any{}, "defects", []any{}, "gaps", []any{},
				"schedules", []any{}, "minio", py.NewOMap())
		} else {
			disc = py.NewOMap() // a JSON value that is not an object: Python would fail on .get (500)
		}
	}
	cat, ok := py.ReadJSON(filepath.Join(d, "backup-templates", "catalog.json"), nil).(*py.OMap)
	if !ok {
		cat = py.NewOMap("engines", py.NewOMap(), "common_keys", []any{})
	}
	return BPRef{Dir: d, Discovery: disc, Catalog: cat}
}

// --- schedules ----------------------------------------------------------------------------------------------------

// BPSched is backup_plan.Sched: sets of minutes, hours, days of month, months, days of week (0=Mon).
type BPSched struct {
	Minutes, Hours, Doms, Months, Dows map[int]bool
	DomAny, DowAny                     bool
}

func bpSorted(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func bpWeekday(t time.Time) int { return (int(t.Weekday()) + 6) % 7 }

func (s *BPSched) dayOK(d time.Time) bool {
	domOK, dowOK := s.Doms[d.Day()], s.Dows[bpWeekday(d)]
	if !s.DomAny && !s.DowAny {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// NextRuns is Sched.next_runs(start, n, horizon_days=400).
func (s *BPSched) NextRuns(start time.Time, n int) []time.Time {
	var out []time.Time
	t := start.Add(time.Minute)
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location())
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	hours, mins := bpSorted(s.Hours), bpSorted(s.Minutes)
	for i := 0; i < 400; i++ {
		if s.Months[int(day.Month())] && s.dayOK(day) {
			for _, h := range hours {
				for _, m := range mins {
					c := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
					if !c.Before(t) {
						out = append(out, c)
						if len(out) >= n {
							return out
						}
					}
				}
			}
		}
		nd := day.AddDate(0, 0, 1)
		day = time.Date(nd.Year(), nd.Month(), nd.Day(), 0, 0, 0, 0, nd.Location())
	}
	return out
}

// bpAtoi is int(s) for a str that passed isdigit(); a number too big for int64 saturates (Python has big ints).
func bpAtoi(s string) (int64, bool) {
	var b strings.Builder
	for _, r := range s {
		d := bpDigitOf(r)
		if d < 0 {
			return 0, false
		}
		b.WriteByte(byte('0' + d))
	}
	if b.Len() == 0 {
		return 0, false
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	if !ok {
		return 0, false
	}
	if !n.IsInt64() {
		return math.MaxInt64, true
	}
	return n.Int64(), true
}

func bpDigitOf(r rune) int {
	if !py.IsDigit(string(r)) {
		return -1
	}
	n, ok := py.ParseInt(string(r))
	if !ok {
		return -1
	}
	return int(n)
}

type bpValueError struct{ msg string }

func (e *bpValueError) Error() string { return e.msg }

func bpErr(m string) error { return &bpValueError{m} }

func bpNum(x string, names map[string]int, what string) (int64, error) {
	if names != nil {
		k := py.Head(strings.ToLower(x), 3)
		if v, ok := names[k]; ok {
			return int64(v), nil
		}
	}
	if !py.IsDigit(x) {
		return 0, bpErr(what + ": '" + x + "' is not a number")
	}
	n, _ := bpAtoi(x)
	return n, nil
}

func bpField(text string, lo, hi int64, names map[string]int, what string) (map[int]bool, error) {
	vals := map[int]bool{}
	for _, part := range strings.Split(text, ",") {
		if part == "" {
			return nil, bpErr(what + ": empty item")
		}
		step := int64(1)
		if p, s, ok := strings.Cut(part, "/"); ok {
			part = p
			n, ok := bpAtoi(s)
			if !py.IsDigit(s) || !ok || n < 1 {
				return nil, bpErr(what + ": bad step '/" + s + "'")
			}
			step = n
		}
		var a, b int64
		var err error
		if part == "*" {
			a, b = lo, hi
		} else if x, y, ok := strings.Cut(part, "-"); ok {
			if a, err = bpNum(x, names, what); err != nil {
				return nil, err
			}
			if b, err = bpNum(y, names, what); err != nil {
				return nil, err
			}
		} else {
			if a, err = bpNum(part, names, what); err != nil {
				return nil, err
			}
			b = a
			if step > 1 {
				b = hi
			}
		}
		if !(lo <= a && a <= hi && lo <= b && b <= hi) || a > b {
			return nil, bpErr(what + ": " + part + " is outside " + strconv.FormatInt(lo, 10) + "-" + strconv.FormatInt(hi, 10))
		}
		for v := a; v <= b; v += step {
			vals[int(v)] = true
			if step > b-v {
				break
			}
		}
	}
	return vals, nil
}

var (
	bpDowNames = map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}
	bpMonNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9,
		"oct": 10, "nov": 11, "dec": 12}
	bpCronDowNames = func() map[string]int {
		m := map[string]int{}
		for k, v := range bpDowNames {
			m[k] = (v + 1) % 7
		}
		return m
	}()
)

// BPParseCron is backup_plan.parse_cron(expr) (allow_or=False).
func BPParseCron(expr string) (*BPSched, error) {
	f := py.Split(expr)
	if len(f) != 5 {
		return nil, bpErr("a cron expression has 5 fields: minute hour day-of-month month day-of-week")
	}
	minutes, err := bpField(f[0], 0, 59, nil, "minute")
	if err != nil {
		return nil, err
	}
	hours, err := bpField(f[1], 0, 23, nil, "hour")
	if err != nil {
		return nil, err
	}
	doms, err := bpField(f[2], 1, 31, nil, "day of month")
	if err != nil {
		return nil, err
	}
	months, err := bpField(f[3], 1, 12, bpMonNames, "month")
	if err != nil {
		return nil, err
	}
	cd, err := bpField(f[4], 0, 7, bpCronDowNames, "day of week")
	if err != nil {
		return nil, err
	}
	dows := map[int]bool{}
	for d := range cd {
		dows[((d%7)-1+7)%7] = true
	}
	domAny, dowAny := f[2] == "*", f[4] == "*"
	if !domAny && !dowAny {
		return nil, bpErr("day-of-month and day-of-week together mean OR in cron but AND in systemd: use only one of them")
	}
	return &BPSched{Minutes: minutes, Hours: hours, Doms: doms, Months: months, Dows: dows, DomAny: domAny, DowAny: dowAny}, nil
}
