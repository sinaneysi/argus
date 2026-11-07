package app

// kube_page.go: the Settings -> Kubernetes page (GET /admin/kube). Its one question: "Can Argus's agent read our
// clusters, and only read?" A status block answers it (kubectl installed, clusters, how many the agent may use), then
// one panel per cluster (its verdict, what the check found, Test again / Replace / Remove), the add-or-replace form
// (folded once a cluster exists) and how to make a read-only ServiceAccount. Reuses the Settings -> Claude sheet
// (claude.css: .cl-now, .cl-sec, .cl-form ...) plus a few .kube-* rules there (DESIGN.md "Settings → Kubernetes").

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

type kubeCluster struct {
	Name                                        string
	Env, Context, User, Server, Namespace, Auth string
	Verdict, Why, SetBy, TestResult, TestMsg    string
	ServerVersion, NsErr                        string
	Enabled, Override, Insecure                 bool
	Findings                                    []string
	SetAt, TestAt, Namespaces                   int64
	HasNs                                       bool
}

type kubeFacts struct {
	Known                 bool // root has published a status at least once
	Kubectl               bool
	KubectlVer, KubectlEr string
	KubectlBad            bool // installed but its sha256 is not the pinned one
	Clusters              []kubeCluster
}

func kubeFactsNow() kubeFacts {
	st := KubeStatus()
	f := kubeFacts{Known: st.Len() > 0}
	if k := py.AsMap(st.Get("kubectl")); k != nil {
		f.Kubectl, f.KubectlVer, f.KubectlEr = py.Truthy(k.Get("installed")), k.S("version"), k.S("error")
		if v, ok := k.Get("verified").(bool); ok && !v && f.Kubectl {
			f.KubectlBad = true
		}
	}
	cl := py.AsMap(st.Get("clusters"))
	if cl == nil {
		return f
	}
	names := append([]string(nil), cl.Keys()...)
	sort.Strings(names)
	for _, n := range names {
		e := py.AsMap(cl.Get(n))
		if e == nil || !kubeNameRe.MatchString(n) {
			continue
		}
		c := kubeCluster{Name: n, Env: e.S("env"), Context: e.S("context"), User: e.S("user"), Server: e.S("server_host"),
			Namespace: e.S("namespace"), Auth: e.S("auth"), Verdict: py.OrS(e.S("verdict"), "unknown"), Why: e.S("why"),
			SetBy: e.S("set_by"), SetAt: py.I(e.Get("set_at")), Enabled: e.Get("enabled") == true,
			Override: e.Get("override") == true, Insecure: e.Get("insecure") == true}
		for _, x := range e.List("findings") {
			if s, ok := x.(string); ok && len(c.Findings) < 8 {
				c.Findings = append(c.Findings, s)
			}
		}
		if t := py.AsMap(e.Get("test")); t != nil {
			c.TestResult, c.TestMsg, c.TestAt = t.S("result"), t.S("message"), py.I(t.Get("at"))
			c.ServerVersion, c.NsErr = t.S("server_version"), t.S("namespaces_error")
			if t.Get("namespaces") != nil {
				c.HasNs, c.Namespaces = true, py.I(t.Get("namespaces"))
			}
		}
		f.Clusters = append(f.Clusters, c)
	}
	return f
}

// kubeVerdictOf is the status block: one state, one sentence, at most one next action.
func kubeVerdictOf(f kubeFacts) claudeVerdict {
	if !f.Known {
		return claudeVerdict{"unknown", "Not checked", "Root has not reported on Kubernetes yet.",
			"The status appears within seconds of the next deploy or the first saved cluster.", "", ""}
	}
	if !f.Kubectl {
		d := "Root installs the pinned kubectl " + E(py.OrS(f.KubectlVer, "")) + " at deploy; until then every check and every agent call is refused."
		if f.KubectlEr != "" {
			d = "The last install failed: <q>" + E(FirstLine(SafeErr(f.KubectlEr), 160)) + "</q>. " + d
		}
		return claudeVerdict{"fail", "Not ready", "The agent cannot read Kubernetes: kubectl is not installed on " + E(ARGUS_HOSTNAME) + ".", d, "", ""}
	}
	if f.KubectlBad {
		return claudeVerdict{"fail", "Not ready", "kubectl on " + E(ARGUS_HOSTNAME) + " is not the pinned build.",
			"Root refuses every save and check until it reinstalls the pinned kubectl (at the next save or deploy).", "", ""}
	}
	if len(f.Clusters) == 0 {
		return claudeVerdict{"unknown", "None yet", "No cluster added yet.",
			"Add a read-only kubeconfig and the agent can read pods, logs and events there.", "Add a cluster", "#kube-add"}
	}
	var refused, over []string
	for _, c := range f.Clusters {
		if !c.Enabled {
			refused = append(refused, c.Name)
		} else if c.Override {
			over = append(over, c.Name)
		}
	}
	n := len(f.Clusters)
	if len(refused) > 0 {
		head := E(strings.Join(refused, ", ")) + If(len(refused) == 1, " is", " are") + " refused for the agent."
		return claudeVerdict{"warn", "Check", head, "The check found write or Secret access, or could not reach the cluster. " +
			"The agent reads only clusters whose login is read-only.", "See " + refused[0], "#kube-c-" + refused[0]}
	}
	if len(over) > 0 {
		return claudeVerdict{"warn", "Override", "The agent can read " + I2S(int64(n)) + If(n == 1, " cluster", " clusters") +
			", but " + E(strings.Join(over, ", ")) + " can also write or read Secrets.",
			"An admin enabled it with the override. The agent's tool still only reads; replace it with a read-only login when you can.",
			"See " + over[0], "#kube-c-" + over[0]}
	}
	return claudeVerdict{"ok", "Working", "The agent can read " + I2S(int64(n)) + If(n == 1, " cluster", " clusters") + ", read-only.",
		"Every login was checked: it can get, list and watch, and nothing else.", "", ""}
}

// kubeFactList is the facts under the status block, in plain text.
func kubeFactList(f kubeFacts) []claudeFact {
	kc := claudeFact{"fail", "kubectl", "not installed", ""}
	switch {
	case !f.Known:
		kc = claudeFact{"", "kubectl", "not known yet", ""}
	case f.KubectlBad:
		kc = claudeFact{"fail", "kubectl", f.KubectlVer + ", not the pinned build", ""}
	case f.Kubectl:
		kc = claudeFact{"ok", "kubectl", py.OrS(f.KubectlVer, "installed"), ""}
	}
	on := 0
	var last int64
	for _, c := range f.Clusters {
		if c.Enabled {
			on++
		}
		if c.TestAt > last {
			last = c.TestAt
		}
	}
	n := len(f.Clusters)
	out := []claudeFact{kc, {"", "Clusters", I2S(int64(n)), ""}}
	if n > 0 {
		out = append(out, claudeFact{If(on == n, "ok", "warn"), "For the agent", I2S(int64(on)) + " of " + I2S(int64(n)), ""})
	}
	if last > 0 {
		out = append(out, claudeFact{"", "Last check", Ago(last), ""})
	}
	return out
}

// kubeBadge is a cluster's state in one word, matching its colour.
func kubeBadge(c kubeCluster) string {
	switch {
	case c.Enabled && !c.Override:
		return `<span class="badge ok">Read-only</span>`
	case c.Enabled:
		return `<span class="badge warn">Override</span>`
	case c.Verdict == "writes":
		return `<span class="badge fail">Can write</span>`
	case c.Verdict == "secrets":
		return `<span class="badge fail">Reads Secrets</span>`
	}
	return `<span class="badge warn">Not verified</span>`
}

func kubeMono(label, v string) string {
	if v == "" {
		return ""
	}
	return label + ` <span class="mono">` + E(v) + `</span>`
}

// kubeClusterLines is a cluster's facts as lines (HTML) and what the check found.
func kubeClusterLines(c kubeCluster) (line, agent, saved, test, why string, items []string) {
	var bits []string
	for _, b := range []string{kubeMono("server", c.Server), kubeMono("context", c.Context), kubeMono("user", c.User),
		kubeMono("namespace", c.Namespace)} {
		if b != "" {
			bits = append(bits, b)
		}
	}
	if len(bits) > 0 {
		line = strings.Join(bits, ` · `) + `.`
	}
	switch {
	case c.Enabled && !c.Override:
		agent = `<b>The agent can read it.</b> The check found read access only.`
	case c.Enabled:
		agent = `<b>The agent can read it, with an override.</b> This kubeconfig can write or read Secrets; the agent's tool still only reads.`
	case c.Verdict == "writes":
		agent = `<b>Refused for the agent: this kubeconfig can write.</b>` + If(c.Env == "release", ` Release never takes the override.`, "")
	case c.Verdict == "secrets":
		agent = `<b>Refused for the agent: this kubeconfig can read Secrets.</b>` + If(c.Env == "release", ` Release never takes the override.`, "")
	default:
		agent = `<b>Refused for the agent until a check passes.</b> The last check could not tell what this login may do.`
	}
	if c.SetAt > 0 {
		saved = ` Saved <time title="` + E(When(c.SetAt)) + `">` + E(Ago(c.SetAt)) + `</time>` + If(c.SetBy != "", ` by `+E(c.SetBy), "") + `.`
	}
	if c.TestAt > 0 {
		test = `Last check <time title="` + E(When(c.TestAt)) + `">` + E(Ago(c.TestAt)) + `</time>`
		if c.TestResult == "ok" {
			test += If(c.ServerVersion != "", `: Kubernetes `+E(c.ServerVersion), `: the cluster answered`)
			if c.HasNs {
				test += `, ` + I2S(c.Namespaces) + If(c.Namespaces == 1, " namespace", " namespaces") + ` visible`
			} else if c.NsErr != "" {
				test += `, namespaces not listable`
			}
		} else {
			test += `: failed`
		}
		test += `.`
	}
	if c.TestResult != "ok" && c.TestMsg != "" {
		why = `<p class="cl-why">kubectl said: <q>` + E(strings.TrimSuffix(FirstLine(SafeErr(c.TestMsg), 200), ".")) + `</q></p>`
	}
	items = []string{}
	if c.Verdict != "read-only" {
		items = append(items, c.Findings...)
	}
	if c.Insecure {
		items = append(items, "TLS verification is off for this server (insecure-skip-tls-verify)")
	}
	return
}

// kubeHelpYAML is the recommended read-only ServiceAccount: get/list/watch on what an RCA needs, never Secrets.
const kubeHelpYAML = `apiVersion: v1
kind: ServiceAccount
metadata: {name: argus-ro, namespace: kube-system}
---
apiVersion: v1
kind: Secret
type: kubernetes.io/service-account-token
metadata:
  name: argus-ro-token
  namespace: kube-system
  annotations: {kubernetes.io/service-account.name: argus-ro}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: argus-ro}
rules:
- apiGroups: [""]
  resources: [pods, pods/log, services, endpoints, namespaces, nodes, events,
              persistentvolumes, persistentvolumeclaims, configmaps,
              serviceaccounts, resourcequotas, limitranges, replicationcontrollers]
  verbs: [get, list, watch]
- apiGroups: [apps]
  resources: [deployments, replicasets, statefulsets, daemonsets]
  verbs: [get, list, watch]
- apiGroups: [batch]
  resources: [jobs, cronjobs]
  verbs: [get, list, watch]
- apiGroups: [networking.k8s.io]
  resources: [ingresses, ingressclasses, networkpolicies]
  verbs: [get, list, watch]
- apiGroups: [autoscaling, policy, storage.k8s.io, events.k8s.io, discovery.k8s.io]
  resources: [horizontalpodautoscalers, poddisruptionbudgets, storageclasses, events, endpointslices]
  verbs: [get, list, watch]
- apiGroups: [metrics.k8s.io]
  resources: [pods, nodes]
  verbs: [get, list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: argus-ro}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: argus-ro}
subjects:
- {kind: ServiceAccount, name: argus-ro, namespace: kube-system}`

const kubeHelpCmds = `kubectl apply -f argus-ro.yaml
kubectl -n kube-system get secret argus-ro-token -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
SA_TOKEN=$(kubectl -n kube-system get secret argus-ro-token -o jsonpath='{.data.token}' | base64 -d)
K=argus-ro.kubeconfig
kubectl config set-cluster dev --server=https://<api-server>:6443 --certificate-authority=ca.crt --embed-certs=true --kubeconfig=$K
kubectl config set-credentials argus-ro --token=$SA_TOKEN --kubeconfig=$K
kubectl config set-context argus-ro --cluster=dev --user=argus-ro --namespace=default --kubeconfig=$K
kubectl config use-context argus-ro --kubeconfig=$K
kubectl --kubeconfig=$K auth can-i --list      # get/list/watch only, no secrets`

// --- results of a request to root (?kr=<request id>) ---------------------------------------------------------------

var reKubeDone = regexp.MustCompile(`^(saved|tested) ([a-z0-9-]+): (\S+); (enabled|not enabled) \((.*)\)$`)

func kubeResult(c *Ctx, rid, user string) string {
	if !jiraRidRe.MatchString(rid) {
		return ""
	}
	if boardExists(filepath.Join(SET_IN, "kube", user+"-"+rid+".json")) {
		c.Refresh = 5
		return `<div class="cl-res">` + StateBox(StBusy, "Checking on "+ARGUS_HOSTNAME+"…",
			"Root is storing the kubeconfig and asking the cluster what it may do. You can leave this page; the result "+
				"shows here and the page refreshes itself.", `<a href="/admin/kube">Pause live updates</a>`) + `</div>`
	}
	r := py.AsMap(py.AsMap(KubeStatus().Get("results")).Get(rid))
	if r == nil || r.Get("user") != user {
		c.Refresh = 5
		return `<div class="cl-res">` + StateBox(StBusy, "Sent. Waiting for root to pick it up.",
			"The result shows here. The page refreshes itself.", `<a href="/admin/kube">Pause live updates</a>`) + `</div>`
	}
	kind, title, detail := kubeResultWords(r.S("result"))
	box := ""
	switch kind {
	case "ok":
		box = StateBox(StSynced, title, detail, "")
	case "fail":
		box = StateBox(StFailed, title, detail, "")
	default:
		box = `<div class="banner warn" role="status">` + Icon("alert") + `<span><b>` + E(title) + `</b> ` + detail + `</span></div>`
	}
	return `<div class="cl-res">` + box + `</div>`
}

// kubeResultWords maps root's result line (internal/kube handle) to a state, a plain title and the next step.
func kubeResultWords(res string) (kind, title, detail string) {
	if strings.HasPrefix(res, "refused: ") {
		return "fail", "Not done: " + strings.TrimSuffix(SafeErr(py.Slice(res, 9)), ".") + ".", "Nothing was changed."
	}
	if strings.HasPrefix(res, "removed ") {
		n := strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(res, "removed "), ";", 2)[0], ".")
		return "ok", n + " removed. The agent can no longer read it.", "Root kept a backup of the kubeconfig."
	}
	m := reKubeDone.FindStringSubmatch(res)
	if m == nil {
		return "warn", "Finished.", E(strings.TrimSuffix(SafeErr(res), ".")) + "."
	}
	verb := If(m[1] == "saved", "saved", "checked")
	name, verdict, enabled, why := m[2], m[3], m[4] == "enabled", m[5]
	what := map[string]string{"writes": "this kubeconfig can write", "secrets": "this kubeconfig can read Secrets"}[verdict]
	switch {
	case enabled && verdict == "read-only":
		return "ok", name + " " + verb + ". The agent can read it, read-only.", "The check found read access only."
	case enabled:
		return "warn", name + " " + verb + " and enabled with the override: " + what + ".",
			"The agent's tool still only reads. Replace it with a read-only login when you can."
	case verdict == "unknown":
		return "warn", name + " " + verb + ", but it could not be checked, so the agent cannot use it yet.",
			"See what kubectl said below, then check it again once the cluster answers."
	}
	d := "The agent cannot use it. Make a read-only ServiceAccount (below) and save that instead."
	if why == "release never takes the override" {
		d += " Release never takes the override."
	}
	return "fail", name + " " + verb + ", but refused for the agent: " + what + ".", d
}

// --- the page -----------------------------------------------------------------------------------------------------

// kubePageWith is the page after a refused POST: the field marked, the kubeconfig to paste again.
func kubeRefused(c *Ctx, field, msg string) string {
	c.Rej = Reject(kubeFID, field, msg, py.QS{})
	return msg
}
