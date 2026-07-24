package social

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratePKCEPair(t *testing.T) {
	verifier, challenge, err := generatePKCEPair()
	require.NoError(t, err)
	require.NotEmpty(t, verifier)
	require.NotEmpty(t, challenge)

	// Verifier must be 43-128 chars per RFC 7636.
	require.GreaterOrEqual(t, len(verifier), 43)
	require.LessOrEqual(t, len(verifier), 128)

	// Challenge must equal S256(verifier) — base64url without padding.
	digest := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(digest[:])
	require.Equal(t, expected, challenge)
}

func TestGeneratePKCEPair_Uniqueness(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		v, _, err := generatePKCEPair()
		require.NoError(t, err)
		require.False(t, seen[v], "verifier collision at iter %d", i)
		seen[v] = true
	}
}
