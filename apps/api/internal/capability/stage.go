package capability

import (
	"encoding/json"
	"strings"
)

// DefaultDevelopLabels is ARGUS_ACT_DEVELOP_LABELS' default: the board label that marks a task of the stage
// (develop) environment.
const DefaultDevelopLabels = "env:stage"

// ParseDevelopLabels reads ARGUS_ACT_DEVELOP_LABELS (comma list, compared without case); empty: the default.
func ParseDevelopLabels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultDevelopLabels
	}
	var out []string
	for _, l := range strings.Split(raw, ",") {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// LabelsJSON reads a task's labels column (a JSON list of strings); nil when it is not one.
func LabelsJSON(s string) []string {
	var l []string
	if json.Unmarshal([]byte(s), &l) != nil {
		return nil
	}
	return l
}

// StageEvidence is the board's evidence that a task is of the develop environment (ADR-0022 PR 5 review): the
// model's classification alone is not enough for a write-tier capability. ok when the labels hold one of the
// develop labels and no other "env:" label (a disagreement, e.g. env:stage and env:production, is not ok); why says
// what is missing in words.
func StageEvidence(labels, develop []string) (bool, string) {
	if len(develop) == 0 {
		develop = ParseDevelopLabels("")
	}
	isDev := map[string]bool{}
	for _, d := range develop {
		isDev[strings.ToLower(d)] = true
	}
	found, other := "", ""
	for _, l := range labels {
		k := strings.ToLower(strings.TrimSpace(l))
		switch {
		case isDev[k]:
			found = l
		case strings.HasPrefix(k, "env:") && other == "":
			other = l
		}
	}
	switch {
	case other != "":
		return false, "the board label " + clipName(other) + " disagrees with the develop classification"
	case found == "":
		return false, "the task has no board label for the stage environment (" + strings.Join(develop, ", ") + ")"
	}
	return true, "board label " + clipName(found)
}
