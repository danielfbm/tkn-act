package main

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// resolverConfig is the shape of the --resolver-config file (YAML or
// JSON). Only the keys below are read; unknown keys are rejected so a
// typo (`tokn:`) doesn't silently drop a credential.
//
//	hub:
//	  token: <bearer token for the hub resolver>
//	http:
//	  token: <bearer token for the http resolver; wins over TKNACT_HTTP_RESOLVER_TOKEN>
type resolverConfig struct {
	Hub  resolverTokenConfig `json:"hub,omitempty"`
	HTTP resolverTokenConfig `json:"http,omitempty"`
}

type resolverTokenConfig struct {
	Token string `json:"token,omitempty"`
}

// loadResolverConfig reads and parses path. An empty path returns the
// zero config (no file configured).
func loadResolverConfig(path string) (resolverConfig, error) {
	var cfg resolverConfig
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("--resolver-config: %w", err)
	}
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return cfg, fmt.Errorf("--resolver-config %s: %w (supported keys: hub.token, http.token)", path, err)
	}
	return cfg, nil
}

// applyResolverConfig re-registers the hub / http resolvers with the
// configured bearer tokens. Resolvers without a configured token keep
// the registry's defaults untouched.
func applyResolverConfig(reg *refresolver.Registry, cfg resolverConfig) {
	if cfg.Hub.Token != "" {
		reg.Register(refresolver.NewHubResolver(refresolver.HubOptions{Token: cfg.Hub.Token}))
	}
	if cfg.HTTP.Token != "" {
		reg.Register(refresolver.NewHTTPResolver(refresolver.HTTPOptions{
			AllowInsecureHTTP: gf.resolverAllowInsecureHTTP,
			Token:             cfg.HTTP.Token,
		}))
	}
}
