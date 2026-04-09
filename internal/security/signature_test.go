package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignAndVerify(t *testing.T) {
	keyHex := "943b421c9eb07c830af81030552c86009268de4a7405e1de8b52c3c88f703df2"
	saltHex := "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53"
	path := "/api/v1/images/abc123?w=800&h=600"

	sig, err := SignURL(path, keyHex, saltHex, 32)
	require.NoError(t, err)
	assert.NotEmpty(t, sig)

	err = VerifyURL(sig, path, keyHex, saltHex, 32, true)
	assert.NoError(t, err)
}

func TestVerifyWrongSignature(t *testing.T) {
	keyHex := "943b421c9eb07c830af81030552c86009268de4a7405e1de8b52c3c88f703df2"
	saltHex := "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53"

	err := VerifyURL("invalidsig", "/api/v1/images/abc123", keyHex, saltHex, 32, true)
	assert.Error(t, err)
}

func TestVerifySkipsWhenNotConfigured(t *testing.T) {
	err := VerifyURL("", "/api/v1/images/abc123", "", "", 32, false)
	assert.NoError(t, err)
}

func TestVerifyRequiredWithNoConfig(t *testing.T) {
	err := VerifyURL("", "/api/v1/images/abc123", "", "", 32, true)
	assert.ErrorIs(t, err, ErrNoSignatureConfig)
}

func TestSignatureTruncation(t *testing.T) {
	keyHex := "943b421c9eb07c830af81030552c86009268de4a7405e1de8b52c3c88f703df2"
	saltHex := "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53"
	path := "/api/v1/images/abc123?w=800"

	sig16, err := SignURL(path, keyHex, saltHex, 16)
	require.NoError(t, err)

	sig32, err := SignURL(path, keyHex, saltHex, 32)
	require.NoError(t, err)

	// Truncated signature should be shorter
	assert.Less(t, len(sig16), len(sig32))

	// Truncated signature should verify correctly
	err = VerifyURL(sig16, path, keyHex, saltHex, 16, true)
	assert.NoError(t, err)
}

func TestVerifyTamperedPath(t *testing.T) {
	keyHex := "943b421c9eb07c830af81030552c86009268de4a7405e1de8b52c3c88f703df2"
	saltHex := "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53"
	path := "/api/v1/images/abc123?w=800"

	sig, err := SignURL(path, keyHex, saltHex, 32)
	require.NoError(t, err)

	// Tamper with the path
	err = VerifyURL(sig, "/api/v1/images/abc123?w=1600", keyHex, saltHex, 32, true)
	assert.ErrorIs(t, err, ErrSignatureMismatch)
}

func TestVerifyMissingSignatureNotRequired(t *testing.T) {
	keyHex := "943b421c9eb07c830af81030552c86009268de4a7405e1de8b52c3c88f703df2"
	saltHex := "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53"

	// Empty signature when not required should pass
	err := VerifyURL("", "/api/v1/images/abc123", keyHex, saltHex, 32, false)
	assert.NoError(t, err)
}

func TestVerifyMissingSignatureRequired(t *testing.T) {
	keyHex := "943b421c9eb07c830af81030552c86009268de4a7405e1de8b52c3c88f703df2"
	saltHex := "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53"

	err := VerifyURL("", "/api/v1/images/abc123", keyHex, saltHex, 32, true)
	assert.ErrorIs(t, err, ErrMissingSignature)
}

// TestSignVerifyCanonicalization ensures a URL signed with one param order
// verifies when the client later sends the params in a different order.
// This was the root cause of the HMAC canonicalization HIGH finding.
func TestSignVerifyCanonicalization(t *testing.T) {
	keyHex := "943b421c9eb07c830af81030552c86009268de4a7405e1de8b52c3c88f703df2"
	saltHex := "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53"

	// Sign in "natural" order a user might choose.
	signed := "/api/v1/images/abc?w=800&h=600&format=webp"
	sig, err := SignURL(signed, keyHex, saltHex, 32)
	require.NoError(t, err)

	// Verify with reordered query string.
	reordered := "/api/v1/images/abc?format=webp&h=600&w=800"
	err = VerifyURL(sig, reordered, keyHex, saltHex, 32, true)
	assert.NoError(t, err)

	// Also verify with a dangling "sig" in the path — Canonicalize must drop it.
	withSig := "/api/v1/images/abc?h=600&w=800&format=webp&sig=junk"
	err = VerifyURL(sig, withSig, keyHex, saltHex, 32, true)
	assert.NoError(t, err)
}

func TestCanonicalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/foo", "/foo"},
		{"/foo?", "/foo"},
		{"/foo?a=1", "/foo?a=1"},
		{"/foo?b=2&a=1", "/foo?a=1&b=2"},
		{"/foo?b=2&a=1&sig=x", "/foo?a=1&b=2"},
		{"/foo?sig=x", "/foo"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, Canonicalize(c.in), "in=%q", c.in)
	}
}
