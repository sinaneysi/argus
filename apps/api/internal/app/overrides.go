package app

// Accepted failures ("Mark as OK"), Go side of bin/overrides.py (Go only, ADR-0005). The app reads the overrides
// from ops.db (read-only) and writes add/undo requests to settings-in/backup-overrides/, which bin/overrides.py
// (argus) validates and stores. The rules (OverrideValid) are the same as overrides.evaluate(), checked by the
// shared vectors in internal/app/testdata/override_vectors.json. The real status is never changed: the page shows it,
// labelled "accepted".

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const (
	OVERRIDE_MAX_DAYS  = 30 // "for N days"
	OVERRIDE_OPEN_DAYS = 30 // "until the next successful run" / "until changed" also end after this
)

// Override is a stored acceptance.
type Override struct {
	ID                            int64
	Key, Check, Reason, By, Scope string
	At, Expires, Days             int64
	Snapshot                      string
	Ended                         bool
}

// OverrideValid is overrides.evaluate(): (valid, why) for the current status and fingerprint.
func OverrideValid(o Override, status, fp string, okSince bool, now int64) (bool, string) {
	switch {
	case o.Ended:
		return false, "ended"
	case now >= o.Expires:
		return false, "expired"
	case status == "OK":
		return false, "passing"
	case o.Scope == "until-ok" && okSince:
		return false, "passed since"
	case status != o.Check:
		return false, "new kind of failure"
	case o.Scope == "until-changed" && fp != o.Snapshot:
		return false, "changed"
	}
	return true, "accepted"
}

// Fingerprint is overrides.fingerprint(newest, size).
func Fingerprint(newest any, size any) string {
	sz := ""
	switch v := size.(type) {
	case int64:
		sz = strconv.FormatInt(v, 10)
	case float64:
		sz = strconv.FormatInt(int64(v), 10)
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			sz = strconv.FormatInt(n, 10)
		}
	}
	return py.OrS(newest, "") + "|" + sz
}

func overrideOf(r Row) Override {
	return Override{ID: r.I("id"), Key: r.Str("set_name"), Check: r.Str("check_kind"), Reason: r.Str("reason"), By: r.Str("by_user"),
		Scope: r.Str("scope"), At: r.I("at"), Expires: r.I("expires"), Days: r.I("days"), Snapshot: r.S("snapshot"),
		Ended: !r.IsNull("ended_at")}
}

// BackupAccepted maps the failing rows (latest backup-check) that a valid override covers to it.
func BackupAccepted(rows []Row) map[string]Override {
	bad := map[string]Row{}
	for _, r := range rows {
		if r.Str("status") != "OK" {
			bad[r.Str("name")] = r
		}
	}
	out := map[string]Override{}
	if len(bad) == 0 {
		return out
	}
	now := NowInt()
	for _, x := range Q("SELECT * FROM backup_overrides WHERE ended_at IS NULL AND expires > ? ORDER BY id", now) {
		o := overrideOf(x)
		r, ok := bad[o.Key]
		if !ok {
			continue
		}
		okSince := len(Q(`SELECT 1 FROM backup_results b JOIN runs r ON r.id=b.run_id WHERE r.task='backup-check'
		                  AND b.name=? AND b.status='OK' AND r.started_at > ? LIMIT 1`, o.Key, o.At)) > 0
		if v, _ := OverrideValid(o, r.Str("status"), Fingerprint(r.Get("newest"), r.Get("size")), okSince, now); v {
			out[o.Key] = o
		}
	}
	return out
}

// LatestBackupRows are the rows of the newest backup-check run.
func LatestBackupRows() []Row {
	return Q(`SELECT * FROM backup_results WHERE run_id = (SELECT id FROM runs WHERE task='backup-check' ORDER BY started_at DESC LIMIT 1)`)
}

func scopeText(o Override) string {
	switch o.Scope {
	case "days":
		return "until " + Strftime("2006-01-02 15:04", float64(o.Expires))
	case "until-changed":
		return "until the newest backup changes (latest " + Strftime("2006-01-02", float64(o.Expires)) + ")"
	}
	return "until it passes again (latest " + Strftime("2006-01-02", float64(o.Expires)) + ")"
}

var (
	reOvSet   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+:@-]{0,199}$`)
	reOvCheck = regexp.MustCompile(`^[A-Z][A-Za-z0-9_()-]{0,31}$`)
)

// BackupOverridePost is POST /backups/override (admins; the MFA middleware already required a recent code).
func BackupOverridePost(c *Ctx, form py.QS, sess *Session, ip string) (int, string) {
	get := func(k string) string { return py.Strip(form.Get(k)) }
	if sess.Role != "admin" {
		return 403, "Only admins can mark a backup as OK."
	}
	back := get("back")
	if !strings.HasPrefix(back, "/backups") {
		back = "/backups"
	}
	back = SafeNext(back)
	req := py.NewOMap("action", get("action"), "by", sess.Username, "role", sess.Role, "mfa", true, "at", NowInt())
	switch get("action") {
	case "undo":
		id, err := strconv.ParseInt(get("id"), 10, 64)
		if err != nil || id <= 0 || id >= 1e9 {
			return 400, "Invalid request."
		}
		req.Set("id", id)
	case "add":
		name, check, scope := get("set"), get("check"), get("scope")
		if !reOvSet.MatchString(name) || strings.Contains(name, "..") || !reOvCheck.MatchString(check) || check == "OK" ||
			!In(scope, "until-ok", "days", "until-changed") {
			return 400, "Invalid request."
		}
		reason := strings.Join(py.Split(get("reason")), " ")
		if n := len([]rune(reason)); n < 10 || n > 500 {
			return 400, "The reason needs at least 10 characters."
		}
		req.Set("set", name).Set("check", check).Set("scope", scope).Set("reason", reason).Set("rid", auth.TokenHex(6))
		if scope == "days" {
			d, err := strconv.ParseInt(get("days"), 10, 64)
			if err != nil || d < 1 || d > OVERRIDE_MAX_DAYS {
				return 400, "Days must be 1 to " + Itoa(OVERRIDE_MAX_DAYS) + "."
			}
			req.Set("days", d)
		}
	default:
		return 400, "Invalid request."
	}
	d := filepath.Join(SET_IN, "backup-overrides")
	if err := os.MkdirAll(d, 0o755); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	name := time.Unix(NowInt(), 0).UTC().Format("20060102150405") + "-" + auth.TokenHex(4) + ".json"
	if err := WriteFileAtomic(filepath.Join(d, name), filepath.Join(d, "."+name+".tmp"), []byte(py.Dumps(req, -1))); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	target := get("set")
	if get("action") == "undo" {
		target = "override-" + get("id")
	}
	secEvent(sess, "backup.override."+get("action"), target, get("check")+" "+get("scope"), ip, py.OrS(req.Get("reason"), ""))
	logf("backup override %s requested by %s", get("action"), sess.Username)
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	return 303, back + sep + "sent=override"
}
