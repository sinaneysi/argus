package app

// Containers in the vulnerability scan: which image runs where (ops.db cve_containers, written by
// bin/cve_inventory.py from cAdvisor labels, the stage docker list and config containers_declared), what the root
// image analyser read from it, and what the scan cannot see (cve_blind_spots). Read-only; Go-only (ADR-0005).

import (
	"strings"
)

var cveBlindLabel = map[string]string{
	"no-evidence":           "No package list, no container list",
	"db-rule":               "Database host skipped by rule",
	"cadvisor-no-labels":    "cAdvisor without image labels",
	"cadvisor-unmapped":     "cAdvisor instance without a VM",
	"image-not-analysed":    "Image could not be analysed",
	"declared":              "Declared, not observed",
	"image-drift":           "Running image older than analysed",
	"declared-unknown-host": "Declared host unknown",
	"web-no-header":         "Web fingerprint without a version",
	"web-no-backend":        "Web fingerprint without a backend",
	"source-error":          "Source failed",
	"feed-failed":           "Feed query failed",
	"feed-unparsable":       "Feed cache file unparsable",
}

var cveBlindOrder = []string{"no-evidence", "cadvisor-no-labels", "image-not-analysed", "image-drift", "declared",
	"cadvisor-unmapped", "db-rule", "declared-unknown-host", "web-no-header", "web-no-backend", "source-error",
	"feed-failed", "feed-unparsable"}

func cveSourceText(src string) string {
	switch {
	case src == "declared":
		return "declared in config"
	case src == "ssh-ro docker":
		return defStageDockerLabel
	case strings.HasPrefix(src, "cadvisor:"):
		return "cAdvisor (" + strings.TrimPrefix(src, "cadvisor:") + ")"
	}
	return src
}

func cveAnalysisText(a string) string {
	switch a {
	case "ok":
		return `<span class="ok">analysed</span>`
	case "failed":
		return `<span class="failnum">not analysed</span>`
	}
	return `<span class="dim">pending</span>`
}

// cveVMContainers is the VM card's "Containers on this VM": each container, its image and whether it was analysed.
func cveVMContainerRows(vid int64) []Row {
	return Q("SELECT c.name, c.image, c.source, c.analysis, c.evidence, (SELECT count(DISTINCT f.id) FROM cve_affects a "+
		"JOIN cve_findings f ON f.group_key=a.group_key AND f.state='open' WHERE a.vm_id=c.vm_id AND "+
		"a.unit='container:'||c.name) n FROM cve_containers c WHERE c.vm_id=? ORDER BY c.name", vid)
}
