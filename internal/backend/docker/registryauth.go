package docker

import (
	"fmt"

	"github.com/docker/docker/api/types/registry"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// registryKeychain resolves pull credentials. The default keychain
// reads $DOCKER_CONFIG/config.json (or ~/.docker/config.json),
// including `auths` entries and credential helpers / credsStore, i.e.
// exactly what `docker login` wrote. Variable so tests can swap it.
var registryKeychain authn.Keychain = authn.DefaultKeychain

// registryAuthFor returns the X-Registry-Auth header value for pulling
// img: the API's ImagePull sends no credentials on its own (unlike the
// docker CLI), so private images failed even after `docker login`.
// Returns "" for anonymous access or an unparseable reference (the
// daemon then reports the real pull error).
func registryAuthFor(img string) (string, error) {
	ref, err := name.ParseReference(img)
	if err != nil {
		return "", nil
	}
	a, err := registryKeychain.Resolve(ref.Context())
	if err != nil {
		return "", fmt.Errorf("resolve registry credentials for %s: %w", ref.Context().RegistryStr(), err)
	}
	if a == authn.Anonymous {
		return "", nil
	}
	cfg, err := a.Authorization()
	if err != nil {
		return "", fmt.Errorf("registry credentials for %s: %w", ref.Context().RegistryStr(), err)
	}
	if *cfg == (authn.AuthConfig{}) {
		return "", nil
	}
	return registry.EncodeAuthConfig(registry.AuthConfig{
		Username:      cfg.Username,
		Password:      cfg.Password,
		Auth:          cfg.Auth,
		IdentityToken: cfg.IdentityToken,
		RegistryToken: cfg.RegistryToken,
		ServerAddress: ref.Context().RegistryStr(),
	})
}
