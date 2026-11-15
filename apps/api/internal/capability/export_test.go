package capability

import "net/http"

// GitLabClientForTest is the GitLab family's guarded client, for the guard test.
func GitLabClientForTest(g *GitLab) *http.Client { return g.client }
