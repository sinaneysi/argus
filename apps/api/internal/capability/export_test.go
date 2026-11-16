package capability

import (
	"net/http"
	"time"
)

// GitLabClientForTest is the GitLab family's guarded client, for the guard test.
func GitLabClientForTest(g *GitLab) *http.Client { return g.client }

// KeycloakClientForTest is keycloak.read's guarded client, for the guard test.
func KeycloakClientForTest(k *Keycloak) *http.Client { return k.client }

// SetKeycloakClock replaces keycloak.read's clock (the token cache test).
func SetKeycloakClock(k *Keycloak, now func() time.Time) { k.now = now }
