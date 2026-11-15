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
	Dir string     // argus-act/backups
	Key secret.Key // the executor key
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

// Put seals plain under ref with meta as additional data; an existing backup of the same step is kept (the first
// backup of a step holds the state before the action).
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
		return nil
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

// Prune removes the backups of actions whose folder is older than days.
func (v Vault) Prune(days int, now time.Time) (int, error) {
	if days <= 0 {
		return 0, fmt.Errorf("bad retention")
	}
	ents, err := os.ReadDir(v.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() || !capability.ValidActionID(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil || now.Sub(fi.ModTime()) < time.Duration(days)*24*time.Hour {
			continue
		}
		if os.RemoveAll(filepath.Join(v.Dir, e.Name())) == nil {
			n++
		}
	}
	return n, nil
}
