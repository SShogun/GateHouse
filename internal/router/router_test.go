package router

import (
	"net/http"
	"sync"
	"testing"
)

func TestCompileRoutePrecedence(t *testing.T) {
	tests := []struct {
		name   string
		routes []Route
		input  Request
		want   string
	}{
		{
			name: "exact host beats wildcard host",
			routes: []Route{
				{ID: "wild", Host: "*.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
				{ID: "exact", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
			},
			input: Request{Host: "API.EXAMPLE.COM:8443", Path: "/x"}, want: "exact",
		},
		{
			name: "wildcard matches one label only",
			routes: []Route{
				{ID: "wild", Host: "*.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
			},
			input: Request{Host: "a.b.example.com", Path: "/x"}, want: "",
		},
		{
			name: "exact path beats prefix",
			routes: []Route{
				{ID: "prefix", Host: "api.example.com", Path: "/v1", PathType: PathPrefix, ClusterID: "c"},
				{ID: "exact", Host: "api.example.com", Path: "/v1", PathType: PathExact, ClusterID: "c"},
			},
			input: Request{Host: "api.example.com", Path: "/v1"}, want: "exact",
		},
		{
			name: "longer prefix beats shorter",
			routes: []Route{
				{ID: "short", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
				{ID: "long", Host: "api.example.com", Path: "/v1", PathType: PathPrefix, ClusterID: "c"},
			},
			input: Request{Host: "api.example.com", Path: "/v1/users"}, want: "long",
		},
		{
			name: "method constraint beats unconstrained",
			routes: []Route{
				{ID: "any", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
				{ID: "get", Host: "api.example.com", Path: "/", PathType: PathPrefix, Methods: []string{"GET"}, ClusterID: "c"},
			},
			input: Request{Host: "api.example.com", Path: "/x", Method: "GET"}, want: "get",
		},
		{
			name: "header constraint beats unconstrained",
			routes: []Route{
				{ID: "any", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
				{ID: "json", Host: "api.example.com", Path: "/", PathType: PathPrefix, Headers: map[string]string{"Content-Type": "application/json"}, ClusterID: "c"},
			},
			input: Request{Host: "api.example.com", Path: "/x", Headers: http.Header{"Content-Type": []string{"application/json"}}}, want: "json",
		},
		{
			name: "equal rules for same cluster use lexical route id",
			routes: []Route{
				{ID: "z-route", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
				{ID: "a-route", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
			},
			input: Request{Host: "api.example.com", Path: "/x"}, want: "a-route",
		},
		{
			name: "path comparison preserves escapes and repeated slashes",
			routes: []Route{
				{ID: "escaped", Host: "api.example.com", Path: "/a%2Fb", PathType: PathExact, ClusterID: "c"},
			},
			input: Request{Host: "api.example.com", Path: "/a%2Fb"}, want: "escaped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := Compile(Config{Routes: tt.routes}, []string{"c"})
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			got, ok := matcher.Match(tt.input)
			if tt.want == "" {
				if ok {
					t.Fatalf("Match() = %q, want no route", got.ID)
				}
				return
			}
			if !ok || got.ID != tt.want {
				t.Fatalf("Match() = (%q, %v), want (%q, true)", got.ID, ok, tt.want)
			}
		})
	}
}

func TestMatchReturnedRouteCannotMutateMatcher(t *testing.T) {
	route := Route{
		ID:        "json-get",
		Host:      "api.example.com",
		Path:      "/",
		PathType:  PathPrefix,
		Methods:   []string{"GET"},
		Headers:   map[string]string{"Content-Type": "application/json"},
		ClusterID: "c",
	}
	matcher, err := Compile(Config{Routes: []Route{route}}, []string{"c"})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	request := Request{
		Host:    "api.example.com",
		Path:    "/items",
		Method:  "GET",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	}
	returned, ok := matcher.Match(request)
	if !ok {
		t.Fatal("Match() found no route")
	}
	returned.Methods[0] = "POST"
	returned.Headers["Content-Type"] = "text/plain"

	got, ok := matcher.Match(request)
	if !ok || got.ID != "json-get" {
		t.Fatalf("mutation of returned route changed later matching: (%q, %v)", got.ID, ok)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	start := make(chan struct{})
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			returned.Methods[0] = "POST"
			returned.Headers["Content-Type"] = "text/plain"
			returned.Methods[0] = "GET"
			returned.Headers["Content-Type"] = "application/json"
		}
	}()
	close(start)
	for i := 0; i < 1000; i++ {
		if _, ok := matcher.Match(request); !ok {
			t.Fatal("concurrent mutation of returned route changed matching")
		}
	}
	wg.Wait()
}

func TestCompileRejectsEquivalentSelectorsWithConflictingClusters(t *testing.T) {
	routes := []Route{
		{ID: "blue", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "blue-cluster"},
		{ID: "green", Host: "api.example.com", Path: "/", PathType: PathPrefix, ClusterID: "green-cluster"},
	}
	if _, err := Compile(Config{Routes: routes}, []string{"blue-cluster", "green-cluster"}); err == nil {
		t.Fatal("Compile() error = nil, want conflicting destination rejection")
	}
}

func TestCompileRejectsAmbiguousRoutes(t *testing.T) {
	tests := []struct {
		name   string
		routes []Route
	}{
		{
			name: "intersecting header constraints at same rank",
			routes: []Route{
				{ID: "a", Host: "api.example.com", Path: "/", PathType: PathPrefix, Methods: []string{"GET"}, Headers: map[string]string{"X-A": "1"}, ClusterID: "a"},
				{ID: "b", Host: "api.example.com", Path: "/", PathType: PathPrefix, Methods: []string{"GET"}, Headers: map[string]string{"X-B": "2"}, ClusterID: "b"},
			},
		},
		{
			name: "intersecting method constraints at same rank",
			routes: []Route{
				{ID: "a", Host: "api.example.com", Path: "/", PathType: PathPrefix, Methods: []string{"GET", "POST"}, ClusterID: "a"},
				{ID: "b", Host: "api.example.com", Path: "/", PathType: PathPrefix, Methods: []string{"GET", "PUT"}, ClusterID: "b"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Compile(Config{Routes: tt.routes}, []string{"a", "b"}); err == nil {
				t.Fatal("Compile() error = nil, want ambiguous route rejection")
			}
		})
	}
}

func TestCompileRejectsUnknownClusterReference(t *testing.T) {
	_, err := Compile(Config{Routes: []Route{{ID: "r", Path: "/", PathType: PathPrefix, ClusterID: "missing"}}}, nil)
	if err == nil {
		t.Fatal("Compile() error = nil, want missing cluster rejection")
	}
}

func FuzzRouteMatch(f *testing.F) {
	f.Add("api.example.com", "/v1/users", "GET", "application/json")
	f.Add("API.EXAMPLE.COM:443", "/a%2Fb//../c", "POST", "text/plain")
	f.Add("x.example.com", "", "", "")
	f.Fuzz(func(t *testing.T, host, path, method, contentType string) {
		// Keep fuzz inputs bounded so this property stays a matcher test rather
		// than a memory/CPU stress test.
		if len(host) > 256 || len(path) > 1024 || len(method) > 32 || len(contentType) > 128 {
			t.Skip()
		}
		routes := []Route{
			{ID: "fallback", Path: "/", PathType: PathPrefix, ClusterID: "c"},
			{ID: "host", Host: "*.example.com", Path: "/", PathType: PathPrefix, ClusterID: "c"},
			{ID: "path", Host: "*.example.com", Path: "/v1", PathType: PathPrefix, ClusterID: "c"},
			{ID: "exact", Host: "api.example.com", Path: "/v1", PathType: PathExact, Methods: []string{"GET"}, Headers: map[string]string{"Content-Type": "application/json"}, ClusterID: "c"},
		}
		matcher, err := Compile(Config{Routes: routes}, []string{"c"})
		if err != nil {
			t.Fatalf("valid seed config rejected: %v", err)
		}
		req := Request{Host: host, Path: path, Method: method, Headers: http.Header{"Content-Type": []string{contentType}}}
		first, firstOK := matcher.Match(req)
		second, secondOK := matcher.Match(req)
		if firstOK != secondOK || first.ID != second.ID {
			t.Fatalf("nondeterministic Match(): (%q,%v) then (%q,%v)", first.ID, firstOK, second.ID, secondOK)
		}
		if firstOK {
			winnerMatches := false
			for _, route := range routes {
				if routeMatches(route, req) && routePrecedes(route, first) {
					t.Fatalf("winner %q loses precedence to matching route %q", first.ID, route.ID)
				}
				if route.ID == first.ID && routeMatches(route, req) {
					winnerMatches = true
				}
			}
			if !winnerMatches {
				t.Fatalf("winner %q does not match request", first.ID)
			}
		}
	})
}
