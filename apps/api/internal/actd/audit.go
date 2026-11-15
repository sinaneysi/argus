package actd

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/notify"
)

// Record is one audit line (ADR-0022 D7): one per phase (plan, apply, verify, rollback, refused).
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

// Audit is the authoritative hash chain argus-act/audit/actions.jsonl: append only, fsync after each line. Each
// line's sha256 is the SHA-256 of the line without its sha256 member; prev_sha256 is the previous line's sha256
// ("" for the first line). An edited, removed or reordered line breaks the chain.
type Audit struct {
	mu   sync.Mutex
	path string
	head string
	n    int
	err  error // the chain is broken: nothing more is appended
}

var tailRe = regexp.MustCompile(`,"sha256":"([0-9a-f]{64})"}$`)

// VerifyFile checks the chain of an audit file: the head (last sha256) and the number of records. A missing file is
// an empty chain.
func VerifyFile(path string) (head string, n int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		n++
		m := tailRe.FindSubmatchIndex(line)
		if m == nil {
			return head, n, fmt.Errorf("line %d has no sha256", n)
		}
		body := append(append([]byte{}, line[:m[0]]...), '}')
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		if got != string(line[m[2]:m[3]]) {
			return head, n, fmt.Errorf("line %d was changed (its sha256 does not match)", n)
		}
		var r Record
		if err := json.Unmarshal(body, &r); err != nil {
			return head, n, fmt.Errorf("line %d is not a record", n)
		}
		if r.Prev != head {
			return head, n, fmt.Errorf("line %d does not follow line %d (a line was removed, added or moved)", n, n-1)
		}
		head = got
	}
	if err := sc.Err(); err != nil {
		return head, n, fmt.Errorf("read: %v", err)
	}
	return head, n, nil
}

// OpenAudit opens (creates) the audit file and verifies its chain. A broken chain is returned as the error and kept
// in the Audit: Check keeps failing and Append refuses, so actd refuses every step until a person looks.
func OpenAudit(path string) (*Audit, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	a := &Audit{path: path}
	a.head, a.n, a.err = VerifyFile(path)
	return a, a.err
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

// Expect compares the chain with a head recorded elsewhere (the last status file): fewer records than recorded,
// or another line at the recorded position, is a broken chain (the last lines were removed or replaced).
func (a *Audit) Expect(head string, n int) error {
	if n <= 0 || head == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	if a.n < n {
		a.err = fmt.Errorf("the chain has %d records, the last status recorded %d (lines were removed)", a.n, n)
		return a.err
	}
	if a.n == n && a.head != head {
		a.err = errors.New("the chain head is not the one the last status recorded")
	}
	if a.n > n && !a.contains(head, n) {
		a.err = errors.New("the chain does not hold the head the last status recorded")
	}
	return a.err
}

func (a *Audit) contains(head string, n int) bool {
	f, err := os.Open(a.path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for i := 1; sc.Scan(); i++ {
		if i == n {
			m := tailRe.FindSubmatch(sc.Bytes())
			return m != nil && string(m[1]) == head
		}
	}
	return false
}

// Check verifies the whole file again and compares it with what this process appended (a removed last line, or a
// line added by someone else, is a broken chain).
func (a *Audit) Check() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	head, n, err := VerifyFile(a.path)
	switch {
	case err != nil:
		a.err = err
	case n != a.n || head != a.head:
		a.err = fmt.Errorf("the chain changed outside actd (%d records, expected %d)", n, a.n)
	}
	return a.err
}

// Append adds one record (it fills ts, prev_sha256 and sha256) and syncs the file. It refuses on a broken chain.
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
	sum := sha256.Sum256(body)
	r.SHA256 = hex.EncodeToString(sum[:])
	line := append(body[:len(body)-1], []byte(`,"sha256":"`+r.SHA256+`"}`+"\n")...)
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return r, err
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		a.err = fmt.Errorf("append failed: %v", err)
		return r, a.err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		a.err = fmt.Errorf("fsync failed: %v", err)
		return r, a.err
	}
	if err := f.Close(); err != nil {
		return r, err
	}
	a.head, a.n = r.SHA256, a.n+1
	return r, nil
}
