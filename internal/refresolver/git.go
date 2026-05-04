package refresolver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// GitResolver implements Resolver for taskRef.resolver: git. It honors
// the standard Tekton resolver params:
//
//	url        (required) — repo URL (file://, https://, ssh://, git@..)
//	revision   (default "main") — branch, tag, or full SHA
//	pathInRepo (required) — relative path to the YAML inside the repo
//
// The implementation uses go-git for cross-platform portability (no git
// CLI exec on the host required). Clones are shallow (Depth: 1) for the
// happy path and land in <cacheDir>/git/<sha256(url+revision)>/repo/.
// On a cache hit (the directory already exists), bytes are served from
// the cached working tree without any network IO.
//
// HTTPS / file:// URLs are accepted by default; plain http:// is refused
// unless AllowInsecureHTTP is true. SSH URLs (ssh:// or git@host:path)
// are passed through to go-git, which uses ssh-agent transparently when
// present (no in-tkn-act key handling).
type GitResolver struct {
	cacheDir          string
	allowInsecureHTTP bool

	// mu guards concurrent fetches against the same cache key. Two
	// PipelineTasks resolving the same (url, revision) within one run
	// would otherwise race to populate the cache directory.
	mu       sync.Mutex
	keyLocks map[string]*sync.Mutex
}

// NewGit returns a GitResolver that caches under cacheDir. cacheDir may
// be empty — in which case clones go to a tempdir and are cleaned up at
// the end of each Resolve call (no cache reuse). Production callers
// should pass a non-empty cacheDir (the CLI default is
// $XDG_CACHE_HOME/tkn-act/resolved).
func NewGit(cacheDir string) *GitResolver {
	return &GitResolver{cacheDir: cacheDir, keyLocks: map[string]*sync.Mutex{}}
}

// SetAllowInsecureHTTP toggles whether plain http:// URLs are accepted.
// The Registry constructor wires this from Options.AllowInsecureHTTP.
func (g *GitResolver) SetAllowInsecureHTTP(b bool) { g.allowInsecureHTTP = b }

// Name implements Resolver.
func (g *GitResolver) Name() string { return "git" }

// Resolve implements Resolver. See type-level docs for the param shape.
func (g *GitResolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
	repoURL := req.Params["url"]
	revision := req.Params["revision"]
	if revision == "" {
		revision = "main"
	}
	pathInRepo := req.Params["pathInRepo"]

	if repoURL == "" {
		return Resolved{}, errors.New("git: url param is required")
	}
	if pathInRepo == "" {
		return Resolved{}, errors.New("git: pathInRepo param is required")
	}
	if err := g.checkURLScheme(repoURL); err != nil {
		return Resolved{}, err
	}

	cleanPath, err := sanitizePathInRepo(pathInRepo)
	if err != nil {
		return Resolved{}, err
	}

	keyHex := gitCacheKey(repoURL, revision)
	repoDir, cleanup, err := g.acquireRepo(ctx, repoURL, revision, keyHex)
	if err != nil {
		return Resolved{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	// repoDir is "<cacheDir>/git/<keyHex>/repo" or a tempdir. Whatever
	// the resolver placed there, it has the working tree we cloned;
	// pathInRepo is relative to that root.
	full := filepath.Join(repoDir, cleanPath)
	bytes, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return Resolved{}, fmt.Errorf("git: pathInRepo %q not found at revision %q", pathInRepo, revision)
		}
		return Resolved{}, fmt.Errorf("git: reading pathInRepo %q: %w", pathInRepo, err)
	}

	headSHA, _ := readHEADSHA(repoDir)
	source := fmt.Sprintf("git: %s@%s -> %s", redactURL(repoURL), shortSHA(headSHA, revision), pathInRepo)

	hash := sha256.Sum256(bytes)
	res := Resolved{
		Bytes:  bytes,
		Source: source,
		SHA256: hex.EncodeToString(hash[:]),
		Cached: g.cachedHit(keyHex),
	}
	// Persist a marker so the next call sees Cached=true without
	// having to reach into go-git's internals.
	g.markCached(keyHex)
	return res, nil
}

// acquireRepo returns a directory containing the cloned working tree
// for the given (url, revision). On a cache hit it returns the cached
// path with a nil cleanup. On a miss it clones to the cache (if
// cacheDir is set) or to a tempdir (if cacheDir is empty), returning a
// cleanup that removes the tempdir.
func (g *GitResolver) acquireRepo(ctx context.Context, repoURL, revision, keyHex string) (dir string, cleanup func(), err error) {
	if g.cacheDir == "" {
		// No persistent cache; clone to a tempdir per Resolve call.
		tmp, terr := os.MkdirTemp("", "tkn-act-git-")
		if terr != nil {
			return "", nil, fmt.Errorf("git: tempdir: %w", terr)
		}
		if err := cloneShallow(ctx, repoURL, revision, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return "", nil, err
		}
		return tmp, func() { _ = os.RemoveAll(tmp) }, nil
	}
	root := filepath.Join(g.cacheDir, "git", keyHex)
	repoDir := filepath.Join(root, "repo")

	// Per-key lock: two concurrent Resolve calls for the same
	// (url, revision) must serialize so only one wins the clone.
	lock := g.keyLock(keyHex)
	lock.Lock()
	defer lock.Unlock()

	if dirExists(repoDir) {
		return repoDir, nil, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, fmt.Errorf("git: mkdir %s: %w", root, err)
	}
	if err := cloneShallow(ctx, repoURL, revision, repoDir); err != nil {
		// Best-effort cleanup so a half-cloned dir doesn't poison the
		// cache. RemoveAll is OK if the dir is missing.
		_ = os.RemoveAll(repoDir)
		return "", nil, err
	}
	return repoDir, nil, nil
}

func (g *GitResolver) keyLock(key string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, ok := g.keyLocks[key]
	if !ok {
		l = &sync.Mutex{}
		g.keyLocks[key] = l
	}
	return l
}

// cachedHit reports whether the on-disk cache directory for key already
// existed before this Resolve call attempted a clone. We check the
// presence of a marker file written at the end of the previous call,
// not just the dir's existence: the dir is created mid-clone and would
// otherwise read as "cached" before bytes had landed.
func (g *GitResolver) cachedHit(key string) bool {
	if g.cacheDir == "" {
		return false
	}
	marker := filepath.Join(g.cacheDir, "git", key, ".tkn-act-fetched")
	_, err := os.Stat(marker)
	return err == nil
}

func (g *GitResolver) markCached(key string) {
	if g.cacheDir == "" {
		return
	}
	marker := filepath.Join(g.cacheDir, "git", key, ".tkn-act-fetched")
	_ = os.WriteFile(marker, []byte("ok\n"), 0o644)
}

// checkURLScheme rejects plain http:// unless AllowInsecureHTTP is set.
// All other schemes (https, file, ssh, git@..) are accepted.
func (g *GitResolver) checkURLScheme(raw string) error {
	if g.allowInsecureHTTP {
		return nil
	}
	// Strip credentials before scheme checks so user-agent style
	// "user:pass@host" inside https:// stays accepted.
	if strings.HasPrefix(strings.ToLower(raw), "http://") {
		return errors.New("git: refusing insecure http:// URL (set --resolver-allow-insecure-http to override)")
	}
	// Be tolerant of URL shapes go-git supports: ssh, git@host:path, file://
	// without a host, etc. Only http:// is special-cased above.
	if u, err := url.Parse(raw); err == nil && u.Scheme == "http" {
		return errors.New("git: refusing insecure http:// URL (set --resolver-allow-insecure-http to override)")
	}
	return nil
}

// cloneShallow clones repoURL @ revision into dir using a depth-1 clone.
// revision may be a branch, a tag, or a full SHA. For SHAs go-git's
// ReferenceName lookup falls through to a deeper fetch via SingleBranch
// + ResolveRevision; we handle that case by retrying as a full clone if
// the shallow attempt finds no matching ref.
func cloneShallow(ctx context.Context, repoURL, revision, dir string) error {
	// First attempt: shallow clone of the named branch/tag.
	opts := &git.CloneOptions{
		URL:           repoURL,
		Depth:         1,
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(revision),
	}
	repo, err := git.PlainCloneContext(ctx, dir, false, opts)
	if err == nil {
		return nil
	}
	// branch lookup failed; try as a tag.
	_ = os.RemoveAll(dir)
	opts.ReferenceName = plumbing.NewTagReferenceName(revision)
	repo, err = git.PlainCloneContext(ctx, dir, false, opts)
	if err == nil {
		return nil
	}
	// tag lookup failed; might be a full SHA — fall back to a full clone
	// and check out by revision.
	_ = os.RemoveAll(dir)
	repo, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: repoURL})
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("git: clone %s: %w", redactURL(repoURL), err)
	}
	// Try to resolve the revision against the full repo.
	hash, rerr := repo.ResolveRevision(plumbing.Revision(revision))
	if rerr != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("git: revision %q not found in %s: %w", revision, redactURL(repoURL), rerr)
	}
	wt, err := repo.Worktree()
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("git: worktree: %w", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: *hash}); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("git: checkout %s: %w", revision, err)
	}
	return nil
}

// readHEADSHA reads the working tree's HEAD SHA via go-git. Best-effort
// — used for the human-readable Source string only.
func readHEADSHA(repoDir string) (string, error) {
	r, err := git.PlainOpen(repoDir)
	if err != nil {
		return "", err
	}
	ref, err := r.Head()
	if err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// gitCacheKey is the per-(url, revision) key. Distinct from the
// registry's CacheKey (which hashes all params) — this one is for the
// on-disk repo dir layout and is per-revision so a `revision: main`
// fetch and a `revision: HEAD` fetch don't share storage.
func gitCacheKey(url, revision string) string {
	h := sha256.New()
	h.Write([]byte(url))
	h.Write([]byte{0})
	h.Write([]byte(revision))
	return hex.EncodeToString(h.Sum(nil))
}

// sanitizePathInRepo refuses ".." traversals so a malicious resolver
// param can't escape the repo dir. Path is normalized but kept relative.
func sanitizePathInRepo(p string) (string, error) {
	cleaned := filepath.Clean(p)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("git: pathInRepo %q escapes the repository root", p)
	}
	return cleaned, nil
}

// redactURL strips embedded credentials (user:pass@host) so error
// messages, source strings, and any logged URL never leak tokens.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("***")
	return u.String()
}

// shortSHA returns a 7-char prefix of sha when non-empty; otherwise the
// raw revision string. Used in the Source output line.
func shortSHA(sha, fallback string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return fallback
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
