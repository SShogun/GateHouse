// Package router compiles immutable route configuration into a deterministic matcher.
package router

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
)

// PathType describes whether a route path is an exact path or a byte prefix.
type PathType uint8

const (
	PathExact PathType = iota + 1
	PathPrefix
)

// Route is one immutable routing rule. Host may be empty (any host), an exact
// hostname, or a single-label wildcard such as "*.example.com". Configured
// paths and Request.Path are compared byte-for-byte; callers should pass the
// escaped request path when escaped path distinctions matter. No path cleaning,
// decoding, or slash collapsing is performed.
type Route struct {
	ID        string
	Host      string
	Path      string
	PathType  PathType
	Methods   []string
	Headers   map[string]string
	ClusterID string
}

// Config contains routes to compile. Cluster IDs are supplied separately to
// Compile so callers can validate references against their cluster inventory.
type Config struct {
	Routes []Route
}

// Request contains the matching fields already extracted from an HTTP request.
// Host may include a port; it is lower-cased and the port is removed for match.
type Request struct {
	Host    string
	Path    string
	Method  string
	Headers http.Header
}

// Matcher is an immutable compiled route set safe for concurrent matching.
type Matcher struct {
	routes []Route
}

// Compile validates route configuration and cluster references, then creates
// a deterministic matcher. Equal rules are permitted and resolved by lexical
// route ID; overlapping rules with equal precedence but different selectors
// are rejected as ambiguous.
func Compile(config Config, clusterIDs []string) (*Matcher, error) {
	clusters := make(map[string]struct{}, len(clusterIDs))
	for _, id := range clusterIDs {
		if id == "" {
			return nil, errors.New("cluster ID must not be empty")
		}
		clusters[id] = struct{}{}
	}

	routes := make([]Route, len(config.Routes))
	ids := make(map[string]struct{}, len(config.Routes))
	for i, route := range config.Routes {
		if route.ID == "" {
			return nil, fmt.Errorf("route %d: ID must not be empty", i)
		}
		if _, exists := ids[route.ID]; exists {
			return nil, fmt.Errorf("route %q: duplicate ID", route.ID)
		}
		ids[route.ID] = struct{}{}
		if route.ClusterID == "" {
			return nil, fmt.Errorf("route %q: cluster ID must not be empty", route.ID)
		}
		if _, exists := clusters[route.ClusterID]; !exists {
			return nil, fmt.Errorf("route %q: unknown cluster %q", route.ID, route.ClusterID)
		}
		if route.PathType != PathExact && route.PathType != PathPrefix {
			return nil, fmt.Errorf("route %q: invalid path type", route.ID)
		}
		host, err := normalizePatternHost(route.Host)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		route.Host = host
		route.Methods, err = normalizeMethods(route.Methods)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		route.Headers, err = normalizeHeaders(route.Headers)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		routes[i] = route
	}

	for i := range routes {
		for j := i + 1; j < len(routes); j++ {
			if ambiguous(routes[i], routes[j]) {
				return nil, fmt.Errorf("routes %q and %q have unresolved equal-precedence overlap", routes[i].ID, routes[j].ID)
			}
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	return &Matcher{routes: routes}, nil
}

// Match returns the highest-precedence matching route. A nil Matcher behaves
// as an empty matcher, keeping malformed call paths panic-free.
func (m *Matcher) Match(request Request) (Route, bool) {
	if m == nil {
		return Route{}, false
	}
	var winner Route
	found := false
	for _, route := range m.routes {
		if !routeMatches(route, request) {
			continue
		}
		if !found || routePrecedes(route, winner) {
			winner, found = route, true
		}
	}
	if !found {
		return Route{}, false
	}
	return cloneRoute(winner), true
}

func cloneRoute(route Route) Route {
	route.Methods = append([]string(nil), route.Methods...)
	if route.Headers != nil {
		headers := make(map[string]string, len(route.Headers))
		for name, value := range route.Headers {
			headers[name] = value
		}
		route.Headers = headers
	}
	return route
}

func routeMatches(route Route, request Request) bool {
	host := normalizeRequestHost(request.Host)
	if !hostMatches(route.Host, host) {
		return false
	}
	if route.PathType == PathExact && request.Path != route.Path {
		return false
	}
	if route.PathType == PathPrefix && !strings.HasPrefix(request.Path, route.Path) {
		return false
	}
	if len(route.Methods) > 0 && !contains(route.Methods, strings.ToUpper(request.Method)) {
		return false
	}
	for name, value := range route.Headers {
		if request.Headers.Get(name) != value {
			return false
		}
	}
	return true
}

func routePrecedes(a, b Route) bool {
	if hostRank(a.Host) != hostRank(b.Host) {
		return hostRank(a.Host) > hostRank(b.Host)
	}
	if a.PathType != b.PathType {
		return a.PathType == PathExact
	}
	if len(a.Path) != len(b.Path) {
		return len(a.Path) > len(b.Path)
	}
	if (len(a.Methods) > 0) != (len(b.Methods) > 0) {
		return len(a.Methods) > 0
	}
	if (len(a.Headers) > 0) != (len(b.Headers) > 0) {
		return len(a.Headers) > 0
	}
	return a.ID < b.ID
}

func hostRank(host string) int {
	switch {
	case host == "":
		return 0
	case strings.HasPrefix(host, "*."):
		return 1
	default:
		return 2
	}
}

func hostMatches(pattern, host string) bool {
	if pattern == "" {
		return true
	}
	if !strings.HasPrefix(pattern, "*.") {
		return pattern == host
	}
	suffix := pattern[1:] // includes the leading dot
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := strings.TrimSuffix(host, suffix)
	return label != "" && !strings.Contains(label, ".")
}

func normalizePatternHost(host string) (string, error) {
	if host == "" {
		return "", nil
	}
	host = strings.ToLower(host)
	if strings.ContainsAny(host, "/\\ \t\r\n") || strings.Contains(host, ":") {
		return "", errors.New("host pattern must be a hostname without a port")
	}
	if strings.HasPrefix(host, "*.") {
		if strings.Count(host, "*") != 1 || len(host) <= 2 || strings.Contains(host[2:], "*") || strings.HasPrefix(host[2:], ".") {
			return "", errors.New("invalid single-label wildcard host")
		}
		return host, nil
	}
	if strings.Contains(host, "*") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return "", errors.New("invalid exact host")
	}
	return host, nil
}

func normalizeRequestHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(host, "[]")
}

func normalizeMethods(methods []string) ([]string, error) {
	if len(methods) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(methods))
	seen := map[string]struct{}{}
	for _, method := range methods {
		method = strings.ToUpper(method)
		if method == "" {
			return nil, errors.New("method constraint must not be empty")
		}
		if _, exists := seen[method]; !exists {
			out = append(out, method)
			seen[method] = struct{}{}
		}
	}
	sort.Strings(out)
	return out, nil
}

func normalizeHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if canonical == "" || strings.ContainsAny(canonical, " \t\r\n:") {
			return nil, fmt.Errorf("invalid header constraint name %q", name)
		}
		if _, exists := out[canonical]; exists {
			return nil, fmt.Errorf("duplicate header constraint %q", canonical)
		}
		out[canonical] = value
	}
	return out, nil
}

func ambiguous(a, b Route) bool {
	if !sameRank(a, b) || !routesOverlap(a, b) {
		return false
	}
	if !sameSelectors(a, b) {
		return true
	}
	return a.ClusterID != b.ClusterID
}

func sameRank(a, b Route) bool {
	return hostRank(a.Host) == hostRank(b.Host) && a.PathType == b.PathType && len(a.Path) == len(b.Path) &&
		(len(a.Methods) > 0) == (len(b.Methods) > 0) && (len(a.Headers) > 0) == (len(b.Headers) > 0)
}

func routesOverlap(a, b Route) bool {
	if !hostsOverlap(a.Host, b.Host) || !pathsOverlap(a, b) || !methodsOverlap(a.Methods, b.Methods) {
		return false
	}
	for name, value := range a.Headers {
		if other, exists := b.Headers[name]; exists && other != value {
			return false
		}
	}
	return true
}

func hostsOverlap(a, b string) bool {
	if a == "" || b == "" || a == b {
		return true
	}
	if strings.HasPrefix(a, "*.") {
		return hostMatches(a, b) || strings.HasPrefix(b, "*.") && hostMatches(b, strings.TrimPrefix(a, "*."))
	}
	if strings.HasPrefix(b, "*.") {
		return hostMatches(b, a)
	}
	return false
}

func pathsOverlap(a, b Route) bool {
	if a.PathType == PathExact {
		return pathMatches(b, a.Path)
	}
	if b.PathType == PathExact {
		return pathMatches(a, b.Path)
	}
	return strings.HasPrefix(a.Path, b.Path) || strings.HasPrefix(b.Path, a.Path)
}

func pathMatches(route Route, path string) bool {
	if route.PathType == PathExact {
		return path == route.Path
	}
	return strings.HasPrefix(path, route.Path)
}

func methodsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, method := range a {
		if contains(b, method) {
			return true
		}
	}
	return false
}

func sameSelectors(a, b Route) bool {
	if a.Host != b.Host || a.Path != b.Path || a.PathType != b.PathType || !equalStrings(a.Methods, b.Methods) || len(a.Headers) != len(b.Headers) {
		return false
	}
	for name, value := range a.Headers {
		if b.Headers[name] != value {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
