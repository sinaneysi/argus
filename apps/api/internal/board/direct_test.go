package board

import (
	"os"
	"path/filepath"
	"testing"
)

// Jira given directly (Settings -> Data sources in the volume layout): no settings file and no token file needed.
func TestDirectJiraConfig(t *testing.T) {
	e := newEnv(t, true)
	b, _ := os.ReadFile(filepath.Join(e.d, "etc", "jira.env"))
	url := string(b)[len("JIRA_URL=") : len(b)-len("\nJIRA_PROJECT=OPS\n")]
	os.Remove(filepath.Join(e.d, "etc", "jira.env"))
	os.Remove(e.o.PATFile)
	if st := e.run(t); st["configured"] == true {
		t.Fatal("configured without any Jira settings")
	}
	e.o.Config = map[string]string{"JIRA_URL": url, "JIRA_PROJECT": "OPS"}
	e.o.Token = goodTok
	if st := e.run(t); st["configured"] != true || st["bot"] != "board-bot" {
		t.Fatalf("direct config: %v", st)
	}
}
