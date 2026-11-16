package actd

import (
	"encoding/json"
	"os"
	"strconv"

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
	// SealKey is the fingerprint of actd's sealing public key (actions/actd.pub); compare with the api's pin
	SealKey string `json:"seal_key"`
	// GitLabProjects is the resolved project allow-list (id -> path); empty until it is resolved
	GitLabProjects map[string]string `json:"gitlab_projects,omitempty"`
	// BackupsKept is the number of sealed old values kept (an applied, not rolled-back write keeps its backup)
	BackupsKept int `json:"backups_kept"`
	// Sig is actd's Ed25519 signature (keys/status.key) over the status without it; the api shows the status as
	// verified only when it checks against the pinned key (argus approval-key trust-actd-status)
	Sig string `json:"sig,omitempty"`
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
	s := Status{BackupsKept: d.backupsKept, TS: d.now().Unix(), Version: d.o.Version, Mode: string(d.o.Mode), Capabilities: d.o.Registry.Names(), Last: d.last}
	if s.Capabilities == nil {
		s.Capabilities = []string{}
	}
	if d.o.SealKey != nil {
		s.SealKey = capability.SealFingerprint(d.o.SealKey.PublicKey())
	}
	if d.o.Registry != nil && d.o.Registry.GitLab != nil {
		if p := d.o.Registry.GitLab.Projects(); len(p) > 0 {
			s.GitLabProjects = map[string]string{}
			for id, path := range p {
				s.GitLabProjects[strconv.FormatInt(id, 10)] = path
			}
		}
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
	if d.o.Credentials != nil { // names and times only: the status is readable by the other services
		for _, c := range d.o.Credentials() {
			c.Last4 = ""
			s.Credentials = append(s.Credentials, c)
		}
	}
	if s.Credentials == nil {
		s.Credentials = []Credential{}
	}
	return s
}

// WriteStatus writes the heartbeat (0644: names and states only).
func (d *Daemon) WriteStatus() error {
	s := d.CurrentStatus()
	if d.o.StatusKey != nil {
		s.Sig = capability.SignStatus(d.o.StatusKey, statusBody(s))
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(StatusPath(d.o.ActionsDir), b, 0o644)
}
