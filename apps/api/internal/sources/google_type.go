package sources

// google_type.go: the data-source type google (ADR-0021 D6, the marketing module): a Google service-account key for
// Search Console and, optionally, GA4. The credential is the key file's JSON (sealed with the master key like every
// credential); the address is https://www.googleapis.com, or a test service. The check signs in with the read-only
// Search Console scope and lists the properties the key may read; it never writes.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/marketing/google"
)

func checkGoogle(base, cred string, _ *Entry) (Check, error) {
	k, err := google.ParseKey(cred)
	if err != nil {
		return result("failed", err.Error()+"; paste the key file's JSON", "", nil, nil), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl := &google.Client{Key: k, Base: base, AllowHTTP: AllowHTTP || testAllowLocal, Wait: []time.Duration{2 * time.Second}}
	sites, err := cl.Sites(ctx)
	var he *google.HTTPError
	switch {
	case err != nil && strings.HasPrefix(err.Error(), "Google refused the key"):
		return result("failed", err.Error()+"; replace the key", k.Email, nil, nil), nil
	case errors.As(err, &he) && (he.Code == 401 || he.Code == 403):
		return loginFailed(he.Code), nil
	case err != nil:
		return result("unreachable", cut(err.Error(), 160), k.Email, nil, nil), nil
	}
	if len(sites) == 0 {
		return result("failed", "the key reads no Search Console property: add its e-mail as a Restricted user of the property", k.Email, nil, nil), nil
	}
	return result("ok", fmt.Sprintf("reads %d Search Console propert%s", len(sites), map[bool]string{true: "y", false: "ies"}[len(sites) == 1]),
		k.Email, sites, map[string]any{"properties": len(sites)}), nil
}

func init() { checks["google"] = checkGoogle }
