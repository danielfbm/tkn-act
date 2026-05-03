// Package volumes resolves Tekton TaskSpec.Volumes into host directories the
// docker backend can bind-mount.
//
// Four volume kinds are supported:
//
//	emptyDir   fresh per-Task tmpdir under the run cache
//	hostPath   literal host path (path mode honored only as diagnostic)
//	configMap  bytes from a Store (file-backed dir + inline overrides)
//	secret     same, with separate Store
//
// The validator rejects any other kind before this package sees it.
package volumes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// Store is a configMap-or-secret bytes-source.
//
// Lookup precedence (highest first):
//
//  1. Inline overrides (Add) — typically from `--configmap` / `--secret`.
//  2. On-disk Dir layout — typically from `--configmap-dir` / `--secret-dir`.
//  3. Bundle-loaded bytes (LoadBytes) — typically from `kind: ConfigMap`
//     / `kind: Secret` resources found in the `-f` YAML stream.
//
// Each layer is checked per (name, key); a key present at a higher
// layer hides the same key at lower layers.
type Store struct {
	Dir    string                       // <root>/<name>/<key> per source
	Inline map[string]map[string]string // name -> key -> value
	Bundle map[string]map[string][]byte // name -> key -> bytes
}

// NewStore returns an empty Store rooted at dir. Inline overrides may be
// added via Add and bundle-loaded data via LoadBytes. dir may be empty
// (no on-disk layout).
func NewStore(dir string) *Store {
	return &Store{
		Dir:    dir,
		Inline: map[string]map[string]string{},
		Bundle: map[string]map[string][]byte{},
	}
}

// Add records an inline override; later Lookup calls for (name, key) return
// the inline value instead of consulting the on-disk dir.
func (s *Store) Add(name, key, value string) {
	if s.Inline[name] == nil {
		s.Inline[name] = map[string]string{}
	}
	s.Inline[name][key] = value
}

// LoadBytes records bundle-loaded bytes for every key under `name`.
// These sit at the lowest precedence layer: a key present here is
// shadowed by the same key in either the on-disk Dir layout or the
// inline overrides.
//
// Calling LoadBytes twice for the same name is a merge (later keys
// overwrite earlier ones); duplicate-name detection happens at the
// loader layer, not here.
func (s *Store) LoadBytes(name string, bytesByKey map[string][]byte) {
	if s.Bundle[name] == nil {
		s.Bundle[name] = map[string][]byte{}
	}
	for k, v := range bytesByKey {
		// Copy the slice so later mutations of the caller's map don't
		// leak into the Store.
		cp := make([]byte, len(v))
		copy(cp, v)
		s.Bundle[name][k] = cp
	}
}

// Resolve returns the bytes for every key declared by source `name`.
// Layers are merged with this precedence (higher beats lower):
//
//  1. Inline (Add)
//  2. On-disk Dir
//  3. Bundle (LoadBytes)
//
// An error is returned only when no layer produced any keys.
func (s *Store) Resolve(name string) (map[string][]byte, error) {
	out := map[string][]byte{}
	// 3. Bundle (lowest).
	for k, v := range s.Bundle[name] {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	// 2. On-disk dir.
	if s.Dir != "" {
		base := filepath.Join(s.Dir, name)
		entries, err := os.ReadDir(base)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				data, rerr := os.ReadFile(filepath.Join(base, e.Name()))
				if rerr != nil {
					return nil, fmt.Errorf("read %s/%s: %w", name, e.Name(), rerr)
				}
				out[e.Name()] = data
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read source %s: %w", name, err)
		}
	}
	// 1. Inline (highest).
	for k, v := range s.Inline[name] {
		out[k] = []byte(v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source %q has no keys (looked in %s, inline overrides, and -f-loaded resources; pass --configmap/--secret, populate the dir, or include kind: ConfigMap/Secret in -f)", name, s.Dir)
	}
	return out, nil
}

// MaterializeForTask resolves every Volume in spec into a host directory and
// returns map[volumeName] -> hostPath. baseDir is a per-task scratch dir
// under which emptyDir / configMap / secret subdirs are created.
func MaterializeForTask(
	taskName string,
	vs []tektontypes.Volume,
	baseDir string,
	cm, sec *Store,
) (map[string]string, error) {
	if len(vs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", baseDir, err)
	}
	out := map[string]string{}
	for _, v := range vs {
		switch {
		case v.EmptyDir != nil:
			d := filepath.Join(baseDir, "emptyDir-"+v.Name)
			if err := os.MkdirAll(d, 0o755); err != nil {
				return nil, fmt.Errorf("volume %q: %w", v.Name, err)
			}
			out[v.Name] = d
		case v.HostPath != nil:
			out[v.Name] = v.HostPath.Path
		case v.ConfigMap != nil:
			d, err := materializeKeyValueDir(filepath.Join(baseDir, "cm-"+v.Name), cm, v.ConfigMap.Name, v.ConfigMap.Items)
			if err != nil {
				return nil, fmt.Errorf("volume %q (configMap %q): %w", v.Name, v.ConfigMap.Name, err)
			}
			out[v.Name] = d
		case v.Secret != nil:
			d, err := materializeKeyValueDir(filepath.Join(baseDir, "sec-"+v.Name), sec, v.Secret.SecretName, v.Secret.Items)
			if err != nil {
				return nil, fmt.Errorf("volume %q (secret %q): %w", v.Name, v.Secret.SecretName, err)
			}
			out[v.Name] = d
		default:
			return nil, fmt.Errorf("task %q volume %q: unsupported source", taskName, v.Name)
		}
	}
	return out, nil
}

func materializeKeyValueDir(dst string, store *Store, name string, items []tektontypes.KeyToPath) (string, error) {
	if store == nil {
		return "", fmt.Errorf("source %q: no store configured", name)
	}
	bytesByKey, err := store.Resolve(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	if len(items) == 0 {
		// Default: every key becomes <name-of-key> in dst.
		for k, v := range bytesByKey {
			if err := os.WriteFile(filepath.Join(dst, k), v, 0o644); err != nil {
				return "", err
			}
		}
		return dst, nil
	}
	// Items projection: only the listed keys, optionally renamed.
	for _, it := range items {
		v, ok := bytesByKey[it.Key]
		if !ok {
			return "", fmt.Errorf("source %q has no key %q (declared in items)", name, it.Key)
		}
		path := it.Path
		if path == "" {
			path = it.Key
		}
		// Disallow path escapes via subpath traversal.
		if strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
			return "", fmt.Errorf("items.path %q must be a relative path inside the volume", path)
		}
		full := filepath.Join(dst, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, v, 0o644); err != nil {
			return "", err
		}
	}
	return dst, nil
}
