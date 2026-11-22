package actd

// seal.go: actd's sealing key pair (ADR-0022 D2, D4). The X25519 private key is keys/act-x25519.key in the executor
// volume (0600, only actd mounts it); the public half is published to actions/actd.pub in the shared volume, where
// the api pins it (argus approval-key trust-actd). The api seals each secret input of an approval to it; actd opens
// the inputs a step refers to just before the step runs and keeps them in memory only.

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/sinaneysi/argus/internal/capability"
)

// SealKeyPath is the private sealing key in the executor volume.
func SealKeyPath(actDir string) string { return filepath.Join(actDir, "keys", "act-x25519.key") }

// SealPubPath is the published public half in the shared volume.
func SealPubPath(actionsDir string) string { return filepath.Join(actionsDir, "actd.pub") }

// LoadSealKey reads keys/act-x25519.key; with create, a missing key is made (0600).
func LoadSealKey(actDir string, create bool) (*ecdh.PrivateKey, error) {
	p := SealKeyPath(actDir)
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) && create {
		k, gerr := ecdh.X25519().GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		b, perr := capability.SealPrivatePEM(k)
		if perr != nil {
			return nil, perr
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := writeFile(p, b, 0o600); err != nil {
			return nil, err
		}
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return nil, errors.New("the sealing key is not a small regular file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the sealing key %s has mode %04o, must be 0600", p, fi.Mode().Perm())
	}
	b := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, err
	}
	return capability.ParseSealPrivate(b)
}

// PublishSealPub writes the public half to actions/actd.pub (0644: a public key).
func PublishSealPub(actionsDir string, k *ecdh.PrivateKey) error {
	b, err := capability.SealPublicPEM(k.PublicKey())
	if err != nil {
		return err
	}
	return writeFile(SealPubPath(actionsDir), b, 0o644)
}

// inputs is the request's sealed inputs checked against the steps: every input:<NAME> a capability step refers to
// has exactly one sealed value for that step, and there is no other. It opens nothing.
func (d *Daemon) checkInputs(t *task, sealed map[string]string) *capability.Refusal {
	if len(sealed) > capability.MaxSealedInputs {
		return capability.Refuse(4, capability.CodeArguments, "more than %d sealed inputs", capability.MaxSealedInputs)
	}
	want := map[string]bool{}
	for _, s := range t.steps {
		cp, ok := d.o.Registry.Get(s.Capability)
		if !ok {
			continue
		}
		for _, n := range cp.Schema().InputNames(s.Args) {
			want[capability.InputKey(s.N, n)] = true
		}
	}
	var missing, extra []string
	for k := range want {
		if _, ok := sealed[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k, v := range sealed {
		if _, _, ok := capability.ParseInputKey(k); !ok || !want[k] || !capability.ValidSealed(v) {
			extra = append(extra, clip(k, 40))
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	switch {
	case len(missing) > 0:
		return capability.Refuse(3, capability.CodeApproval, "the approval has no sealed input for %s: approve again and type the value", strings.Join(missing, ", "))
	case len(extra) > 0:
		return capability.Refuse(3, capability.CodeApproval, "the approval carries sealed inputs no step uses, or not in the sealed form: %s", strings.Join(extra, ", "))
	case len(want) > 0 && d.o.SealKey == nil:
		return capability.Refuse(3, capability.CodeApproval, "actd has no sealing key (keys/act-x25519.key): the sealed inputs cannot be opened")
	}
	return nil
}

// openInputs opens the sealed inputs of one step (in memory only). An input that does not open for this action,
// step and name refuses the step.
func (d *Daemon) openInputs(id, planSHA string, cp capability.Capability, s planStep, sealed map[string]string) (map[string]string, *capability.Refusal) {
	out := map[string]string{}
	for _, n := range cp.Schema().InputNames(s.Args) {
		v, err := capability.OpenInput(d.o.SealKey, id, planSHA, s.N, n, sealed[capability.InputKey(s.N, n)])
		if err != nil {
			return nil, capability.Refuse(3, capability.CodeApproval, "step %d: the sealed input %s: %s", s.N, n, err.Error())
		}
		out[n] = v
	}
	return out, nil
}
