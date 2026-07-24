package social

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/servekit/user-service/pkg/xcodes"
)

// generatePKCEPair returns a random RFC 7636 code_verifier and its S256
// code_challenge. The verifier is 64 raw bytes base64url-encoded (86 chars,
// within the 43-128 range). The challenge is base64url(sha256(verifier))
// without padding.
//
// user-service generates the pair at GetOAuthURL time, stores the verifier
// in the OAuth state entry, and threads the challenge into the provider's
// authorization URL. SocialLogin reads the verifier back from state and
// passes it to the provider's token exchange.
func generatePKCEPair() (verifier, challenge string, err error) {
	var raw [64]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", xcodes.ErrInternal.Wrapf(err, "read random bytes for PKCE verifier")
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	return verifier, challenge, nil
}
