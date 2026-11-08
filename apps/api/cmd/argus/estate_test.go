package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The estate sites: ARGUS_PROM_SOURCES first, then Grafana entries enabled for the agent, then Prometheus entries of
// type Alertmanager / Prometheus; an Alertmanager and a Grafana that is not enabled are not sites.
func TestEstateSites(t *testing.T) {
	vol := t.TempDir()
	c, _ := volumeConfig(t, vol, "ARGUS_PROM_SOURCES=direct=https://prom.example.com\n")
	if ok, why := estateConfigured(c); !ok || why != "" {
		t.Fatalf("ARGUS_PROM_SOURCES alone: %v %q", ok, why)
	}
	reg := filepath.Join(vol, "etc", "sources", "registry.json")
	os.MkdirAll(filepath.Dir(reg), 0o755)
	os.WriteFile(reg, []byte(`{"sources":{
	 "grafana/east":{"type":"grafana","name":"east","url":"https://grafana-east.example.com","purpose":"agent","enabled":true},
	 "grafana/west":{"type":"grafana","name":"west","url":"https://grafana-west.example.com","purpose":"agent","enabled":false},
	 "alertmanager/am":{"type":"alertmanager","name":"am","url":"https://am.example.com","purpose":"alerts"},
	 "alertmanager/prom":{"type":"alertmanager","name":"prom","url":"https://prom2.example.com","purpose":"alerts","facts":{"api":"prometheus"}},
	 "alertmanager/direct":{"type":"alertmanager","name":"direct","url":"https://other.example.com","purpose":"alerts","facts":{"api":"prometheus"}}}}`), 0o644)
	sites, errs := estateSites(c)
	var got []string
	for _, s := range sites {
		got = append(got, s.Name+"="+s.URL+map[bool]string{true: " grafana", false: ""}[s.Grafana])
	}
	want := "direct=https://prom.example.com,east=https://grafana-east.example.com grafana,prom=https://prom2.example.com"
	if strings.Join(got, ",") != want || len(errs) > 0 {
		t.Fatalf("sites %v errs %v", got, errs)
	}
	c2, _ := volumeConfig(t, t.TempDir(), "ARGUS_PROM_SOURCES=Bad Name=https://x.example.com,ok=http://plain.example.com\n")
	if _, errs := estateSites(c2); len(errs) != 2 {
		t.Fatalf("want 2 problems, got %v", errs)
	}
	if ok, why := estateConfigured(c2); ok || !strings.Contains(why, "not configured") {
		t.Fatalf("no valid site: %v %q", ok, why)
	}
}
