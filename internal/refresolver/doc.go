// Package refresolver fetches Tekton Tasks/Pipelines referenced via
// taskRef.resolver / pipelineRef.resolver. It is intentionally distinct
// from internal/resolver, which performs $(...) variable substitution.
// The two packages share no types and do not depend on each other; the
// naming separation exists because both senses of "resolver" appear in
// the Tekton spec.
//
// Phase 1 (Track 1 #9) lands the type scaffolding and an "inline" stub
// resolver used by the test harness. Concrete resolvers (git, hub,
// http, bundles, cluster) and the remote ResolutionRequest driver land
// in subsequent phases. See docs/superpowers/plans/2026-05-04-resolvers.md.
package refresolver
