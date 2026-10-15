package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestGooseURL pins the bracketing GooseURL exists for: an IPv6 host
// literal must come out bracketed, or the URL fails url.Parse on current
// Go (multiple colons in an unbracketed http(s) host — see
// golang/go#75223), which is exactly the input that used to cascade into
// WaitForReady's nil deref. Observed live: every conversation cold start
// SIGSEGVed the agent-runner on an IPv6-only k3s cluster, where every Pod
// IP is an IPv6 literal.
func TestGooseURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		want string
	}{
		{"IPv4 Pod/container IP", "10.0.4.12", GooseServePort, "http://10.0.4.12:3284"},
		{"IPv6 Pod IP (the crashing shape, bracketed)", "fd42:42:42::1f5", GooseServePort, "http://[fd42:42:42::1f5]:3284"},
		{"hostname", "localhost", 32941, "http://localhost:32941"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GooseURL(tt.host, tt.port); got != tt.want {
				t.Errorf("GooseURL(%q, %d) = %q, want %q", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

// TestGooseServeHostParsesAsSocketAddr pins the syntax GooseServeHost must
// satisfy: goose serve builds its bind address as SocketAddr from
// "host:port" (goose-cli's handle_serve_command), so the host literal must
// already carry its own IPv6 brackets — a bare "::" would produce "::3284"
// and exit with "invalid socket address syntax" at startup, while an
// unbracketed fix here would be invisible to every IPv4-only dev machine
// and only fail on IPv6-only pod networks.
func TestGooseServeHostParsesAsSocketAddr(t *testing.T) {
	bindAddr := GooseServeHost + ":" + fmt.Sprint(GooseServePort)
	addr, err := netip.ParseAddrPort(bindAddr)
	if err != nil {
		t.Fatalf("goose serve bind address %q does not parse as a SocketAddr (see goose-cli's handle_serve_command): %v", bindAddr, err)
	}
	if !addr.Addr().Is6() || !addr.Addr().IsUnspecified() {
		t.Errorf("bind address should be the IPv6 wildcard for a dual-stack listener, got %v", addr.Addr())
	}
	if addr.Port() != GooseServePort {
		t.Errorf("bind port = %d, want %d", addr.Port(), GooseServePort)
	}
}

// TestWaitForReadyInvalidCandidateIsErrorNotPanic is the regression test
// for the live failure GooseURL's doc comment describes: WaitForReady used
// to discard NewRequestWithContext's error (`req, _ :=`) and dereference
// the nil *http.Request on the very next line, SIGSEGVing the whole
// agent-runner process. An unparseable candidate must surface as an
// ordinary error instead — one that names the bad address and the parse
// cause, since it always reflects a caller-side construction bug.
func TestWaitForReadyInvalidCandidateIsErrorNotPanic(t *testing.T) {
	// The exact shape that crashed production: an unbracketed IPv6
	// literal with a port (http://%s:%d over an IPv6 Pod IP).
	_, err := WaitForReady(context.Background(), []string{"http://fd42:42:42::1f5:3284"}, "secret")
	if err == nil {
		t.Fatal("WaitForReady(invalid candidate) = nil error, want parse-failure error")
	}
	if !strings.Contains(err.Error(), "invalid candidate address") {
		t.Errorf("error should name the invalid candidate, got: %v", err)
	}
	if !strings.Contains(err.Error(), "fd42:42:42::1f5:3284") {
		t.Errorf("error should quote the offending address, got: %v", err)
	}
}

// TestWaitForReadyReturnsReadyCandidate covers the happy path the invalid
// candidate must not regress: the first candidate to answer /status with a
// non-5xx wins, and the secret key rides along on the probe request.
func TestWaitForReadyReturnsReadyCandidate(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("X-Secret-Key"), r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	baseURL, err := WaitForReady(ctx, []string{srv.URL}, "secret")
	if err != nil {
		t.Fatalf("WaitForReady = %v, want nil error", err)
	}
	if baseURL != srv.URL {
		t.Errorf("WaitForReady = %q, want %q", baseURL, srv.URL)
	}
	if gotPath != "/status" {
		t.Errorf("probe hit %q, want /status", gotPath)
	}
	if gotKey != "secret" {
		t.Errorf("probe sent X-Secret-Key %q, want \"secret\"", gotKey)
	}
}
