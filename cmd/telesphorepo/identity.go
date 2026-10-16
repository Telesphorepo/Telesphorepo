package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// identity is the identity of a Telesphorepo server: the scheme (http or https), the host in
// lower case, the effective port (80 or 443 when the URL names none) and the base path without a
// trailing slash ("" for the root). The CLI sends a request only to the canonical URL of the
// server (base).
type identity struct {
	scheme   string
	host     string // lower case; an IPv6 address keeps its brackets
	port     int
	basePath string
}

// parseIdentity reads a server URL such as https://repo.example.com. It must be http or https
// with a host, and have no user name, query or fragment.
func parseIdentity(raw string) (identity, error) {
	bad := fmt.Errorf("the server URL must be an http or https URL with a host and no user name, query or fragment, for example https://repo.example.com (got %q)", raw)
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Host == "" {
		return identity{}, bad
	}
	id := identity{scheme: strings.ToLower(u.Scheme)}
	if id.scheme != "http" && id.scheme != "https" {
		return identity{}, bad
	}
	host, port, err := splitHostPort(u.Host, defaultPort(id.scheme))
	if err != nil {
		return identity{}, bad
	}
	id.host, id.port = host, port
	id.basePath = strings.TrimRight(u.EscapedPath(), "/")
	return id, nil
}

// splitHostPort reads "host[:port]" with port def when it names none. The host is lower case and
// ASCII; an IPv6 address keeps its brackets.
func splitHostPort(hostport string, def int) (string, int, error) {
	if hostport == "" {
		return "", 0, errors.New("no host")
	}
	host, rest := hostport, ""
	if strings.HasPrefix(hostport, "[") {
		end := strings.IndexByte(hostport, ']')
		if end < 0 {
			return "", 0, errors.New("bad IPv6 address")
		}
		host, rest = hostport[:end+1], hostport[end+1:]
		if net.ParseIP(host[1:end]) == nil {
			return "", 0, errors.New("bad IPv6 address")
		}
	} else if i := strings.IndexByte(hostport, ':'); i >= 0 {
		host, rest = hostport[:i], hostport[i:]
	}
	port := def
	if rest != "" {
		if !strings.HasPrefix(rest, ":") {
			return "", 0, errors.New("bad port")
		}
		if p := rest[1:]; p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 65535 {
				return "", 0, errors.New("bad port")
			}
			port = n
		}
	}
	for _, r := range host {
		if r >= 0x80 {
			return "", 0, errors.New("write an internationalized host name in its ASCII form (xn--...)")
		}
	}
	host = strings.ToLower(host)
	if host == "" || host == "[]" {
		return "", 0, errors.New("no host")
	}
	return host, port, nil
}

func defaultPort(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

// base is the canonical URL: no default port and no trailing slash. API routes follow it.
func (id identity) base() string {
	return id.scheme + "://" + id.hostKey() + id.basePath
}

// hostKey is the host as Docker names a registry: host, and ":port" when the port is not the
// default of the scheme.
func (id identity) hostKey() string {
	if id.port == defaultPort(id.scheme) {
		return id.host
	}
	return id.host + ":" + strconv.Itoa(id.port)
}

var unsafeDirChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// deviceDir is the folder name under ~/.telesphorepo: the host without IPv6 brackets, then
// "_<port>" when the port is not the default of the scheme, then "_" and the base path with each
// "/" as "_" when the base path is not empty. Each character other than A-Z, a-z, 0-9, ".", "_"
// and "-" becomes "_".
func (id identity) deviceDir() string {
	b := strings.NewReplacer("[", "", "]", "").Replace(id.host)
	if id.port != defaultPort(id.scheme) {
		b += "_" + strconv.Itoa(id.port)
	}
	if id.basePath != "" {
		b += "_" + strings.ReplaceAll(id.basePath[1:], "/", "_")
	}
	return unsafeDirChars.ReplaceAllString(b, "_")
}

// registryKey reads the server URL that Docker gives a credential helper: "host[:port]", or a
// URL with a scheme and a path. It returns "host:port" with the port of the scheme (a key
// without a scheme takes the port of def), so that two names of one registry compare equal.
func registryKey(serverURL, def string) (string, error) {
	s := strings.TrimSpace(serverURL)
	scheme := def
	if i := strings.Index(s, "://"); i >= 0 {
		scheme = strings.ToLower(s[:i])
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	host, port, err := splitHostPort(s, defaultPort(scheme))
	if err != nil {
		return "", err
	}
	return host + ":" + strconv.Itoa(port), nil
}
