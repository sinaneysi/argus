package actd

import (
	"encoding/json"
	"os"

	"github.com/sinaneysi/argus/internal/capability"
)

// Status is actions/actd-status.json in the shared volume (no secret): the heartbeat, the mode, the chain head
// (so the backup of the shared volume holds it too, D7), the capabilities and the stored credentials' names.
type Status struct {
	TS           int64        `json:"ts"`
	Version      string       `json:"version"`
	Mode         string       `json:"mode"`
	Chain        ChainStatus  `json:"chain"`
	Capabilities []string     `json:"capabilities"`
	Credentials  []Credential `json:"credentials"`
	Last         *lastAction  `json:"last,omitempty"`
	// ApprovalKey is the fingerprint of the api's approval public key; "" when none is stored (every step refused)
	ApprovalKey string `json:"approval_key"`
}

// ChainStatus is the audit chain's state.
type ChainStatus struct {
	OK      bool   `json:"ok"`
	Head    string `json:"head,omitempty"`
	Records int    `json:"records"`
	Error   string `json:"error,omitempty"`
}

// ReadStatus reads a status file.
func ReadStatus(path string) (Status, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Status{}, false
	}
	var s Status
	if json.Unmarshal(b, &s) != nil {
		return Status{}, false
	}
	return s, true
}

// CurrentStatus is the daemon's state now.
func (d *Daemon) CurrentStatus() Status {
	s := Status{TS: d.now().Unix(), Version: d.o.Version, Mode: string(d.o.Mode), Capabilities: d.o.Registry.Names(), Last: d.last}
	if s.Capabilities == nil {
		s.Capabilities = []string{}
	}
	if len(d.o.ApprovalPub) > 0 {
		s.ApprovalKey = capability.KeyFingerprint(d.o.ApprovalPub)
	}
	if d.audit == nil {
		s.Chain.Error = "the executor volume is not usable"
	} else {
		s.Chain.Head, s.Chain.Records = d.audit.Head()
		if err := d.audit.Err(); err != nil {
			s.Chain.Error = Clean(err.Error())
		} else {
			s.Chain.OK = true
		}
	}
	if d.o.Credentials != nil {
		s.Credentials = d.o.Credentials()
	}
	if s.Credentials == nil {
		s.Credentials = []Credential{}
	}
	return s
}

// WriteStatus writes the heartbeat (0644: names and states only).
func (d *Daemon) WriteStatus() error {
	b, err := json.MarshalIndent(d.CurrentStatus(), "", "  ")
	if err != nil {
		return err
	}
	return writeFile(StatusPath(d.o.ActionsDir), b, 0o644)
}
