package security

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

// Golden vectors for the signing scheme.
//
// The scheme is reimplemented in TypeScript (birdple/src/server/images/sign.ts
// and birdple-api/src/modules/images/images.sign.ts). A round-trip test signs
// and verifies with the same code, so it stays green through any change to
// Canonicalize or computeSignature — while every URL those services sign
// starts failing with 403 INVALID_SIGNATURE in production. These vectors pin
// the bytes; if one fails, the canonicalisation changed, and the TypeScript
// copies must change in the same release (or the change reverted).
//
// The signature is base64url(HMAC-SHA256(key, salt || canonical)), truncated
// to the signature size before encoding. The expected values were cross-checked
// with an independent implementation (Python's hmac module).
const (
	goldenKey  = "943b421c9eb07c830af81030552c86009268de4e532ba2ee2eab8247c6da0881"
	goldenSalt = "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53cb7a7fe9fea309c5"
)

var goldenVectors = []struct {
	name, path, canonical, sig32, sig16 string
}{
	{"no query", "/api/v1/images/abc123", "/api/v1/images/abc123",
		"KGQgo-y66xQtp53YdGc7k_e8EVik_XMBL1fsN495lLA", "KGQgo-y66xQtp53YdGc7kw"},
	{"query is sorted", "/api/v1/images/abc123?w=400&f=webp", "/api/v1/images/abc123?f=webp&w=400",
		"qdw3UG00_7d4_acBiBo204lzQv11QyxgJByrYNNqliI", "qdw3UG00_7d4_acBiBo20w"},
	{"already sorted", "/api/v1/images/abc123?f=webp&w=400", "/api/v1/images/abc123?f=webp&w=400",
		"qdw3UG00_7d4_acBiBo204lzQv11QyxgJByrYNNqliI", "qdw3UG00_7d4_acBiBo20w"},
	{"sig in the middle is dropped", "/api/v1/images/abc123?w=400&sig=ignored&f=webp", "/api/v1/images/abc123?f=webp&w=400",
		"qdw3UG00_7d4_acBiBo204lzQv11QyxgJByrYNNqliI", "qdw3UG00_7d4_acBiBo20w"},
	{"repeated key keeps its order", "/api/v1/images/abc123?w=400&w=200", "/api/v1/images/abc123?w=400&w=200",
		"0saKs4URPkXKNM-UWe-5Q6uv8ZHJfFpXkCQUqgqNxNI", "0saKs4URPkXKNM-UWe-5Qw"},
	{"slash in a value is escaped", "/api/v1/images/dir/abc123.webp?b=birdple-dev&d=users/42",
		"/api/v1/images/dir/abc123.webp?b=birdple-dev&d=users%2F42",
		"qfqT9zpSwyDEhreI0Y4wB8VivteKoBqwhqqLZPPgiz0", "qfqT9zpSwyDEhreI0Y4wBw"},
	{"plus for space", "/api/v1/images/abc123?wm_url=https%3A%2F%2Fcdn.example%2Flogo+v2.png",
		"/api/v1/images/abc123?wm_url=https%3A%2F%2Fcdn.example%2Flogo+v2.png",
		"LMAblLv2nJD1MMPNECummFSQdK-Zkxd0PfXw6EnTrBs", "LMAblLv2nJD1MMPNECummA"},
	{"%20 for space signs like plus", "/api/v1/images/abc123?wm_url=https%3A%2F%2Fcdn.example%2Flogo%20v2.png",
		"/api/v1/images/abc123?wm_url=https%3A%2F%2Fcdn.example%2Flogo+v2.png",
		"LMAblLv2nJD1MMPNECummFSQdK-Zkxd0PfXw6EnTrBs", "LMAblLv2nJD1MMPNECummA"},
	{"utf-8 value", "/api/v1/images/abc123?name=%C3%B1and%C3%BA", "/api/v1/images/abc123?name=%C3%B1and%C3%BA",
		"0lhmYakGwXdmAITlslJsh5IAdMuN_TMv-UJJTkOct-c", "0lhmYakGwXdmAITlslJshw"},
	{"exp is signed", "/api/v1/images/abc123?w=400&exp=1893456000", "/api/v1/images/abc123?exp=1893456000&w=400",
		"uG0eVt6JtcW5-BcGuP6rZ1zFeed7e_yQPUtof05kPhQ", "uG0eVt6JtcW5-BcGuP6rZw"},
}

func TestSignURL_GoldenVectors(t *testing.T) {
	for _, v := range goldenVectors {
		t.Run(v.name, func(t *testing.T) {
			if got := Canonicalize(v.path); got != v.canonical {
				t.Errorf("Canonicalize = %q, want %q", got, v.canonical)
			}
			for size, want := range map[int]string{32: v.sig32, 16: v.sig16} {
				got, err := SignURL(v.path, goldenKey, goldenSalt, size)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("SignURL(size %d) = %q, want %q — the signing scheme changed", size, got, want)
				}
			}
		})
	}
}

func TestVerifyURLWithPolicy_Expiry(t *testing.T) {
	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	past := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)

	sign := func(path string) string {
		t.Helper()
		sig, err := SignURL(path, goldenKey, goldenSalt, 32)
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	verify := func(sig, path string, requireExpiry bool) error {
		return VerifyURLWithPolicy(sig, path, goldenKey, goldenSalt, 32, true, requireExpiry)
	}

	valid := "/api/v1/images/abc?w=1&exp=" + future
	if err := verify(sign(valid), valid, true); err != nil {
		t.Fatalf("valid signed URL with a future exp: %v", err)
	}

	expired := "/api/v1/images/abc?w=1&exp=" + past
	if err := verify(sign(expired), expired, true); !errors.Is(err, ErrExpiredSignature) {
		t.Fatalf("expired: got %v", err)
	}

	noExp := "/api/v1/images/abc?w=1"
	if err := verify(sign(noExp), noExp, true); !errors.Is(err, ErrMissingExpiry) {
		t.Fatalf("missing exp with requireExpiry: got %v", err)
	}
	if err := verify(sign(noExp), noExp, false); err != nil {
		t.Fatalf("missing exp without requireExpiry: %v", err)
	}

	badExp := "/api/v1/images/abc?exp=tomorrow"
	if err := verify(sign(badExp), badExp, true); !errors.Is(err, ErrInvalidExpiry) {
		t.Fatalf("unparseable exp: got %v", err)
	}

	// Moving exp invalidates the signature: the window is part of the MAC.
	tampered := "/api/v1/images/abc?w=1&exp=" + strconv.FormatInt(time.Now().Add(240*time.Hour).Unix(), 10)
	if err := verify(sign(valid), tampered, true); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("extended exp: got %v", err)
	}
	// An expired URL with a forged signature reports the mismatch, not the
	// expiry: the MAC is checked first.
	if err := verify("AAAA", expired, true); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("forged and expired: got %v", err)
	}
}

func TestSignURLWithExpiry_ReplacesExistingExp(t *testing.T) {
	sig, path, err := SignURLWithExpiry("/api/v1/images/abc?exp=1&w=1", 1893456000, goldenKey, goldenSalt, 32)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/images/abc?exp=1893456000&w=1" {
		t.Fatalf("path = %q", path)
	}
	if err := VerifyURLWithPolicy(sig, path, goldenKey, goldenSalt, 32, true, true); err != nil {
		t.Fatal(err)
	}
}
