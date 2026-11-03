package refresolver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DiskCache is the on-disk resolved-bytes cache. Entries are keyed by
// (resolver, sortedKVs(SUBSTITUTED-params)) per the spec §3 cache-key
// invariant. The on-disk layout is:
//
//	<root>/<resolver>/<key>.yaml      // resolved bytes
//	<root>/<resolver>/<key>.json      // metadata (resolver, redacted params, source, sha256, fetched-at)
//	<root>/git/<sha256(url+rev)>/     // git resolver clone (repo/ + .tkn-act-fetched marker)
//
// Files are written 0600 and directories 0700: resolved bytes may come
// from private repositories. URL-shaped param values are redacted
// (userinfo and query string masked) before they land in the metadata
// sidecar. Git clone directories are listed, pruned and cleared
// alongside the blob entries so `tkn-act cache clear` also forces a
// re-clone of moving refs such as branches.
//
// Splitting by resolver lets `tkn-act cache prune` / `cache list`
// scope operations to a single resolver if desired, and matches the
// spec's documented layout.
//
// The Cache is concurrency-safe across processes only at the OS-level —
// two tkn-act runs racing to populate the same key may both write; the
// last writer wins (and both bytes are content-equivalent if the
// upstream is deterministic, which it is for git/hub/http/bundles by
// their cache-key invariants).
type DiskCache struct {
	root string
}

// NewDiskCache returns a DiskCache rooted at root. root may not exist
// yet — the first Put will MkdirAll.
func NewDiskCache(root string) *DiskCache {
	return &DiskCache{root: root}
}

// Root returns the cache's filesystem root.
func (c *DiskCache) Root() string { return c.root }

// CacheEntry describes one row in `tkn-act cache list`.
type CacheEntry struct {
	Resolver string    `json:"resolver"`
	Key      string    `json:"key"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mod_time"`
}

// cacheMeta is the JSON payload alongside each cached blob. We keep
// the resolver name + post-substitution params + source + sha for
// human debugging (per spec §10).
type cacheMeta struct {
	Resolver  string            `json:"resolver"`
	Params    map[string]string `json:"params"`
	Source    string            `json:"source,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
	FetchedAt time.Time         `json:"fetched_at"`
}

// Has returns true iff a cached entry exists for req. Errors reading
// the filesystem are conservative: the file is treated as missing.
func (c *DiskCache) Has(req Request) bool {
	if c == nil || c.root == "" {
		return false
	}
	p := c.path(req)
	_, err := os.Stat(p)
	return err == nil
}

// Get returns the cached Resolved for req. ok=false on miss; non-nil
// err only on filesystem corruption (truncated meta JSON, etc.).
//
// The returned Resolved has Cached=true so callers don't need to set
// it separately.
func (c *DiskCache) Get(req Request) (Resolved, bool, error) {
	if c == nil || c.root == "" {
		return Resolved{}, false, nil
	}
	p := c.path(req)
	bytes, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Resolved{}, false, nil
		}
		return Resolved{}, false, fmt.Errorf("cache read %s: %w", p, err)
	}
	out := Resolved{Bytes: bytes, Cached: true}
	// meta is best-effort; absence/corruption doesn't fail Get.
	if mb, err := os.ReadFile(c.metaPath(req)); err == nil {
		var m cacheMeta
		if jerr := json.Unmarshal(mb, &m); jerr == nil {
			out.Source = m.Source
			out.SHA256 = m.SHA256
		}
	}
	return out, true, nil
}

// Put writes resolved.Bytes + a small JSON metadata sidecar under the
// cache root. Existing entries are overwritten.
func (c *DiskCache) Put(req Request, resolved Resolved) error {
	if c == nil || c.root == "" {
		return errors.New("cache: nil or unrooted DiskCache")
	}
	dir := filepath.Join(c.root, sanitize(req.Resolver))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cache mkdir %s: %w", dir, err)
	}
	// MkdirAll leaves pre-existing (older, 0755) dirs alone; tighten.
	_ = os.Chmod(dir, 0o700)
	p := c.path(req)
	if err := writePrivateFile(p, resolved.Bytes); err != nil {
		return fmt.Errorf("cache write %s: %w", p, err)
	}
	meta := cacheMeta{
		Resolver:  req.Resolver,
		Params:    redactParams(req.Params),
		Source:    resolved.Source,
		SHA256:    resolved.SHA256,
		FetchedAt: time.Now().UTC(),
	}
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("cache marshal meta: %w", err)
	}
	if err := writePrivateFile(c.metaPath(req), mb); err != nil {
		return fmt.Errorf("cache write meta %s: %w", c.metaPath(req), err)
	}
	return nil
}

// writePrivateFile writes b to p with mode 0600, also tightening the
// mode of a pre-existing file (os.WriteFile keeps an existing mode).
func writePrivateFile(p string, b []byte) error {
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// redactParams returns a copy of params with URL-shaped values
// redacted, so tokens embedded in e.g. a git `url` param
// (https://user:token@host/...) or an http `url` query string never
// reach the on-disk metadata sidecar.
func redactParams(params map[string]string) map[string]string {
	if params == nil {
		return nil
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		if strings.Contains(v, "://") {
			v = redactURL(v)
		}
		out[k] = v
	}
	return out
}

// List walks the cache root and returns every cached blob. Errors
// reading individual sub-directories abort with the partial list.
func (c *DiskCache) List() ([]CacheEntry, error) {
	if c == nil || c.root == "" {
		return nil, nil
	}
	st, err := os.Stat(c.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("cache root %q is not a directory", c.root)
	}
	resolverDirs, err := os.ReadDir(c.root)
	if err != nil {
		return nil, err
	}
	var out []CacheEntry
	for _, rd := range resolverDirs {
		if !rd.IsDir() {
			continue
		}
		sub := filepath.Join(c.root, rd.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			return out, err
		}
		for _, f := range files {
			if f.IsDir() {
				if e, ok := gitCloneEntry(rd.Name(), sub, f.Name()); ok {
					out = append(out, e)
				}
				continue
			}
			name := f.Name()
			if !strings.HasSuffix(name, ".yaml") {
				continue // skip meta sidecars
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			key := strings.TrimSuffix(name, ".yaml")
			out = append(out, CacheEntry{
				Resolver: rd.Name(),
				Key:      key,
				Path:     filepath.Join(sub, name),
				Size:     info.Size(),
				ModTime:  info.ModTime(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resolver != out[j].Resolver {
			return out[i].Resolver < out[j].Resolver
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// PruneOlderThan deletes every cache entry whose mod-time is older
// than cutoff (now - older). Returns the number of entries pruned.
// A missing root is a no-op.
func (c *DiskCache) PruneOlderThan(older time.Duration) (int, error) {
	entries, err := c.List()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-older)
	n := 0
	for _, e := range entries {
		if !e.ModTime.Before(cutoff) {
			continue
		}
		if err := removeEntry(e); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Clear deletes every entry under the cache root. Returns the number
// of entries removed. A missing root is a no-op.
func (c *DiskCache) Clear() (int, error) {
	entries, err := c.List()
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := removeEntry(e); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}

// removeEntry deletes one List entry: a git clone directory is removed
// recursively; a blob is removed together with its meta sidecar.
func removeEntry(e CacheEntry) error {
	if st, err := os.Lstat(e.Path); err == nil && st.IsDir() {
		return os.RemoveAll(e.Path)
	}
	if err := os.Remove(e.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Best-effort meta removal.
	_ = os.Remove(strings.TrimSuffix(e.Path, ".yaml") + ".json")
	return nil
}

// gitCloneEntry recognizes the git resolver's per-(url, revision)
// clone directory <root>/git/<64-hex key>/ and describes it as a cache
// entry. Size is the total size of the regular files under it; ModTime
// is the last-use marker's mtime (the git resolver touches it on every
// resolve) or, failing that, the directory's own mtime.
func gitCloneEntry(resolverDir, sub, name string) (CacheEntry, bool) {
	if resolverDir != "git" || !isHexKey(name) {
		return CacheEntry{}, false
	}
	dir := filepath.Join(sub, name)
	st, err := os.Stat(dir)
	if err != nil {
		return CacheEntry{}, false
	}
	mod := st.ModTime()
	if mst, err := os.Stat(filepath.Join(dir, gitFetchedMarker)); err == nil {
		mod = mst.ModTime()
	}
	var size int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort size; skip unreadable entries
		}
		if d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return CacheEntry{Resolver: resolverDir, Key: name, Path: dir, Size: size, ModTime: mod}, true
}

func isHexKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// path returns the on-disk file path for a Request's resolved bytes.
func (c *DiskCache) path(req Request) string {
	return filepath.Join(c.root, sanitize(req.Resolver), CacheKey(req.Resolver, req.Params)+".yaml")
}

// metaPath returns the on-disk meta-json path for a Request.
func (c *DiskCache) metaPath(req Request) string {
	return filepath.Join(c.root, sanitize(req.Resolver), CacheKey(req.Resolver, req.Params)+".json")
}

// sanitize keeps the resolver subdir name to a sane filesystem-safe
// shape (resolver names are validated upstream; this is paranoia).
func sanitize(name string) string {
	if name == "" {
		return "_unnamed"
	}
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'a' && ch <= 'z',
			ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9',
			ch == '-', ch == '_':
			out = append(out, ch)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
