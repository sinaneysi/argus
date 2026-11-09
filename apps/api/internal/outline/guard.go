package outline

import (
	"context"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The SSRF guard: https only, no user info, query or fragment, port 443 unless the allow list names host:port, the
// host must be on the allow list, and no address it resolves to may be loopback, link-local, unspecified,
// multicast, reserved or a cloud metadata address. The call is pinned to the checked address.

// Refused is a refusal with its reason (shown to the admin).
type Refused string

func (r Refused) Error() string { return string(r) }

var (
	hostRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*(:[0-9]{1,5})?$`)
	pathRe  = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*$`)
	metaIPs = []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254", "168.63.129.16"}
	// reserved: 240.0.0.0/4 and the IPv6 ranges Python's ipaddress calls reserved
	reservedNets = mustNets("240.0.0.0/4", "::/8", "100::/8", "200::/7", "400::/6", "800::/5", "1000::/4", "4000::/3",
		"6000::/3", "8000::/3", "a000::/3", "c000::/3", "e000::/4", "f000::/5", "f800::/6", "fe00::/9")
)

func mustNets(cidrs ...string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}

// HostOK: a host or host:port of the allow list.
func HostOK(h string) bool { return hostRe.MatchString(h) }

// ParseURL is (normalised base, host, port) or a refusal. No network.
func ParseURL(raw string, allowLocal bool) (string, string, string, error) {
	if len(raw) > 300 || strings.IndexFunc(raw, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) >= 0 {
		return "", "", "", Refused("the Outline URL is not valid")
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil {
		return "", "", "", Refused("the Outline URL is not valid")
	}
	if u.Scheme != "https" && !(allowLocal && u.Scheme == "http") {
		return "", "", "", Refused("the Outline URL must start with https://")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return "", "", "", Refused("the Outline URL must not contain a user name or password")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", "", "", Refused("the Outline URL must not have a query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", "", Refused("the Outline URL has no host")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	if !pathRe.MatchString(u.EscapedPath()) {
		return "", "", "", Refused("the Outline URL path may only hold letters, digits and . _ ~ -")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + u.Path, host, port, nil
}

// BadAddress is why an address may not be called ("" when it may).
func BadAddress(ip net.IP, allowLocal bool) string {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if allowLocal && ip.IsLoopback() {
		return ""
	}
	for _, m := range metaIPs {
		if ip.Equal(net.ParseIP(m)) {
			return "a cloud metadata"
		}
	}
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast():
		return "link-local"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsMulticast():
		return "multicast"
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return "reserved"
		}
	}
	return ""
}

// Resolver resolves a host (tests replace it).
var Resolver = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	var out []net.IP
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, err
}

// CheckURL is (base, host key, pinned address) for a URL Argus may call.
func CheckURL(raw string, hosts []string, allowLocal bool) (string, string, string, error) {
	base, host, port, err := ParseURL(raw, allowLocal)
	if err != nil {
		return "", "", "", err
	}
	key := host
	if port != "443" {
		key = host + ":" + port
	}
	ok := false
	for _, h := range hosts {
		ok = ok || h == key
	}
	if !ok {
		list := strings.Join(hosts, ", ")
		if list == "" {
			list = "none"
		}
		return "", "", "", Refused(key + " is not an allowed Outline host (Allowed Outline hosts: " + list + ")")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ips, err = Resolver(ctx, host)
		if err != nil || len(ips) == 0 {
			return "", "", "", Refused(host + " does not resolve from this server")
		}
	}
	for _, ip := range ips {
		if why := BadAddress(ip, allowLocal); why != "" {
			return "", "", "", Refused(host + " resolves to " + why + " address " + ip.String() + "; refused")
		}
	}
	return base, key, ips[0].String(), nil
}
