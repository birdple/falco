// Package security provides HMAC-based URL signing for image delivery endpoints.
// Adapted from imgproxy (https://github.com/imgproxy/imgproxy)
// Copyright (c) 2017 Sergey "DarthSim" Aleksandrovich — MIT License
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

var (
	ErrNoSignatureConfig = errors.New("HMAC key and salt are not configured")
	ErrInvalidEncoding   = errors.New("invalid signature encoding")
	ErrSignatureMismatch = errors.New("signature mismatch")
)

// SignURL generates an HMAC-SHA256 signature for the given path.
// path should be the URL path + query string that will be signed.
func SignURL(path string, keyHex, saltHex string, signatureSize int) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", errors.New("invalid HMAC key encoding (expected hex)")
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return "", errors.New("invalid HMAC salt encoding (expected hex)")
	}
	sig := computeSignature(path, key, salt, signatureSize)
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyURL verifies the HMAC-SHA256 signature for the given path.
// If keyHex is empty and required is false, verification is skipped.
func VerifyURL(signature, path string, keyHex, saltHex string, signatureSize int, required bool) error {
	if keyHex == "" || saltHex == "" {
		if required {
			return ErrNoSignatureConfig
		}
		return nil
	}

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return errors.New("server HMAC key misconfigured")
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return errors.New("server HMAC salt misconfigured")
	}

	if signature == "" {
		if required {
			return errors.New("missing signature")
		}
		return nil
	}

	messageMAC, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return ErrInvalidEncoding
	}

	expected := computeSignature(path, key, salt, signatureSize)
	if !hmac.Equal(messageMAC, expected) {
		return ErrSignatureMismatch
	}
	return nil
}

func computeSignature(str string, key, salt []byte, signatureSize int) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(salt)
	mac.Write([]byte(str))
	result := mac.Sum(nil)
	if signatureSize > 0 && signatureSize < 32 {
		return result[:signatureSize]
	}
	return result
}
