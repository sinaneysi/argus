package actd

// backups.go: the sealed old values of write steps (ADR-0022 D2, D7): argus-act/backups/<action-id>/<step>.enc,
// sealed with the executor key, additional data = the reference and its metadata (project, key, scope). Kept
// ARGUS_ACTIONS_BACKUP_DAYS.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/secret"
)

// Vault is capability.Backups over the executor volume.
type Vault struct {
	Dir   string     // argus-act/backups
	Key   secret.Key // the executor key
	Steps string     // argus-act/steps: a backup that exists without a stored step result is not overwritten
}

func (v Vault) path(ref capability.BackupRef) (string, error) {
	id, n, err := capability.ParseBackupRef(ref)
	if err != nil {
		return "", err
	}
	return filepath.Join(v.Dir, id, strconv.Itoa(n)+".enc"), nil
}

func vaultName(ref capability.BackupRef, meta string) string {
	return "act-backup/" + string(ref) + "\n" + meta
}

// Put seals plain under ref with meta as additional data. An existing backup of the same step is kept (the first
// backup of a step holds the state before the action); when that step has no stored result, an earlier apply
// stopped in the middle, and Put refuses: a person checks the target first.
func (v Vault) Put(ref capability.BackupRef, meta string, plain []byte) error {
	if len(v.Key) != 32 {
		return errors.New("no executor key")
	}
	if strings.ContainsAny(meta, "\r\n") || len(meta) > 600 {
		return errors.New("bad backup metadata")
	}
	p, err := v.path(ref)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err == nil {
		id, n, _ := capability.ParseBackupRef(ref)
		if v.Steps != "" {
			if _, ok := (Store{Dir: v.Steps}).Get(id, n); ok {
				return nil
			}
		}
		return errors.New("a backup of this step exists without a stored result: an earlier apply was interrupted")
	}
	enc, err := v.Key.Seal(vaultName(ref, meta), base64.StdEncoding.EncodeToString(plain))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return writeFile(p, []byte(meta+"\n"+enc+"\n"), 0o600)
}

// Get opens the backup of ref.
func (v Vault) Get(ref capability.BackupRef) (string, []byte, error) {
	if len(v.Key) != 32 {
		return "", nil, errors.New("no executor key")
	}
	p, err := v.path(ref)
	if err != nil {
		return "", nil, err
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", nil, errors.New("no backup for this step")
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 2 || !secret.Sealed(lines[1]) {
		return "", nil, errors.New("the backup file is not in the sealed form")
	}
	b64, err := v.Key.Open(vaultName(ref, lines[0]), lines[1])
	if err != nil {
		return "", nil, errors.New("the backup does not open with the executor key")
	}
	plain, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", nil, errors.New("the backup is damaged")
	}
	return lines[0], bytes.Clone(plain), nil
}

// Prune removes the backups older than days whose step is over without a change left in place: rolled back, refused
// or failed. A backup of an applied write that was not rolled back is kept (it is the only way back). It returns
// the number removed and the number kept.
func (v Vault) Prune(days int, now time.Time) (removed, kept int, err error) {
	if days <= 0 {
		return 0, 0, fmt.Errorf("bad retention")
	}
	ents, err := os.ReadDir(v.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	st := Store{Dir: v.Steps}
	for _, e := range ents {
		if !e.IsDir() || !capability.ValidActionID(e.Name()) {
			continue
		}
		dir := filepath.Join(v.Dir, e.Name())
		files, _ := os.ReadDir(dir)
		left := 0
		for _, fe := range files {
			n, err := strconv.Atoi(strings.TrimSuffix(fe.Name(), ".enc"))
			fi, ierr := fe.Info()
			if err != nil || ierr != nil || !strings.HasSuffix(fe.Name(), ".enc") {
				left++
				continue
			}
			r, ok := st.Get(e.Name(), n)
			// over: nothing of the step is left in place. Not "failed-checked": a person's verdict may be wrong
			over := ok && (r.Status == "rolled-back" || r.Status == StateRefused || r.Status == "failed")
			if v.Steps == "" || !over || now.Sub(fi.ModTime()) < time.Duration(days)*24*time.Hour {
				left++
				kept++
				continue
			}
			if os.Remove(filepath.Join(dir, fe.Name())) == nil {
				removed++
			} else {
				left++
				kept++
			}
		}
		if left == 0 {
			os.Remove(dir)
		}
	}
	return removed, kept, nil
}
