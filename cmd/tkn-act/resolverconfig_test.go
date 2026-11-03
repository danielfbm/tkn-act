package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

func TestLoadResolverConfig(t *testing.T) {
	if cfg, err := loadResolverConfig(""); err != nil || cfg != (resolverConfig{}) {
		t.Fatalf("empty path: cfg=%+v err=%v", cfg, err)
	}
	dir := t.TempDir()
	p := writeFile(t, dir, "rc.yaml", "hub:\n  token: h1\nhttp:\n  token: t1\n")
	cfg, err := loadResolverConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub.Token != "h1" || cfg.HTTP.Token != "t1" {
		t.Fatalf("cfg = %+v", cfg)
	}
	j := writeFile(t, dir, "rc.json", `{"hub":{"token":"h2"}}`)
	if cfg, err := loadResolverConfig(j); err != nil || cfg.Hub.Token != "h2" {
		t.Fatalf("json: cfg=%+v err=%v", cfg, err)
	}
	typo := writeFile(t, dir, "typo.yaml", "hub:\n  tokn: h1\n")
	if _, err := loadResolverConfig(typo); err == nil {
		t.Fatal("unknown key must be rejected")
	}
	if _, err := loadResolverConfig(dir + "/missing.yaml"); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestApplyResolverConfig_HTTPToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("kind: Task\n"))
	}))
	defer srv.Close()

	saved := gf
	t.Cleanup(func() { gf = saved })
	gf.resolverAllowInsecureHTTP = true
	t.Setenv(refresolver.HTTPTokenEnv, "")

	reg := refresolver.NewDefaultRegistry(refresolver.Options{AllowInsecureHTTP: true})
	applyResolverConfig(reg, resolverConfig{HTTP: resolverTokenConfig{Token: "s3cret"}, Hub: resolverTokenConfig{Token: "hubtok"}})
	if _, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "http", Params: map[string]string{"url": srv.URL + "/t.yaml"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotAuth != "Bearer s3cret" {
		t.Fatalf("Authorization = %q, want Bearer s3cret", gotAuth)
	}
}
