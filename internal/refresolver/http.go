package refresolver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// HTTPTokenEnv is the environment variable name the http resolver consults
// for a bearer token. An explicit HTTPOptions.Token wins over the env var.
const HTTPTokenEnv = "TKNACT_HTTP_RESOLVER_TOKEN"

// HTTPOptions configures an http resolver. All fields optional.
type HTTPOptions struct {
	// AllowInsecureHTTP opts the resolver into plain http:// URLs against
	// non-loopback hosts. Loopback URLs are always allowed (so unit tests
	// using httptest.NewServer work). The CLI plumbs this from
	// --resolver-allow-insecure-http (a CI-only escape hatch).
	AllowInsecureHTTP bool
	// Token, if non-empty, is sent as `Authorization: Bearer <token>`. If
	// empty the resolver consults the TKNACT_HTTP_RESOLVER_TOKEN env var
	// at Resolve time; a non-empty env value is then used.
	Token string
	// Client overrides http.Client. Tests may inject a httptest server's
	// transport. The default has a 30-second timeout.
	Client *http.Client
}

// HTTPResolver fetches Tasks/Pipelines from a plain HTTPS URL.
//
// Resolver params:
//
//	url   required. https:// by default; http:// only against loopback or
//	      with --resolver-allow-insecure-http.
//
// 5xx responses retry once with a 250ms backoff. Bearer tokens come from
// HTTPOptions.Token (precedence) or the TKNACT_HTTP_RESOLVER_TOKEN env var.
type HTTPResolver struct {
	opts HTTPOptions
}

// NewHTTPResolver constructs an http resolver.
func NewHTTPResolver(opts HTTPOptions) *HTTPResolver {
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPResolver{opts: opts}
}

// Name implements Resolver.
func (h *HTTPResolver) Name() string { return "http" }

// Resolve implements Resolver.
func (h *HTTPResolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
	rawURL := strings.TrimSpace(req.Params["url"])
	if rawURL == "" {
		return Resolved{}, fmt.Errorf("http: param %q is required", "url")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Resolved{}, fmt.Errorf("http: invalid url %q: %w", rawURL, err)
	}
	switch parsed.Scheme {
	case "https":
		// always fine.
	case "http":
		if !h.opts.AllowInsecureHTTP && !isLoopbackHost(parsed.Host) {
			return Resolved{}, fmt.Errorf(
				"http: refusing plain-http URL %q; HTTPS is required (use --resolver-allow-insecure-http for non-loopback http:// URLs)",
				rawURL)
		}
	default:
		return Resolved{}, fmt.Errorf("http: unsupported scheme %q in %q", parsed.Scheme, rawURL)
	}

	body, err := h.fetchWithRetry(ctx, rawURL)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Bytes:  body,
		Source: fmt.Sprintf("http: %s", rawURL),
	}, nil
}

func (h *HTTPResolver) fetchWithRetry(ctx context.Context, rawURL string) ([]byte, error) {
	const maxAttempts = 2
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, retry, err := h.fetchOnce(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry || attempt == maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, lastErr
}

func (h *HTTPResolver) fetchOnce(ctx context.Context, rawURL string) ([]byte, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("http: building request: %w", err)
	}
	if tok := h.bearerToken(); tok != "" {
		httpReq.Header.Set("Authorization", "Bearer "+tok)
	}
	httpReq.Header.Set("Accept", "text/yaml, application/yaml, application/octet-stream, */*")

	resp, err := h.opts.Client.Do(httpReq)
	if err != nil {
		return nil, true, fmt.Errorf("http: GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		body, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return nil, true, fmt.Errorf("http: reading body: %w", rerr)
		}
		return body, false, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, fmt.Errorf("http: GET %s: not found (status 404)", rawURL)
	case resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("http: GET %s: server error %d", rawURL, resp.StatusCode)
	default:
		return nil, false, fmt.Errorf("http: GET %s: unexpected status %d", rawURL, resp.StatusCode)
	}
}

// bearerToken returns HTTPOptions.Token if non-empty; otherwise it
// consults the TKNACT_HTTP_RESOLVER_TOKEN env var.
func (h *HTTPResolver) bearerToken() string {
	if h.opts.Token != "" {
		return h.opts.Token
	}
	return os.Getenv(HTTPTokenEnv)
}
