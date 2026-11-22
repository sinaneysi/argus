package collect

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BackupSet is one entry of the backup sets file (ARGUS_BACKUP_SETS_FILE, JSON):
//
//	{"sets": [{"name": "pg-prod_orders", "path": "/srv/backups/pg/orders", "glob": "*.dump.age",
//	           "max_age_hours": 26, "min_bytes": 1048576, "min_ratio": 0.5}]}
//
// path is a file or a directory (searched recursively, symlinks not followed). The newest matching file decides
// the age and the size. The part of the name before the first "_" is the bucket filter on /backups.
//
// A set in object storage has "s3": "<source>/<bucket>/<prefix>" instead of path (source: a Settings -> Data
// sources entry of type S3) and "min_size": "auto" or a byte count; it is checked by the earlier MinIO check's rule
// (backups_s3.go). A sets file whose name ends in .tsv is the earlier check's TSV (name, path, max age, min size).
type BackupSet struct {
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	S3          string  `json:"s3,omitempty"`
	MinSize     string  `json:"min_size,omitempty"` // S3 sets: auto (default) or a byte count
	Glob        string  `json:"glob"`               // basename pattern (path.Match); default "*"
	MaxAgeHours int     `json:"max_age_hours"`      // older: STALE; default 26
	MinBytes    int64   `json:"min_bytes"`          // smaller: SMALL; default 1
	MinRatio    float64 `json:"min_ratio"`          // below this share of the median of the last 7 checks: SMALL; default 0.5
}

// BackupsOptions configure the backup check.
type BackupsOptions struct {
	SetsFile string              // ARGUS_BACKUP_SETS_FILE
	DB       string              // ARGUS_DB
	S3       map[string]S3Lister // the S3 data sources by name
	MaxFiles int                 // per set, a bound on the directory walk (default 100000)
	Now      func() time.Time
}

// LoadBackupSets reads and validates the sets file; every problem is returned at once.
func LoadBackupSets(path string) ([]BackupSet, error) {
	if strings.HasSuffix(path, ".tsv") {
		return LoadBackupSetsTSV(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Sets []BackupSet `json:"sets"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var errs []string
	seen := map[string]bool{}
	for i := range doc.Sets {
		s := &doc.Sets[i]
		switch {
		case s.Name == "" || strings.ContainsAny(s.Name, " \t/"):
			errs = append(errs, fmt.Sprintf("set %d: name must be set, without spaces or slashes", i+1))
		case seen[s.Name]:
			errs = append(errs, fmt.Sprintf("set %s: listed twice", s.Name))
		case s.S3 != "" && s.Path != "":
			errs = append(errs, fmt.Sprintf("set %s: path or s3, not both", s.Name))
		case s.S3 != "":
			if _, _, _, err := ParseS3Path(s.S3); err != nil {
				errs = append(errs, fmt.Sprintf("set %s: %v", s.Name, err))
			}
			if s.MinSize == "" {
				s.MinSize = "auto"
			} else if _, err := strconv.ParseInt(s.MinSize, 10, 64); err != nil && s.MinSize != "auto" {
				errs = append(errs, fmt.Sprintf("set %s: min_size must be auto or a byte count", s.Name))
			}
		case !filepath.IsAbs(s.Path):
			errs = append(errs, fmt.Sprintf("set %s: path must be absolute", s.Name))
		}
		seen[s.Name] = true
		if s.Glob == "" {
			s.Glob = "*"
		}
		if _, err := filepath.Match(s.Glob, "x"); err != nil {
			errs = append(errs, fmt.Sprintf("set %s: bad glob %q", s.Name, s.Glob))
		}
		if s.MaxAgeHours <= 0 {
			s.MaxAgeHours = 26
		}
		if s.MinBytes <= 0 {
			s.MinBytes = 1
		}
		if s.MinRatio <= 0 {
			s.MinRatio = 0.5
		}
	}
	if len(doc.Sets) == 0 {
		errs = append(errs, "no sets")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s: %s", path, strings.Join(errs, "; "))
	}
	return doc.Sets, nil
}

type backupFound struct {
	newest string
	mtime  time.Time
	size   int64
	kinds  int
	n      int
}

var errTooMany = errors.New("too many files")

// scanSet finds the newest file of a set. Read-only: stat and directory listings only.
func scanSet(s BackupSet, maxFiles int) (backupFound, error) {
	var f backupFound
	suffixes := map[string]bool{}
	visit := func(path string, info fs.FileInfo) {
		if ok, _ := filepath.Match(s.Glob, filepath.Base(path)); !ok {
			return
		}
		f.n++
		suffixes[suffixOf(filepath.Base(path))] = true
		if f.newest == "" || info.ModTime().After(f.mtime) {
			f.newest, f.mtime, f.size = path, info.ModTime(), info.Size()
		}
	}
	st, err := os.Lstat(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return f, nil // MISSING, not a collector failure
		}
		return f, err
	}
	if st.Mode().IsRegular() {
		visit(s.Path, st)
	} else if st.IsDir() {
		count := 0
		err = filepath.WalkDir(s.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if count++; count > maxFiles {
				return errTooMany
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			visit(path, info)
			return nil
		})
		if err != nil {
			return f, err
		}
	}
	f.kinds = len(suffixes)
	return f, nil
}

// suffixOf is the file's suffix chain: "x.dump.age" -> ".dump.age", "x" -> "".
func suffixOf(base string) string {
	if i := strings.Index(strings.TrimLeft(base, "."), "."); i >= 0 {
		return strings.TrimLeft(base, ".")[i:]
	}
	return ""
}

// median of the non-NULL sizes of a set in the last 7 backup-check runs (before this one); 0 when none.
func medianPrev7(ctx context.Context, db *sql.DB, name string) (int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT b.size FROM backup_results b JOIN runs r ON r.id=b.run_id
	  WHERE r.task='backup-check' AND b.name=? AND b.size IS NOT NULL ORDER BY r.started_at DESC, r.id DESC LIMIT 7`, name)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var v []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
		v = append(v, n)
	}
	if len(v) == 0 {
		return 0, rows.Err()
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	if len(v)%2 == 1 {
		return v[len(v)/2], nil
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2, nil
}

// Backups checks every set and records one "backup-check" run with one backup_results row per set.
func Backups(ctx context.Context, o BackupsOptions) (Result, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 100000
	}
	started := o.Now()
	sets, err := LoadBackupSets(o.SetsFile)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("backups: %w", err)
	}
	db, err := openDB(o.DB)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("backups: %w", err)
	}
	defer db.Close()

	type row struct {
		name, status string
		newest       any
		age, size    any
		median       int64
		s3med        any // S3 sets: the median of the earlier check (0 included)
		kinds        any
	}
	var rows []row
	var bad []string
	for _, s := range sets {
		if err := ctx.Err(); err != nil {
			return Result{Status: "ERROR"}, err
		}
		if s.S3 != "" {
			r := row{name: s.Name, status: "FAIL(empty)", newest: "-", kinds: int64(0)}
			src, bucket, prefix, _ := ParseS3Path(s.S3)
			if l := o.S3[src]; l == nil {
				Logger.Warn("set not checked: no S3 data source of this name", "collector", "backups", "set", s.Name, "source", src)
			} else if objs, err := l.List(ctx, bucket, prefix); err != nil {
				Logger.Warn("listing failed", "collector", "backups", "set", s.Name, "err", err)
			} else {
				for i := range objs {
					objs[i].Key = strings.TrimPrefix(objs[i].Key, prefix)
				}
				if x, ok := checkS3(objs, o.Now(), int64(s.MaxAgeHours), s.MinSize); ok {
					r = row{name: s.Name, status: x.status, newest: x.newest, age: x.age, size: x.size, s3med: x.med, kinds: x.kinds}
				}
			}
			if r.status != "OK" {
				bad = append(bad, s.Name)
			}
			Logger.Info("set checked", "collector", "backups", "set", s.Name, "status", r.status)
			rows = append(rows, r)
			continue
		}
		f, err := scanSet(s, o.MaxFiles)
		if err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("backups: set %s: %w", s.Name, err)
		}
		med, err := medianPrev7(ctx, db, s.Name)
		if err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("backups: %w", err)
		}
		r := row{name: s.Name, median: med, kinds: f.kinds}
		if f.newest == "" {
			r.status = "MISSING"
		} else {
			ageH := int64(math.Floor(o.Now().Sub(f.mtime).Hours()))
			if ageH < 0 {
				ageH = 0
			}
			r.newest, r.age, r.size = f.newest, ageH, f.size
			switch {
			case ageH > int64(s.MaxAgeHours):
				r.status = "STALE"
			case f.size < s.MinBytes || (med > 0 && float64(f.size) < s.MinRatio*float64(med)):
				r.status = "SMALL"
			default:
				r.status = "OK"
			}
		}
		if r.status != "OK" {
			bad = append(bad, s.Name)
		}
		Logger.Info("set checked", "collector", "backups", "set", s.Name, "status", r.status, "files", f.n)
		rows = append(rows, r)
	}
	status, summary := "OK", fmt.Sprintf("OK: all %d sets fresh and within size.", len(sets))
	if len(bad) > 0 {
		status, summary = "WARN", fmt.Sprintf("WARN: %d of %d sets need a look: %s.", len(bad), len(sets), strings.Join(bad, ", "))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("backups: %w", err)
	}
	defer tx.Rollback()
	rid, err := insertRun(ctx, tx, "backup-check", started, status, summary)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("backups: %w", err)
	}
	for _, r := range rows {
		var med any
		if r.median > 0 {
			med = r.median
		}
		if r.s3med != nil {
			med = r.s3med
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_results (run_id, name, status, newest, age_h, size, median_prev7, kinds)
		  VALUES (?,?,?,?,?,?,?,?)`, rid, r.name, r.status, r.newest, r.age, r.size, med, r.kinds); err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("backups: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("backups: %w", err)
	}
	return Result{Status: status, Summary: summary, Items: len(sets)}, nil
}
