// Package access builds the commands a person runs to reach a resource through an access broker (Teleport or HashiCorp
// Boundary). Argus only shows them: the person signs in to the broker, and the broker decides what they may reach.
// Argus never grants access (docs/architecture/adr/0019-access-brokers.md).
package access

import (
	"regexp"
	"strings"
)

// Config is the install's broker. Broker is "teleport", "boundary" or "" for none.
type Config struct {
	Broker        string
	TeleportProxy string
	SSHUser       string
	BoundaryAddr  string
	BoundaryScope string
}

// Command is one step a person copies into a terminal.
type Command struct {
	Label string `json:"label"`
	Cmd   string `json:"cmd"`
}

// nameRe keeps every value safe to paste into a shell: names come from collected data, not from the person.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func ok(vals ...string) bool {
	for _, v := range vals {
		if !nameRe.MatchString(v) {
			return false
		}
	}
	return true
}

// Label is the broker's name for the page, "" when there is none.
func (c Config) Label() string {
	return map[string]string{"teleport": "Teleport", "boundary": "Boundary"}[strings.ToLower(c.Broker)]
}

func (c Config) signIn() []Command {
	if strings.ToLower(c.Broker) == "teleport" {
		return []Command{{"Sign in", "tsh login --proxy=" + c.TeleportProxy}}
	}
	return []Command{{"Sign in", "export BOUNDARY_ADDR=" + c.BoundaryAddr + "\nboundary authenticate"}}
}

func (c Config) ready() bool {
	switch strings.ToLower(c.Broker) {
	case "teleport":
		return ok(c.TeleportProxy, c.SSHUser)
	case "boundary":
		return strings.HasPrefix(c.BoundaryAddr, "https://") && ok(strings.TrimPrefix(c.BoundaryAddr, "https://"), c.BoundaryScope)
	}
	return false
}

// SSH is the way to open a shell on a VM, nil when no broker is set or the name is not safe to show.
func (c Config) SSH(vm string) []Command {
	if !c.ready() || !ok(vm) {
		return nil
	}
	if strings.ToLower(c.Broker) == "teleport" {
		return append(c.signIn(), Command{"Open a shell", "tsh ssh " + c.SSHUser + "@" + vm})
	}
	return append(c.signIn(), Command{"Open a shell", "boundary connect ssh -target-name " + vm + " -target-scope-name " + c.BoundaryScope})
}

// Kube is the way to use a Kubernetes cluster, nil when no broker is set or the name is not safe to show.
func (c Config) Kube(cluster string) []Command {
	if !c.ready() || !ok(cluster) {
		return nil
	}
	if strings.ToLower(c.Broker) == "teleport" {
		return append(c.signIn(), Command{"Use the cluster", "tsh kube login " + cluster + "\nkubectl get pods -A"})
	}
	return append(c.signIn(), Command{"Use the cluster", "boundary connect kube -target-name " + cluster + " -target-scope-name " + c.BoundaryScope + " -- get pods -A"})
}
