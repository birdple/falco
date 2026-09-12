package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aliasesFromEnv runs the loader over STORAGE_BUCKET_ALIASES and unmarshals the
// result the way Load does, so what is asserted is the value the server will
// actually see — not the raw string, and not what viper was handed.
func aliasesFromEnv(t *testing.T, raw string) map[string]string {
	t.Helper()

	t.Setenv("STORAGE_BUCKET_ALIASES", raw)

	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()
	l.loadBucketAliasesFromEnv(v)

	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	return cfg.Storage.BucketAliases
}

func TestBucketAliases_ParsedFromEnv(t *testing.T) {
	got := aliasesFromEnv(t, "birdple-dev=jay, birdple = jay ,shop=colibri")

	assert.Equal(t, map[string]string{
		"birdple-dev": "jay",
		"birdple":     "jay",
		"shop":        "colibri",
	}, got, "whitespace around an entry must not become part of a bucket name")
}

func TestBucketAliases_AbsentEnvLeavesNone(t *testing.T) {
	assert.Empty(t, aliasesFromEnv(t, ""))
}

// A malformed entry must not be dropped. Skipping it would leave the operator
// with an alias they believe is configured and requests refused as though it
// had never been written — the same silence the alias exists to remove.
func TestBucketAliases_MalformedEntryIsKeptForValidation(t *testing.T) {
	got := aliasesFromEnv(t, "birdple-dev=jay,oops")

	assert.Equal(t, "jay", got["birdple-dev"])
	require.Contains(t, got, "oops")
	assert.Empty(t, got["oops"], "a malformed entry is carried with no target so validation can name it")

	cfg := aliasConfig(got)
	assert.ErrorContains(t, NewValidator().Validate(cfg), `"oops" has no target bucket`)
}

// aliasConfig builds the smallest config that passes every other check, so a
// validation failure can only come from the aliases. The bucket is named "jay"
// after the one the stack really runs, and typed filesystem only because that
// is the type with the fewest required fields — nothing here depends on it.
func aliasConfig(aliases map[string]string) *Config {
	cfg := &Config{}
	cfg.Server.Port = 4009
	cfg.Server.Host = "0.0.0.0"
	cfg.Storage.Default = "jay"
	cfg.Storage.Buckets = map[string]BucketConfig{
		"jay": {Type: "filesystem", Path: "/tmp/falco-alias-test"},
	}
	cfg.Storage.BucketAliases = aliases
	cfg.Processing.MaxFileSizeMB = 10
	cfg.Processing.DefaultQuality = 85
	cfg.Processing.DefaultFormat = "webp"
	cfg.Cache.SizeMB = 256
	return cfg
}

func TestBucketAliases_Validation(t *testing.T) {
	tests := []struct {
		name    string
		aliases map[string]string
		wantErr string
	}{
		{
			name:    "a declared alias onto a declared bucket is accepted",
			aliases: map[string]string{"birdple-dev": "jay"},
		},
		{
			name:    "a target that is not a bucket stops the boot",
			aliases: map[string]string{"birdple-dev": "nope"},
			wantErr: `points at "nope", which is not in storage.buckets`,
		},
		{
			name:    "an alias may not shadow a bucket",
			aliases: map[string]string{"jay": "jay"},
			wantErr: `alias "jay" shadows a bucket of the same name`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewValidator().Validate(aliasConfig(tt.aliases))
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
