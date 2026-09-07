package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An alias is the mechanism that lets a name a client already sends keep
// working without falco absorbing the mismatch silently. It has to resolve to
// the SAME backend the target name resolves to — not merely return "ok".
func TestRegistry_AliasResolvesToItsTarget(t *testing.T) {
	alpha := &fastBackend{}
	beta := &fastBackend{}

	reg := NewRegistry(alpha)
	reg.Register("alpha", alpha)
	reg.Register("beta", beta)
	require.NoError(t, reg.SetDefault("alpha"))
	require.NoError(t, reg.RegisterAlias("birdple-dev", "beta"))

	got, err := reg.Get("birdple-dev")
	require.NoError(t, err)
	assert.Same(t, beta, got)
	assert.Equal(t, "beta", reg.Canonical("birdple-dev"))
}

// Names returns the buckets, not the aliases: the panel and /stats enumerate
// storage, and listing an alias there would show one bucket twice.
func TestRegistry_AliasesAreNotBuckets(t *testing.T) {
	backend := &fastBackend{}
	reg := NewRegistry(backend)
	reg.Register("alpha", backend)
	require.NoError(t, reg.SetDefault("alpha"))
	require.NoError(t, reg.RegisterAlias("birdple-dev", "alpha"))

	assert.NotContains(t, reg.Names(), "birdple-dev")
	assert.Equal(t, map[string]string{"birdple-dev": "alpha"}, reg.Aliases())
}

// A misconfigured alias has to stop the boot. Accepted here, it would surface
// as refused requests in production with nothing pointing at the config.
func TestRegistry_RegisterAliasRefusesBadInput(t *testing.T) {
	backend := &fastBackend{}
	reg := NewRegistry(backend)
	reg.Register("alpha", backend)

	assert.ErrorIs(t, reg.RegisterAlias("ghost-alias", "ghost"), ErrBackendNotFound,
		"an alias pointing at no bucket must be refused")
	assert.ErrorIs(t, reg.RegisterAlias("alpha", "alpha"), ErrInvalidConfiguration,
		"an alias must not shadow a bucket of the same name")
	assert.ErrorIs(t, reg.RegisterAlias("", "alpha"), ErrInvalidConfiguration)
	assert.ErrorIs(t, reg.RegisterAlias("something", ""), ErrInvalidConfiguration)
}

// Canonical is what the scope check runs on, so an unknown name must come back
// unchanged rather than being rewritten to the default. Silently canonicalising
// it would re-create the very fallback this work removed.
func TestRegistry_CanonicalLeavesUnknownNamesAlone(t *testing.T) {
	backend := &fastBackend{}
	reg := NewRegistry(backend)
	reg.Register("alpha", backend)
	require.NoError(t, reg.SetDefault("alpha"))

	assert.Equal(t, "ghost", reg.Canonical("ghost"))
	assert.Equal(t, "alpha", reg.Canonical(""), "an empty name is the default bucket")

	_, err := reg.Get("ghost")
	assert.ErrorIs(t, err, ErrBackendNotFound)
	assert.Contains(t, err.Error(), "ghost", "the error must name what was asked for")
}
