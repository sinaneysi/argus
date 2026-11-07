package app

// Port of the parts of app/backup_uri.py the web app uses: parse() (to resolve the database host and refuse a bad
// string before anything is dropped) and fingerprint(). The Python module stays for root's bin/ scripts.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// BUEngines is backup_uri.ENGINES (scheme -> engine).
var BUEngines = map[string]string{"mongodb": "mongodb", "mongodb+srv": "mongodb", "mysql": "mariadb-mysql", "mariadb": "mariadb-mysql",
	"postgres": "postgres", "postgresql": "postgres", "redis": "redis", "rediss": "redis",
	"sqlserver": "mssql", "mssql": "mssql"}

// BUPorts is backup_uri.PORTS.
var BUPorts = map[string]int64{"mongodb": 27017, "mariadb-mysql": 3306, "postgres": 5432, "redis": 6379, "mssql": 1433}

// BUError is backup_uri.URIError; its text is shown to the user.
type BUError struct{ Msg string }

func (e *BUError) Error() string { return e.Msg }

// BUHost is one (host, port) of a connection string; HasPort false is Python's None port (mongodb+srv).
type BUHost struct {
	Host    string
	Port    int64
	HasPort bool
}

// BUParsed is the dict backup_uri.parse returns (the fields the app reads).
type BUParsed struct {
	Scheme, Engine, Flavor, User, Password, DB string
	Hosts                                      []BUHost
	Options                                    *py.OMap
	SRV                                        bool
	ReplicaSet, AuthDB                         string
}

func buHosts(text string, defPort int64, hasDef bool) ([]BUHost, error) {
	var out []BUHost
	for _, part := range strings.Split(text, ",") {
		part = py.Strip(part)
		if part == "" {
			continue
		}
		var host, port string
		switch {
		case strings.HasPrefix(part, "["):
			end := strings.Index(part, "]")
			if end < 0 {
				return nil, &BUError{"IPv6 host without a closing ]"}
			}
			host = part[1:end]
			rest := part[end+1:]
			if strings.HasPrefix(rest, ":") {
				port = rest[1:]
			}
		case strings.Count(part, ":") == 1:
			host, port, _ = strings.Cut(part, ":")
		case strings.Contains(part, ":"):
			return nil, &BUError{"an IPv6 host must be written in brackets: [::1]:27017"}
		default:
			host = part
		}
		if host == "" || strings.ContainsAny(host, "/?#@ ") {
			return nil, &BUError{"bad host '" + host + "'"}
		}
		if port != "" {
			n, ok := bpAtoi(port)
			if !py.IsDigit(port) || !ok || n < 1 || n > 65535 {
				return nil, &BUError{"bad port '" + port + "'"}
			}
			out = append(out, BUHost{strings.ToLower(host), n, true})
		} else {
			out = append(out, BUHost{strings.ToLower(host), defPort, hasDef})
		}
	}
	if len(out) == 0 {
		return nil, &BUError{"no host"}
	}
	return out, nil
}

// buParseQSL is urllib.parse.parse_qsl(query, keep_blank_values=True).
func buParseQSL(q string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(q, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		out = append(out, [2]string{py.Unquote(strings.ReplaceAll(k, "+", " ")), py.Unquote(strings.ReplaceAll(v, "+", " "))})
	}
	return out
}

func buPop(o *py.OMap, k string) string {
	if !o.Has(k) {
		return ""
	}
	v := py.Str(o.Get(k))
	o.Delete(k)
	return v
}

// BUParse is backup_uri.parse(uri).
func BUParse(uri string) (*BUParsed, error) {
	uri = py.Strip(uri)
	if !strings.Contains(uri, "://") {
		return nil, &BUError{"not a connection string: expected scheme://... (mongodb, mysql, mariadb, postgres, redis, sqlserver)"}
	}
	scheme, rest, _ := strings.Cut(uri, "://")
	scheme = strings.ToLower(scheme)
	engine, ok := BUEngines[scheme]
	if !ok {
		return nil, &BUError{"unsupported scheme '" + scheme + "'"}
	}
	opts := py.NewOMap()
	var userinfo, hostpart, path string
	if engine == "mssql" && strings.Contains(rest, ";") {
		pairs := strings.Split(rest, ";")
		head := pairs[0]
		for _, p := range pairs[1:] {
			if k, v, ok := strings.Cut(p, "="); ok {
				opts.Set(strings.ToLower(py.Strip(k)), py.Strip(v))
			}
		}
		hostpart = head
		if i := strings.LastIndex(head, "@"); i >= 0 {
			userinfo, hostpart = head[:i], head[i+1:]
		}
	} else {
		query := ""
		if i := strings.Index(rest, "?"); i >= 0 {
			rest, query = rest[:i], rest[i+1:]
		}
		hostpart = rest
		if i := strings.LastIndex(rest, "@"); i >= 0 {
			userinfo, hostpart = rest[:i], rest[i+1:]
		}
		if i := strings.Index(hostpart, "/"); i >= 0 {
			hostpart, path = hostpart[:i], hostpart[i+1:]
		}
		for _, kv := range buParseQSL(query) {
			opts.Set(strings.ToLower(kv[0]), kv[1])
		}
	}
	user, password := "", ""
	if userinfo != "" {
		if u, p, ok := strings.Cut(userinfo, ":"); ok {
			user, password = u, p
		} else {
			user = userinfo
		}
		user, password = py.Unquote(user), py.Unquote(password)
	}
	if user == "" {
		user = buPop(opts, "user")
		if user == "" {
			user = buPop(opts, "username")
		}
	}
	if password == "" {
		password = buPop(opts, "password")
	}
	srv := scheme == "mongodb+srv"
	hosts, err := buHosts(hostpart, BUPorts[engine], !srv)
	if err != nil {
		return nil, err
	}
	if srv && (len(hosts) != 1 || hosts[0].HasPort) {
		return nil, &BUError{"mongodb+srv takes one host name without a port"}
	}
	db := ""
	if path != "" {
		db = py.Unquote(strings.Trim(path, "/"))
	}
	if engine == "mssql" && db == "" {
		db = opts.S("databasename")
		if db == "" {
			db = opts.S("database")
		}
	}
	if engine == "redis" && db != "" && !py.IsDigit(db) {
		return nil, &BUError{"redis database must be a number"}
	}
	flavor := ""
	if scheme == "mysql" {
		flavor = "mysql"
	} else if scheme == "mariadb" {
		flavor = "mariadb"
	}
	auth := opts.S("authsource")
	if auth == "" && engine == "mongodb" && db != "" {
		auth = db
	}
	return &BUParsed{Scheme: scheme, Engine: engine, Flavor: flavor, User: user, Password: password, Hosts: hosts, DB: db,
		Options: opts, SRV: srv, ReplicaSet: opts.S("replicaset"), AuthDB: auth}, nil
}

// BUFingerprint is backup_uri.fingerprint(uri): sha256 of the stripped string.
func BUFingerprint(uri string) string {
	s := sha256.Sum256([]byte(py.Strip(uri)))
	return hex.EncodeToString(s[:])
}
