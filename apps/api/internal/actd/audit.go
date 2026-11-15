package actd

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/secret"
)

// Record is one audit line (ADR-0022 D7): one per phase (plan, apply, verify, rollback, refused). The members
// prev_sha256 and sha256 keep the names of D7; their values are HMAC-SHA-256 tags (ADR-0022 amendment 1).
type Record struct {
	TS          string          `json:"ts"`
	ActionID    string          `json:"action_id"`
	Step        int             `json:"step"`
	TaskKey     string          `json:"task_key"`
	Approver    string          `json:"approver"`
	PlanVersion int             `json:"plan_version"`
	PlanSHA256  string          `json:"plan_sha256"`
	Capability  string          `json:"capability"`
	Args        capability.Args `json:"args"`
	Phase       string          `json:"phase"`
	Result      string          `json:"result"`
	Detail      string          `json:"detail"`
	BackupRef   string          `json:"backup_ref"`
	Prev        string          `json:"prev_sha256"`
	SHA256      string          `json:"sha256,omitempty"`
}

// The phases of an audit record.
const (
	PhasePlan     = "plan"
	PhaseApply    = "apply"
	PhaseVerify   = "verify"
	PhaseRollback = "rollback"
	PhaseRefused  = "refused"
	PhaseStop     = "stop"    // an admin's signed stop: approvals of the task signed before it are revoked
	PhaseChecked  = "checked" // an admin's signed verdict on a step whose outcome was unknown
)

// DetailMax is the longest detail kept, after the secret scan.
const DetailMax = 300

// Clean is the detail as it is stored: secret-scanned, one line, at most DetailMax characters.
func Clean(s string) string {
	s, _ = notify.Redact(s, nil)
	s = string(bytes.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, []byte(s)))
	if utf8.RuneCountInString(s) > DetailMax {
		s = string([]rune(s)[:DetailMax])
	}
	return s
}

// AuditKey is the chain's HMAC key, derived from the executor key (keys/act.key): only actd can extend the chain.
func AuditKey(k secret.Key) ([]byte, error) {
	if len(k) != 32 {
		return nil, errors.New("no executor key")
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte("argus-actd-audit-v1"))
	return m.Sum(nil), nil
}

func tag(key, b []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

// Audit is the authoritative chain argus-act/audit/actions.jsonl: append only, fsync after each line. Each line's
// sha256 member is the HMAC of the line without that member; prev_sha256 is the previous line's tag ("" for the
// first). The head, the record count and the file size are also kept in audit/anchor.json (HMAC-tagged, inside
// argus-act), so a removed last line, an emptied file or a lost anchor is a broken chain too.
type Audit struct {
	mu      sync.Mutex
	path    string
	anchor  string
	key     []byte
	head    string
	n       int
	size    int64
	lastOff int64    // where the last line starts
	lastSum [32]byte // sha256 of the last line's bytes
	err     error    // the chain is broken: nothing more is appended
}

var tailRe = regexp.MustCompile(`,"sha256":"([0-9a-f]{64})"}$`)

type chainState struct {
	head          string
	n             int
	size, lastOff int64
	lastSum       [32]byte
}

func verifyFile(path string, key []byte) (chainState, error) {
	var st chainState
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var off int64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			return st, fmt.Errorf("read: %v", err)
		}
		if line[len(line)-1] != '\n' {
			return st, fmt.Errorf("line %d is not complete", st.n+1)
		}
		st.n++
		body := line[:len(line)-1]
		m := tailRe.FindSubmatchIndex(body)
		if m == nil {
			return st, fmt.Errorf("line %d has no tag", st.n)
		}
		plain := append(append([]byte{}, body[:m[0]]...), '}')
		got := tag(key, plain)
		if !hmac.Equal([]byte(got), body[m[2]:m[3]]) {
			return st, fmt.Errorf("line %d was changed (its tag does not match)", st.n)
		}
		var rec Record
		if err := json.Unmarshal(plain, &rec); err != nil {
			return st, fmt.Errorf("line %d is not a record", st.n)
		}
		if rec.Prev != st.head {
			return st, fmt.Errorf("line %d does not follow line %d (a line was removed, added or moved)", st.n, st.n-1)
		}
		st.head, st.lastOff, st.lastSum = got, off, sha256.Sum256(line)
		off += int64(len(line))
		st.size = off
	}
	return st, nil
}

type anchorDoc struct {
	Head    string `json:"head"`
	Records int    `json:"records"`
	Size    int64  `json:"size"`
	Tag     string `json:"tag"`
}

func anchorTag(key []byte, a anchorDoc) string {
	return tag(key, []byte("anchor|"+a.Head+"|"+strconv.Itoa(a.Records)+"|"+strconv.FormatInt(a.Size, 10)))
}

func readAnchor(path string, key []byte) (anchorDoc, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return anchorDoc{}, false, nil
	}
	if err != nil {
		return anchorDoc{}, false, fmt.Errorf("the anchor cannot be read: %v", err)
	}
	var a anchorDoc
	if json.Unmarshal(b, &a) != nil || !hmac.Equal([]byte(a.Tag), []byte(anchorTag(key, a))) {
		return anchorDoc{}, true, errors.New("the anchor is damaged or was not written by actd")
	}
	return a, true, nil
}

// compare checks a verified chain against its anchor.
func compare(st chainState, a anchorDoc, have bool, aerr error) error {
	switch {
	case aerr != nil:
		return aerr
	case !have && st.n > 0:
		return errors.New("the anchor is missing while the chain holds records")
	case !have:
		return nil
	case a.Records != st.n || a.Head != st.head || a.Size != st.size:
		return fmt.Errorf("the chain does not match its anchor (%d records, the anchor says %d)", st.n, a.Records)
	}
	return nil
}

// AuditPaths are the chain and its anchor in the executor volume.
func AuditPaths(actDir string) (chain, anchor string) {
	return filepath.Join(actDir, "audit", "actions.jsonl"), filepath.Join(actDir, "audit", "anchor.json")
}

// AuditPath is the chain file.
func AuditPath(actDir string) string { p, _ := AuditPaths(actDir); return p }

// VerifyDir checks the chain and its anchor (argus actd verify-audit).
func VerifyDir(actDir string, k secret.Key) (head string, n int, err error) {
	key, err := AuditKey(k)
	if err != nil {
		return "", 0, err
	}
	chain, anchor := AuditPaths(actDir)
	st, err := verifyFile(chain, key)
	if err != nil {
		return st.head, st.n, err
	}
	a, have, aerr := readAnchor(anchor, key)
	return st.head, st.n, compare(st, a, have, aerr)
}

// OpenAudit opens the chain in actDir/audit and verifies it fully. A broken chain is returned as the error and kept
// in the Audit: Check keeps failing and Append refuses, so actd refuses every step until a person looks.
func OpenAudit(actDir string, k secret.Key) (*Audit, error) {
	key, err := AuditKey(k)
	if err != nil {
		return nil, err
	}
	chain, anchor := AuditPaths(actDir)
	if err := os.MkdirAll(filepath.Dir(chain), 0o700); err != nil {
		return nil, err
	}
	a := &Audit{path: chain, anchor: anchor, key: key}
	a.err = a.full()
	return a, a.err
}

func (a *Audit) full() error {
	st, err := verifyFile(a.path, a.key)
	if err == nil {
		an, have, aerr := readAnchor(a.anchor, a.key)
		err = compare(st, an, have, aerr)
	}
	a.head, a.n, a.size, a.lastOff, a.lastSum = st.head, st.n, st.size, st.lastOff, st.lastSum
	return err
}

// Head is the chain head and the number of records.
func (a *Audit) Head() (string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.head, a.n
}

// Err is the chain's state: nil when it verified.
func (a *Audit) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

// Full verifies the whole file and the anchor again (at start and every hour).
func (a *Audit) Full() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	head, n := a.head, a.n
	if err := a.full(); err != nil {
		a.err = err
	} else if a.head != head || a.n != n {
		a.err = errors.New("the chain changed outside actd")
	}
	return a.err
}

// Check is the cheap check before every step: the file has exactly the size this process left, its last line is
// unchanged, and the anchor still matches. Only actd appends, so any other size is a change from outside. The
// whole chain is verified at start and every hour (Full).
func (a *Audit) Check() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	fi, err := os.Stat(a.path)
	switch {
	case errors.Is(err, os.ErrNotExist) && a.size == 0:
	case err != nil:
		a.err = fmt.Errorf("the chain cannot be read: %v", err)
	case fi.Size() != a.size:
		a.err = fmt.Errorf("the chain changed outside actd (%d bytes, expected %d)", fi.Size(), a.size)
	case a.n > 0:
		f, err := os.Open(a.path)
		if err != nil {
			a.err = err
			break
		}
		buf := make([]byte, a.size-a.lastOff)
		_, err = f.ReadAt(buf, a.lastOff)
		f.Close()
		if err != nil || sha256.Sum256(buf) != a.lastSum {
			a.err = errors.New("the last line of the chain was changed")
		}
	}
	if a.err == nil {
		an, have, aerr := readAnchor(a.anchor, a.key)
		a.err = compare(chainState{head: a.head, n: a.n, size: a.size}, an, have, aerr)
	}
	return a.err
}

// Append adds one record (it fills ts, prev_sha256 and sha256), syncs the file and moves the anchor. It refuses on
// a broken chain; a failed write breaks the chain.
func (a *Audit) Append(r Record, now time.Time) (Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return r, a.err
	}
	r.TS = now.UTC().Format(time.RFC3339Nano)
	r.Prev = a.head
	r.SHA256 = ""
	r.Detail = Clean(r.Detail)
	if r.Args == nil {
		r.Args = capability.Args{}
	}
	body, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	r.SHA256 = tag(a.key, body)
	line := append(body[:len(body)-1], []byte(`,"sha256":"`+r.SHA256+`"}`+"\n")...)
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		a.err = fmt.Errorf("the chain cannot be opened: %v", err)
		return r, a.err
	}
	_, werr := f.Write(line)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		a.err = fmt.Errorf("append failed: %v", werr)
		return r, a.err
	}
	a.head, a.n, a.lastOff, a.lastSum = r.SHA256, a.n+1, a.size, sha256.Sum256(line)
	a.size += int64(len(line))
	an := anchorDoc{Head: a.head, Records: a.n, Size: a.size}
	an.Tag = anchorTag(a.key, an)
	b, _ := json.Marshal(an)
	if err := writeFile(a.anchor, b, 0o600); err != nil {
		a.err = fmt.Errorf("the anchor was not written: %v", err)
		return r, a.err
	}
	return r, nil
}
