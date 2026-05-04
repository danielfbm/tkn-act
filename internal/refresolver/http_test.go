package refresolver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// TestHTTPResolverHappyPath: GETs the URL param's content; returns bytes.
func TestHTTPResolverHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/task.yaml" {
			t.Errorf("path = %q, want /task.yaml", r.URL.Path)
		}
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()

	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	out, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/task.yaml"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "kind: Task") {
		t.Errorf("returned bytes do not look like a Task: %q", string(out.Bytes))
	}
	if !strings.HasPrefix(out.Source, "http: ") {
		t.Errorf("Source = %q, want prefix 'http: '", out.Source)
	}
}

// TestHTTPResolverMissingURLFailsLoudly: `url` param is required.
func TestHTTPResolverMissingURLFailsLoudly(t *testing.T) {
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing url")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error %q does not mention 'url'", err)
	}
}

// TestHTTPResolverRejectsHTTPByDefault: a plain http:// URL pointing at a
// non-loopback host is refused unless HTTPOptions.AllowInsecureHTTP is true.
func TestHTTPResolverRejectsHTTPByDefault(t *testing.T) {
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": "http://example.com/task.yaml"},
	})
	if err == nil {
		t.Fatal("expected refusal of plain http://")
	}
	if !strings.Contains(err.Error(), "https") && !strings.Contains(err.Error(), "insecure") {
		t.Errorf("error %q does not mention https/insecure", err)
	}
}

// TestHTTPResolverAllowsLoopbackHTTP: 127.0.0.1 / localhost are exempt
// even without AllowInsecureHTTP, so unit tests using httptest.NewServer
// (which serves on 127.0.0.1:<random>) work out of the box.
func TestHTTPResolverAllowsLoopbackHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/task.yaml"},
	}); err != nil {
		t.Errorf("loopback http should be allowed; got: %v", err)
	}
}

// TestHTTPResolverAllowInsecureHTTP: opt-in flag lets a non-loopback
// http:// URL through (e.g. an internal CI mirror).
func TestHTTPResolverAllowInsecureHTTP(t *testing.T) {
	// We don't actually want to send traffic to example.com; instead we
	// run a tiny custom server bound to a non-loopback hostname by
	// rewriting the URL host in the test. httptest servers always bind
	// to 127.0.0.1, so we approximate by toggling a custom transport
	// stub.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	// Substitute "example.com" for the loopback host so the loopback
	// exemption doesn't apply; the test transport routes back to the
	// httptest server regardless.
	url := strings.Replace(srv.URL, "127.0.0.1", "example.com", 1)
	url = strings.Replace(url, "[::1]", "example.com", 1)
	url += "/task.yaml"
	loopbackHost := strings.TrimPrefix(srv.URL, "http://")
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{
		AllowInsecureHTTP: true,
		Client: &http.Client{
			Transport: &reroutingTransport{target: loopbackHost},
		},
	})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": url},
	}); err != nil {
		t.Errorf("AllowInsecureHTTP should permit non-loopback http://; got: %v", err)
	}
}

// reroutingTransport rewrites the request's Host to a known httptest target.
type reroutingTransport struct {
	target string
}

func (rt *reroutingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Host = rt.target
	req2.Host = rt.target
	return http.DefaultTransport.RoundTrip(req2)
}

// TestHTTPResolverBearerTokenFromEnv: when TKNACT_HTTP_RESOLVER_TOKEN is
// set, the resolver sends Authorization: Bearer <token>.
func TestHTTPResolverBearerTokenFromEnv(t *testing.T) {
	t.Setenv("TKNACT_HTTP_RESOLVER_TOKEN", "secret-token")
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/task.yaml"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-token")
	}
}

// TestHTTPResolverBearerTokenFromOptions: an explicit Token in
// HTTPOptions wins over the env var.
func TestHTTPResolverBearerTokenFromOptions(t *testing.T) {
	t.Setenv("TKNACT_HTTP_RESOLVER_TOKEN", "env-token")
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{Token: "explicit-token"})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/task.yaml"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotAuth != "Bearer explicit-token" {
		t.Errorf("Authorization = %q, want explicit-token", gotAuth)
	}
}

// TestHTTPResolverNoTokenSendsNoAuth: when neither Token nor env is set,
// no Authorization header is sent.
func TestHTTPResolverNoTokenSendsNoAuth(t *testing.T) {
	t.Setenv("TKNACT_HTTP_RESOLVER_TOKEN", "")
	gotAuth := "<unset>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/task.yaml"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (no token)", gotAuth)
	}
}

// TestHTTPResolver404: 404s surface as errors that mention the URL.
func TestHTTPResolver404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/missing.yaml"},
	})
	if err == nil {
		t.Fatal("expected 404 to surface as error")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q does not mention 404 / not-found", err)
	}
}

// TestHTTPResolver5xxRetriesOnce: persistent 5xx fails after one retry.
func TestHTTPResolver5xxRetriesOnce(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/x.yaml"},
	})
	if err == nil {
		t.Fatal("expected error for persistent 5xx")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (one retry), got %d", got)
	}
}

// TestHTTPResolverMalformedURL: garbage URL fails clearly.
func TestHTTPResolverMalformedURL(t *testing.T) {
	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": "not a valid url at all"},
	})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
}

// TestHTTPResolverName: sanity check on Name().
func TestHTTPResolverName(t *testing.T) {
	if got := refresolver.NewHTTPResolver(refresolver.HTTPOptions{}).Name(); got != "http" {
		t.Errorf("Name() = %q, want %q", got, "http")
	}
}

// TestHTTPResolverRegisteredInDefaultRegistry: confirms http is wired
// into NewDefaultRegistry's allow-list and dispatch table after Phase 3.
func TestHTTPResolverRegisteredInDefaultRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		Allow: []string{"http"},
	})
	_, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": srv.URL + "/x.yaml"},
	})
	if err != nil {
		t.Fatalf("expected default-registry http dispatch to succeed against loopback; got %v", err)
	}
	if errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("http should be registered by default")
	}
}

// TestHTTPResolverAllowInsecureFlagThreadedThroughRegistry: when
// NewDefaultRegistry is called with Options.AllowInsecureHTTP=true, the
// http resolver inside it must accept non-loopback http:// URLs.
func TestHTTPResolverAllowInsecureFlagThreadedThroughRegistry(t *testing.T) {
	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		Allow:             []string{"http"},
		AllowInsecureHTTP: true,
	})
	// We just want to confirm the option got threaded to the resolver,
	// not actually hit the network. Use a short ctx deadline so the
	// connect attempt to a black-hole address aborts quickly; the
	// presence of any non-"refusing plain-http" error confirms that
	// AllowInsecureHTTP got threaded through the registry.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := reg.Resolve(ctx, refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": "http://198.51.100.1:1/missing.yaml"}, // TEST-NET-2 unrouteable
	})
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "refusing plain-http") || strings.Contains(err.Error(), "HTTPS is required") {
		t.Errorf("AllowInsecureHTTP=true did not propagate; got %q", err)
	}
}
