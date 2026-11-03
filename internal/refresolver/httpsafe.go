package refresolver

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxRedirects mirrors net/http's default redirect budget, which a
// custom CheckRedirect has to re-implement.
const maxRedirects = 10

// withSafeRedirects returns a shallow copy of base whose CheckRedirect
// (a) refuses https -> http downgrades unless allowInsecure is set and
// (b) drops the Authorization header whenever the redirect target's
// host:port differs from the original request's. net/http only strips
// credentials when the hostname changes and ignores the port and the
// scheme, so a same-host https -> http hop would otherwise resend the
// bearer token in cleartext. A caller-supplied CheckRedirect is kept
// as-is (the caller has taken ownership of redirect policy).
func withSafeRedirects(base *http.Client, allowInsecure bool, resolverName string) *http.Client {
	c := *base
	if c.CheckRedirect != nil {
		return &c
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("%s: stopped after %d redirects", resolverName, maxRedirects)
		}
		first := via[0].URL
		if !allowInsecure && strings.EqualFold(first.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("%s: refusing redirect from https to %s://%s (HTTPS downgrade)",
				resolverName, req.URL.Scheme, req.URL.Host)
		}
		if !strings.EqualFold(req.URL.Host, first.Host) || !strings.EqualFold(req.URL.Scheme, first.Scheme) {
			req.Header.Del("Authorization")
		}
		return nil
	}
	return &c
}

// readLimited reads at most MaxResolvedBytes from r and fails with a
// clear error if the stream is longer.
func readLimited(r io.Reader, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxResolvedBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResolvedBytes {
		return nil, fmt.Errorf("%s exceeds the %d MiB size limit", what, MaxResolvedBytes>>20)
	}
	return body, nil
}

// transportErr strips the *url.Error wrapper net/http puts around
// transport failures: its message embeds the full request URL
// (including any query-string tokens). The caller prints a redacted
// URL itself.
func transportErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// redactURL strips credentials from a URL so error messages, Source
// strings, cache metadata, and any logged URL never leak tokens:
// embedded userinfo (user:pass@host) becomes "***@" and a query string
// becomes "?REDACTED". URLs that don't parse but still look like
// "scheme://..." get the same treatment on a best-effort textual
// basis; anything else (e.g. scp-style git@host:path) is returned as-is.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return redactURLText(raw)
	}
	if u.User == nil && u.RawQuery == "" && !u.ForceQuery {
		return raw
	}
	hadUser := u.User != nil
	u.User = nil
	if u.RawQuery != "" || u.ForceQuery {
		u.RawQuery = "REDACTED"
		u.ForceQuery = false
	}
	s := u.String()
	if hadUser {
		if i := strings.Index(s, "://"); i != -1 {
			s = s[:i+3] + "***@" + s[i+3:]
		}
	}
	return s
}

// redactURLText is redactURL's fallback for strings url.Parse rejects.
func redactURLText(raw string) string {
	i := strings.Index(raw, "://")
	if i == -1 {
		return raw
	}
	scheme, rest := raw[:i+3], raw[i+3:]
	if q := strings.IndexAny(rest, "?#"); q != -1 {
		rest = rest[:q] + "?REDACTED"
	}
	hostEnd := strings.Index(rest, "/")
	if hostEnd == -1 {
		hostEnd = len(rest)
	}
	if at := strings.LastIndex(rest[:hostEnd], "@"); at != -1 {
		rest = "***@" + rest[at+1:]
	}
	return scheme + rest
}

// isLoopbackHost reports whether host (optionally host:port, IPv6 in
// brackets) is a loopback IP literal or "localhost". DNS names that
// merely start with "127." are NOT loopback.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	h := host
	if sh, _, err := net.SplitHostPort(host); err == nil {
		h = sh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
