package collect

// backups_s3.go: backup sets in S3-compatible object storage (the earlier MinIO backup check, scripts/backup-check.sh).
// A set is "<source>/<bucket>/<prefix>": source is a Settings -> Data sources entry of type S3. One set can hold
// several kinds of backup (a config dump and a full dump, say): the kind of an object is its key with every digit
// removed, and each kind is checked against its own history:
//
//   - max age: the larger of the set's max age and 3 x the kind's median interval (intervals under 30 minutes are
//     ignored); a kind with nothing newer than max(10 x that, 30 days) is retired and skipped;
//   - min size: a byte count, or "auto" = half the median size of the 7 objects before the newest.
//
// The set's row is its worst live kind: FAIL(stale), FAIL(small), else OK; nothing live (or a listing that failed)
// is FAIL(empty). The statuses and the row values are the earlier check's.

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var digitRe = regexp.MustCompile(`[0-9]`)

// S3Lister lists the objects under a prefix of a bucket.
type S3Lister interface {
	List(ctx context.Context, bucket, prefix string) ([]S3Object, error)
}

// s3Row is one set's result.
type s3Row struct {
	status, newest string
	age, size, med any
	kinds          int64
}

func upperMedian(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// checkS3 is the earlier check's per-set rule on one listing. objects' keys are relative to the set's prefix.
func checkS3(objs []S3Object, now time.Time, maxAge int64, minSize string) (s3Row, bool) {
	type obj struct {
		k    string
		s, t int64
	}
	groups := map[string][]obj{}
	for _, o := range objs {
		kind := digitRe.ReplaceAllString(o.Key, "")
		groups[kind] = append(groups[kind], obj{o.Key, o.Size, o.LastModified.Unix()})
	}
	kinds := make([]string, 0, len(groups))
	for k := range groups {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	type res struct {
		st, k  string
		age, s int64
		med    float64
	}
	var live []res
	for _, kind := range kinds {
		g := groups[kind]
		sort.SliceStable(g, func(i, j int) bool { return g[i].t < g[j].t })
		last := g[len(g)-1]
		age := int64(math.Floor(float64(now.Unix()-last.t) / 3600))
		var iv []float64
		for i := 1; i < len(g); i++ {
			if d := float64(g[i].t-g[i-1].t) / 3600; d > 0.5 {
				iv = append(iv, d)
			}
		}
		kmax := math.Max(float64(maxAge), math.Ceil(upperMedian(iv)*3))
		retire := math.Max(kmax*10, 720)
		lo := len(g) - 8
		if lo < 0 {
			lo = 0
		}
		var sizes []float64
		for _, o := range g[lo : len(g)-1] {
			sizes = append(sizes, float64(o.s))
		}
		med := upperMedian(sizes)
		var floor float64
		if minSize == "auto" {
			floor = math.Floor(med / 2)
		} else {
			floor, _ = strconv.ParseFloat(minSize, 64)
		}
		st := "OK"
		switch {
		case float64(age) > retire:
			st = "RETIRED"
		case float64(age) > kmax:
			st = "FAIL(stale)"
		case float64(last.s) < floor:
			st = "FAIL(small)"
		}
		if st != "RETIRED" {
			live = append(live, res{st, last.k, age, last.s, med})
		}
	}
	if len(live) == 0 {
		return s3Row{}, false
	}
	w := live[0]
	for _, r := range live {
		if r.st != "OK" {
			w = r
			break
		}
	}
	return s3Row{status: w.st, newest: w.k, age: w.age, size: w.s, med: int64(w.med), kinds: int64(len(live))}, true
}

// ParseS3Path splits "<source>/<bucket>/<prefix>".
func ParseS3Path(p string) (source, bucket, prefix string, err error) {
	parts := strings.SplitN(p, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" || strings.Contains(p, "..") {
		return "", "", "", fmt.Errorf("want <source>/<bucket>/<prefix>, got %q", p)
	}
	if len(parts) == 3 {
		prefix = parts[2]
	}
	return parts[0], parts[1], prefix, nil
}

// LoadBackupSetsTSV reads the earlier check's sets file: name, <source>/<bucket>/<prefix>, max age in hours, min size
// (auto or bytes), tab-separated; # comments and empty lines are skipped.
func LoadBackupSetsTSV(path string) ([]BackupSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []BackupSet
	var errs []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		c := strings.Split(line, "\t")
		if len(c) < 4 {
			errs = append(errs, fmt.Sprintf("line %d: want name, path, max age, min size", n))
			continue
		}
		age, err := strconv.Atoi(strings.TrimSpace(c[2]))
		ms := strings.TrimSpace(c[3])
		if _, perr := strconv.ParseInt(ms, 10, 64); err != nil || age <= 0 || (ms != "auto" && perr != nil) {
			errs = append(errs, fmt.Sprintf("line %d: bad max age or min size", n))
			continue
		}
		if _, _, _, err := ParseS3Path(strings.TrimSpace(c[1])); err != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", n, err))
			continue
		}
		name := strings.TrimSpace(c[0])
		if name == "" || seen[name] {
			errs = append(errs, fmt.Sprintf("line %d: name empty or listed twice", n))
			continue
		}
		seen[name] = true
		out = append(out, BackupSet{Name: name, S3: strings.TrimSpace(c[1]), MaxAgeHours: age, MinSize: ms})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 && len(errs) == 0 {
		errs = append(errs, "no sets")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s: %s", path, strings.Join(errs, "; "))
	}
	return out, nil
}
