package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/loader"
)

// TestBuildVolumeStoresIngestsBundle asserts that bundle-loaded
// ConfigMap/Secret bytes flow into the Store and are visible via
// Resolve, while inline overrides still win on the same key.
func TestBuildVolumeStoresIngestsBundle(t *testing.T) {
	// Reset any global flag state from other tests in this package.
	gf = globalFlags{
		configMaps: []string{"app-config=greeting=from-inline"},
	}
	b := &loader.Bundle{
		ConfigMaps: map[string]map[string][]byte{
			"app-config": {
				"greeting": []byte("from-bundle"),
				"lang":     []byte("en"),
			},
			"other-cfg": {
				"k": []byte("v"),
			},
		},
		Secrets: map[string]map[string][]byte{
			"app-secret": {"token": []byte("hunter2")},
		},
	}

	cm, sec, err := buildVolumeStores(t.TempDir(), b)
	if err != nil {
		t.Fatalf("buildVolumeStores: %v", err)
	}
	got, err := cm.Resolve("app-config")
	if err != nil {
		t.Fatalf("resolve app-config: %v", err)
	}
	if s := string(got["greeting"]); s != "from-inline" {
		t.Errorf("greeting = %q, want from-inline (inline beats bundle)", s)
	}
	if s := string(got["lang"]); s != "en" {
		t.Errorf("lang = %q, want en (bundle-only key)", s)
	}
	got2, err := cm.Resolve("other-cfg")
	if err != nil {
		t.Fatalf("resolve other-cfg: %v", err)
	}
	if s := string(got2["k"]); s != "v" {
		t.Errorf("other-cfg.k = %q, want v", s)
	}
	gotSec, err := sec.Resolve("app-secret")
	if err != nil {
		t.Fatalf("resolve app-secret: %v", err)
	}
	if s := string(gotSec["token"]); s != "hunter2" {
		t.Errorf("token = %q, want hunter2 (bundle)", s)
	}
}

// TestBuildVolumeStoresNilBundleIsBackwardCompatible ensures the
// old call signature semantics survive when no `-f`-loaded
// resources are present.
func TestBuildVolumeStoresNilBundleIsBackwardCompatible(t *testing.T) {
	gf = globalFlags{}
	cm, sec, err := buildVolumeStores(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cm == nil || sec == nil {
		t.Fatal("expected non-nil stores")
	}
}

// TestRunFlagsResolverDefaults asserts that the resolver flags wire
// into globalFlags and that the default cache dir, allow-list, and
// offline mode are sane out of the box.
func TestRunFlagsResolverDefaults(t *testing.T) {
	// Reset
	gf = globalFlags{}
	cmd := newRootCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The flags must exist (this is the scaffolding promise).
	if cmd.PersistentFlags().Lookup("resolver-cache-dir") == nil {
		t.Error("missing --resolver-cache-dir flag")
	}
	if cmd.PersistentFlags().Lookup("offline") == nil {
		t.Error("missing --offline flag")
	}
	if cmd.PersistentFlags().Lookup("resolver-allow") == nil {
		t.Error("missing --resolver-allow flag")
	}
	if cmd.PersistentFlags().Lookup("remote-resolver-context") == nil {
		t.Error("missing --remote-resolver-context flag")
	}
	if cmd.PersistentFlags().Lookup("resolver-config") == nil {
		t.Error("missing --resolver-config flag")
	}
	if cmd.PersistentFlags().Lookup("resolver-allow-insecure-http") == nil {
		t.Error("missing --resolver-allow-insecure-http flag")
	}

	// Default allow-list MUST match docs/AGENTS.md (Phase 1: git, hub,
	// http, bundles — cluster is opt-in).
	want := []string{"git", "hub", "http", "bundles"}
	if len(gf.resolverAllow) != len(want) {
		t.Errorf("resolverAllow = %v, want %v", gf.resolverAllow, want)
	}
	for i, n := range want {
		if i < len(gf.resolverAllow) && gf.resolverAllow[i] != n {
			t.Errorf("resolverAllow[%d] = %q, want %q", i, gf.resolverAllow[i], n)
		}
	}
	if gf.offline {
		t.Error("--offline default = true, want false")
	}
}

// TestRunFlagsClusterResolverDefaultsAreOff: --cluster-resolver-context
// and --cluster-resolver-kubeconfig must exist as flags but default to
// empty, so the security stance "cluster resolver off by default" is
// preserved in the absence of explicit user opt-in.
func TestRunFlagsClusterResolverDefaultsAreOff(t *testing.T) {
	gf = globalFlags{}
	cmd := newRootCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cmd.PersistentFlags().Lookup("cluster-resolver-context") == nil {
		t.Error("missing --cluster-resolver-context flag")
	}
	if cmd.PersistentFlags().Lookup("cluster-resolver-kubeconfig") == nil {
		t.Error("missing --cluster-resolver-kubeconfig flag")
	}
	if gf.clusterResolverContext != "" {
		t.Errorf("clusterResolverContext default = %q, want empty (off-by-default)", gf.clusterResolverContext)
	}
	if gf.clusterResolverKubeconfig != "" {
		t.Errorf("clusterResolverKubeconfig default = %q, want empty", gf.clusterResolverKubeconfig)
	}
	// The default --resolver-allow MUST NOT include "cluster".
	for _, n := range gf.resolverAllow {
		if n == "cluster" {
			t.Errorf("default --resolver-allow includes %q (security: cluster must be opt-in)", n)
		}
	}
}

// TestRunFlagsClusterResolverContextOptsIn: parsing --cluster-resolver-
// context=<ctx> wires the flag into globalFlags so the registry
// constructor can flip AllowCluster=true.
func TestRunFlagsClusterResolverContextOptsIn(t *testing.T) {
	gf = globalFlags{}
	cmd := newRootCmd()
	if err := cmd.PersistentFlags().Parse([]string{"--cluster-resolver-context", "ci-test"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gf.clusterResolverContext != "ci-test" {
		t.Errorf("clusterResolverContext = %q, want ci-test", gf.clusterResolverContext)
	}
}

// TestRunFlagsRemoteResolverNamespaceAndTimeout: Phase 5 adds two new
// flags that pair with --remote-resolver-context. They must exist with
// the documented defaults (namespace=default, timeout=60s).
func TestRunFlagsRemoteResolverNamespaceAndTimeout(t *testing.T) {
	gf = globalFlags{}
	cmd := newRootCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cmd.PersistentFlags().Lookup("remote-resolver-namespace") == nil {
		t.Error("missing --remote-resolver-namespace flag")
	}
	if cmd.PersistentFlags().Lookup("remote-resolver-timeout") == nil {
		t.Error("missing --remote-resolver-timeout flag")
	}
	if gf.remoteResolverNamespace != "default" {
		t.Errorf("remoteResolverNamespace default = %q, want \"default\"", gf.remoteResolverNamespace)
	}
	if gf.remoteResolverTimeout != 60*time.Second {
		t.Errorf("remoteResolverTimeout default = %v, want 60s", gf.remoteResolverTimeout)
	}
}

// TestRunFlagsRemoteResolverParseOverrides: the namespace + timeout
// flags accept overrides from the command line.
func TestRunFlagsRemoteResolverParseOverrides(t *testing.T) {
	gf = globalFlags{}
	cmd := newRootCmd()
	if err := cmd.PersistentFlags().Parse([]string{
		"--remote-resolver-context", "prod",
		"--remote-resolver-namespace", "tekton-pipelines",
		"--remote-resolver-timeout", "5m",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gf.remoteResolverContext != "prod" {
		t.Errorf("remoteResolverContext = %q, want prod", gf.remoteResolverContext)
	}
	if gf.remoteResolverNamespace != "tekton-pipelines" {
		t.Errorf("remoteResolverNamespace = %q, want tekton-pipelines", gf.remoteResolverNamespace)
	}
	if gf.remoteResolverTimeout != 5*time.Minute {
		t.Errorf("remoteResolverTimeout = %v, want 5m", gf.remoteResolverTimeout)
	}
}

// TestRunRemoteResolverContextLoadsKubeconfig: with a temp kubeconfig
// pointing at a fake context, buildRemoteResolver constructs a
// RemoteResolver against that context. We use a syntactically valid
// kubeconfig (no real cluster reachable, but the client config layer
// won't actually dial — Resolve is not called in this test).
func TestRunRemoteResolverContextLoadsKubeconfig(t *testing.T) {
	dir := t.TempDir()
	kubeconfigPath := filepath.Join(dir, "config")
	const kubeconfigYAML = `apiVersion: v1
kind: Config
current-context: test-context
clusters:
- name: test-cluster
  cluster:
    server: https://localhost:6443
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
- name: other-context
  context:
    cluster: test-cluster
    user: test-user
users:
- name: test-user
  user:
    token: test-token
`
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfigYAML), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	gf = globalFlags{
		remoteResolverContext:   "test-context",
		remoteResolverNamespace: "default",
		remoteResolverTimeout:   30 * time.Second,
	}
	t.Setenv("KUBECONFIG", kubeconfigPath)

	rr, err := buildRemoteResolver()
	if err != nil {
		t.Fatalf("buildRemoteResolver: %v", err)
	}
	if rr == nil {
		t.Fatal("expected non-nil RemoteResolver")
	}
	if got := rr.Name(); got != "remote" {
		t.Errorf("Name() = %q, want \"remote\"", got)
	}
}

// TestRunRemoteResolverDisabledByDefault: with no
// --remote-resolver-context, buildRemoteResolver returns (nil, nil) so
// the registry stays in direct mode.
func TestRunRemoteResolverDisabledByDefault(t *testing.T) {
	gf = globalFlags{}
	rr, err := buildRemoteResolver()
	if err != nil {
		t.Fatalf("buildRemoteResolver: %v", err)
	}
	if rr != nil {
		t.Errorf("RemoteResolver = %v, want nil (no --remote-resolver-context)", rr)
	}
}

// TestRunRemoteResolverInvalidKubeconfig: a bogus kubeconfig path
// surfaces as an error from buildRemoteResolver, which the run
// command translates to exit code 3 (environment).
func TestRunRemoteResolverInvalidKubeconfig(t *testing.T) {
	gf = globalFlags{
		remoteResolverContext: "no-such",
	}
	t.Setenv("KUBECONFIG", "/no/such/kubeconfig")
	_, err := buildRemoteResolver()
	if err == nil {
		t.Fatal("expected error from invalid kubeconfig path")
	}
}

// TestResolverCacheDirDefault: when --resolver-cache-dir is empty, the
// CLI's helper resolves to $XDG_CACHE_HOME/tkn-act/resolved.
func TestResolverCacheDirDefault(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-test")
	got := resolveResolverCacheDir("")
	want := "/tmp/xdg-test/tkn-act/resolved"
	if got != want {
		t.Errorf("resolverCacheDir = %q, want %q", got, want)
	}

	// Explicit override wins.
	got = resolveResolverCacheDir("/explicit/path")
	if got != "/explicit/path" {
		t.Errorf("explicit = %q, want /explicit/path", got)
	}
}
