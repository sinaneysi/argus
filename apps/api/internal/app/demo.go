package app

// Demo mode (DEMO_MODE=1, default off): a public click-through of Argus on synthetic data (demo/).
//   - a sticky banner on every page;
//   - the login page offers "Sign in as viewer / as admin (demo)" (DEMO_VIEWER, DEMO_ADMIN, DEMO_PASSWORD);
//   - every POST except log in, log out and the theme switch is answered with a "disabled in the demo" page
//     before any handler runs, so nothing is written (no request files, no settings, no password change);
//   - no outbound network: single sign-on is never configured (main.go), Keycloak is hidden.
// Off, nothing here changes a byte of any page.

var (
	DEMO          = env("DEMO_MODE", "") == "1"
	DEMO_VIEWER   = env("DEMO_VIEWER", "demo.viewer")
	DEMO_ADMIN    = env("DEMO_ADMIN", "demo.admin")
	DEMO_PASSWORD = env("DEMO_PASSWORD", "")
	DEMO_BANNER   = env("DEMO_BANNER", "Demo — sample data, not a real company")
)

// DemoBlocksPost: every write is refused in demo mode; sign-in, sign-out and the theme (apiAuthPOSTs) never reach it.
func DemoBlocksPost(string) bool { return DEMO }
