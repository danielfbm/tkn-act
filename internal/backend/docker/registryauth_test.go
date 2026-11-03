package docker

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/registry"
)

// TestRegistryAuthFor_UsesDockerConfig: ImagePull sent no
// RegistryAuth, so private images failed even after `docker login`.
// Credentials in $DOCKER_CONFIG/config.json must be forwarded.
func TestRegistryAuthFor_UsesDockerConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	cfg := `{"auths":{"registry.example.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("alice:s3cret")) + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := registryAuthFor("registry.example.com/team/app:1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("no RegistryAuth for a registry with stored credentials")
	}
	raw, err := base64.URLEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ac registry.AuthConfig
	if err := json.Unmarshal(raw, &ac); err != nil {
		t.Fatal(err)
	}
	if ac.Username != "alice" || ac.Password != "s3cret" || ac.ServerAddress != "registry.example.com" {
		t.Errorf("auth = %+v", ac)
	}

	// Anonymous for a registry without credentials.
	if got, err := registryAuthFor("other.example.com/x:1"); err != nil || got != "" {
		t.Errorf("anonymous: got %q, %v", got, err)
	}
	// Unparseable reference: let the daemon report it.
	if got, err := registryAuthFor("UPPER//bad"); err != nil || got != "" {
		t.Errorf("bad ref: got %q, %v", got, err)
	}
}
