package refresolver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HubOptions configures a hub resolver. All fields optional.
type HubOptions struct {
	// BaseURL points at a Tekton Hub HTTP API. Default
	// https://api.hub.tekton.dev. HTTPS is required (the spec is firm
	// about hub being HTTPS-only).
	BaseURL string
	// Token, if non-empty, is sent as `Authorization: Bearer <token>`.
	Token string
	// Client overrides the http.Client. Tests inject httptest server
	// transport when needed; the default http.DefaultClient with a
	// 30-second timeout is used otherwise.
	Client *http.Client
}

// HubResolver fetches Tasks/Pipelines from a Tekton Hub HTTP API per
// https://github.com/tektoncd/hub. It honors the public API shape:
//
//	GET <baseURL>/v1/resource/<catalog>/<kind>/<name>/<version>/yaml
//
// Resolver params:
//
//	name      required
//	version   optional, default "latest"
//	kind      optional, default "task"
//	catalog   optional, default "tekton"
//
// HTTPS is required regardless of the run-level
// --resolver-allow-insecure-http flag (that flag scopes to the http
// resolver only). 5xx responses are retried once with a 250ms backoff;
// 404s are surfaced with a "not found" diagnostic.
type HubResolver struct {
	opts HubOptions
}

// NewHubResolver constructs a hub resolver with the given options.
func NewHubResolver(opts HubOptions) *HubResolver {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.hub.tekton.dev"
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HubResolver{opts: opts}
}

// Name implements Resolver.
func (h *HubResolver) Name() string { return "hub" }

// Resolve implements Resolver.
func (h *HubResolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
	name := strings.TrimSpace(req.Params["name"])
	if name == "" {
		return Resolved{}, fmt.Errorf("hub: param %q is required", "name")
	}
	version := strings.TrimSpace(req.Params["version"])
	if version == "" {
		version = "latest"
	}
	kind := strings.TrimSpace(req.Params["kind"])
	if kind == "" {
		kind = "task"
	}
	catalog := strings.TrimSpace(req.Params["catalog"])
	if catalog == "" {
		catalog = "tekton"
	}

	// HTTPS-only sanity check on the base URL. We allow http:// only when
	// the BaseURL points at a 127.0.0.1 / localhost test server (the unit
	// tests all do); refuse anything else.
	parsedBase, err := url.Parse(h.opts.BaseURL)
	if err != nil {
		return Resolved{}, fmt.Errorf("hub: invalid baseURL %q: %w", h.opts.BaseURL, err)
	}
	if parsedBase.Scheme == "http" && !isLoopbackHost(parsedBase.Host) {
		return Resolved{}, fmt.Errorf("hub: refusing plain-http base URL %q; HTTPS is required", h.opts.BaseURL)
	}

	endpoint := strings.TrimRight(h.opts.BaseURL, "/") +
		"/v1/resource/" +
		url.PathEscape(catalog) + "/" +
		url.PathEscape(kind) + "/" +
		url.PathEscape(name) + "/" +
		url.PathEscape(version) + "/yaml"

	body, err := h.fetchWithRetry(ctx, endpoint, name, version, kind, catalog)
	if err != nil {
		return Resolved{}, err
	}

	return Resolved{
		Bytes:  body,
		Source: fmt.Sprintf("hub: %s/%s/%s@%s", catalog, kind, name, version),
	}, nil
}

// fetchWithRetry performs the GET, retrying once on 5xx with a small
// backoff. 404s and 4xx errors are NOT retried.
func (h *HubResolver) fetchWithRetry(ctx context.Context, endpoint, name, version, kind, catalog string) ([]byte, error) {
	const maxAttempts = 2
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, retry, err := h.fetchOnce(ctx, endpoint, name, version, kind, catalog)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry || attempt == maxAttempts {
			break
		}
		// Tiny backoff between attempts. Tests with 5xx-then-200 expect
		// the resolver to make a second call; 250ms keeps the test fast.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, lastErr
}

// fetchOnce performs one HTTP GET; the bool signals whether the caller
// should retry (true on 5xx and transport errors, false on 4xx and on
// other terminal errors).
func (h *HubResolver) fetchOnce(ctx context.Context, endpoint, name, version, kind, catalog string) ([]byte, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, fmt.Errorf("hub: building request: %w", err)
	}
	if h.opts.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.opts.Token)
	}
	httpReq.Header.Set("Accept", "text/yaml, application/yaml, application/octet-stream, */*")

	resp, err := h.opts.Client.Do(httpReq)
	if err != nil {
		// Transport / DNS / connect / TLS — retryable.
		return nil, true, fmt.Errorf("hub: GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		body, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return nil, true, fmt.Errorf("hub: reading body: %w", rerr)
		}
		return body, false, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, fmt.Errorf("hub: %s/%s/%s@%s not found at %s (status 404)",
			catalog, kind, name, version, endpoint)
	case resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("hub: GET %s: server error %d", endpoint, resp.StatusCode)
	default:
		return nil, false, fmt.Errorf("hub: GET %s: unexpected status %d", endpoint, resp.StatusCode)
	}
}

// isLoopbackHost matches host:port forms that are 127.0.0.1, ::1, or
// localhost (case-insensitive). Used to allow httptest servers in unit
// tests without opening up plain HTTP for arbitrary hub mirrors.
func isLoopbackHost(host string) bool {
	// Strip the port if present.
	if i := strings.LastIndex(host, ":"); i != -1 {
		// Be defensive against IPv6 host:port (`[::1]:1234`).
		if !strings.Contains(host[:i], "]") || strings.HasPrefix(host, "[") {
			host = host[:i]
		}
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasPrefix(host, "127.")
}
