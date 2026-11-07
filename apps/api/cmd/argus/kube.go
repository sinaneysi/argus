package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/kube"
)

const kubeSecretsHelp = `
apply-kube       root, run by argus-kube.path (examples/systemd): processes the requests Settings -> Kubernetes writes
                 into ARGUS_SETTINGS_IN/kube/: validates each kubeconfig (inline data only: no exec or auth-provider
                 plugin, no file path, no impersonation), keeps the chosen context, checks it with kubectl as nobody
                 (auth can-i), backs up the previous one and stores ARGUS_KUBE_DIR/<name>.kubeconfig. Only a read-only
                 login is enabled for the agent (an admin may override outside release). Status: ARGUS_SETTINGS_OUT/
                 kube-status.json. The kubeconfig is never printed. Refused when DEMO_MODE=1.
install-kubectl  root: install or verify the pinned kubectl (ARGUS_KUBECTL_VERSION, _SHA256, _URL) at ARGUS_KUBECTL.
`

const toolsHelp = `usage: argus tools kube list
       argus tools kube <cluster> <command> [args]

kube  the agent's read-only Kubernetes tool (Settings -> Kubernetes): get, describe, logs, top, events, api-resources,
      version, can-i on a cluster whose kubeconfig root checked as read-only. Run "argus tools kube list" for the
      clusters and the full catalogue. Exit 2: refused before kubectl started.
`

func kubeOptions(c *config.Config) kube.Options {
	v := func(k string) string { return c.Values[k].Raw }
	return kube.Options{ReqDir: filepath.Join(c.SettingsIn, "kube"), OutDir: c.SettingsOut, AuthDB: c.AuthDB,
		KubeDir: v("ARGUS_KUBE_DIR"), Kubectl: v("ARGUS_KUBECTL"), Version: v("ARGUS_KUBECTL_VERSION"),
		SHA256: v("ARGUS_KUBECTL_SHA256"), URL: v("ARGUS_KUBECTL_URL"), AgentGroup: v("ARGUS_KUBE_AGENT_GROUP"), AppUID: 65534}
}

func runSecretsKube(sub string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("secrets "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintf(stderr, "argus secrets %s: disabled in the demo (DEMO_MODE=1): nothing was changed\n", sub)
		return 1
	}
	o := kubeOptions(c)
	o.Log = stdout
	if sub == "install-kubectl" {
		st := o.InstallKubectl()
		if err := o.Publish(nil, st); err != nil {
			fmt.Fprintln(stderr, "argus secrets install-kubectl: status not written:", err)
		}
		if e, _ := st["error"].(string); e != "" {
			fmt.Fprintln(stderr, "argus secrets install-kubectl:", e)
			return 1
		}
		fmt.Fprintf(stdout, "kubectl %s installed at %s, sha256 verified\n", o.Version, o.Kubectl)
		return 0
	}
	if err := kube.Apply(o); err != nil {
		fmt.Fprintln(stderr, "argus secrets apply-kube: status not written:", err)
		return 1
	}
	return 0
}

func runTools(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[:1]) {
		fmt.Fprint(stdout, toolsHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] != "kube" {
		fmt.Fprintf(stderr, "argus tools: unknown tool %q (kube)\n", args[0])
		return 2
	}
	c, ok := loadQuiet("", stderr)
	if !ok {
		return 1
	}
	v := func(k string) string { return c.Values[k].Raw }
	host, _ := os.Hostname()
	t := &kube.Tool{KubeDir: v("ARGUS_KUBE_DIR"), Kubectl: v("ARGUS_KUBECTL"), AuditLog: v("ARGUS_KUBE_AUDIT_LOG"),
		CacheDir: v("ARGUS_KUBE_CACHE_DIR"), OwnerUID: 0, Host: host}
	return t.Run(args[1:], stdout, stderr)
}
