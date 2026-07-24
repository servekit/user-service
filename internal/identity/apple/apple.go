// Package apple implements the Apple Sign-In social login provider.
//
// Apple requires a JWT client_secret signed with the ES256 algorithm using
// a P-256 ECDSA private key issued from the Apple developer portal. The
// client_secret is regenerated on every code exchange so we never need to
// cache or rotate it ourselves.
package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	userv1 "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/identity"
	"github.com/servekit/user-service/internal/utils/httpx"
	"github.com/servekit/user-service/pkg/xcodes"

	"github.com/golang-jwt/jwt/v5"
)

const (
	appleAuthURL  = "https://appleid.apple.com/auth/authorize"
	appleTokenURL = "https://appleid.apple.com/auth/token"
	appleJWKSURL  = "https://appleid.apple.com/auth/keys"
	appleAudience = "https://appleid.apple.com"
	appleIssuer   = "https://appleid.apple.com"
	jwksCacheTTL  = 15 * time.Minute // Apple rotates keys infrequently; 15 min balances freshness vs load
	// defaultClientSecretTTL is used when Config.ClientSecretTTL is unset.
	// Apple allows up to 6 months; 30 minutes keeps the window narrow without
	// burdening operators with frequent rotation.
	defaultClientSecretTTL = 30 * time.Minute
	p256CoordBytes         = 32 // P-256 scalar length in bytes
)

// Config holds Apple Sign-In provider settings.
type Config struct {
	ClientID        string
	TeamID          string
	KeyID           string
	RedirectURL     string
	PrivateKeyPEM   string        // PEM-encoded PKCS#8 (.p8) private key
	ClientSecretTTL time.Duration // 0 means defaultClientSecretTTL
}

// Provider handles Apple Sign-In authentication.
type Provider struct {
	clientID        string
	teamID          string
	keyID           string
	redirectURL     string
	privateKey      *ecdsa.PrivateKey
	clientSecretTTL time.Duration
	// tokenURL / authURL / jwksURL override the defaults when non-empty
	// (for tests).
	tokenURL string
	authURL  string
	jwksURL  string
	// jwksCache holds Apple's rotating JWKS, refreshed on demand. The cache
	// amortizes the JWKS fetch across verifications; the TTL bounds staleness
	// if Apple rotates a key out-of-band.
	jwksCache *appleJWKSCache
}

// New creates a new Apple Provider from the supplied Config. Returns an
// error if the private key cannot be parsed or is not a P-256 ECDSA key.
func New(cfg Config) (*Provider, error) {
	key, err := parseP256PrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	ttl := cfg.ClientSecretTTL
	if ttl <= 0 {
		ttl = defaultClientSecretTTL
	}
	return &Provider{
		clientID:        cfg.ClientID,
		teamID:          cfg.TeamID,
		keyID:           cfg.KeyID,
		redirectURL:     cfg.RedirectURL,
		privateKey:      key,
		clientSecretTTL: ttl,
		jwksCache:       &appleJWKSCache{ttl: jwksCacheTTL},
	}, nil
}

// Provider returns the identity provider enum value.
func (Provider) Provider() userv1.IdentityProvider {
	return userv1.IdentityProvider_IDENTITY_PROVIDER_APPLE
}

// GetAuthURL returns the Apple authorization URL. Apple requires form_post
// response_mode when the response_type includes id_token, which we request
// so the callback delivers both code and id_token in one round trip.
// codeChallenge, when non-empty, adds PKCE code_challenge + S256 method to
// the authorization URL (Apple has supported PKCE since 2022).
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state, codeChallenge string) (string, error) {
	ru := redirectURL
	if ru == "" {
		ru = p.redirectURL
	}
	authURL := p.authURL
	if authURL == "" {
		authURL = appleAuthURL
	}
	q := url.Values{}
	q.Set("client_id", p.clientID)
	q.Set("redirect_uri", ru)
	q.Set("response_type", "code id_token")
	q.Set("scope", "email")
	q.Set("state", state)
	q.Set("response_mode", "form_post")
	if codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	return authURL + "?" + q.Encode(), nil
}

// ExchangeCode exchanges the authorization code at Apple's token endpoint
// and decodes the returned ID token to extract the user identifier.
// codeVerifier, when non-empty, is sent as code_verifier for PKCE.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	clientSecret, err := p.generateClientSecret()
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	tokenURL := p.tokenURL
	if tokenURL == "" {
		tokenURL = appleTokenURL
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", p.clientID)
	form.Set("client_secret", clientSecret)
	form.Set("redirect_uri", p.redirectURL)
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpx.Default.Do(req)
	if err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}
	defer resp.Body.Close()
	if err := httpx.CheckStatus(resp); err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}
	if tokenResp.IDToken == "" {
		return nil, xcodes.ErrAppleTokenInvalid.New("empty id_token in token response")
	}

	claims, err := p.verifyIDToken(ctx, tokenResp.IDToken)
	if err != nil {
		return nil, xcodes.ErrAppleTokenInvalid.Wrap(err)
	}

	return &identity.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_APPLE,
		ProviderUID: claims.Sub,
		Email:       claims.Email,
		AccessToken: tokenResp.AccessToken,
	}, nil
}

// --- internal helpers ---

// tokenResponse is the JSON body returned by Apple's token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// jwtHeader is the JOSE header we write into the client_secret JWT.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// clientSecretClaims is the payload of the client_secret JWT. Field names
// follow RFC 7519 (iss/iat/exp/aud/sub).
type clientSecretClaims struct {
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Aud string `json:"aud"`
	Sub string `json:"sub"`
}

// idTokenClaims holds the ID-token payload fields we read.
type idTokenClaims struct {
	Sub            string `json:"sub"`
	Email          string `json:"email"`
	EmailVerified  string `json:"email_verified"`
	IsPrivateEmail string `json:"is_private_email"`
}

// decodeIDToken extracts the payload of an Apple ID token without any
// cryptographic verification. Used only by tests; production callers must
// use verifyIDToken, which checks signature, iss, aud, and exp.
func decodeIDToken(idToken string) (*idTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, xcodes.ErrInternal.New("invalid JWT format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, xcodes.ErrInternal.Wrapf(err, "decode JWT payload")
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, xcodes.ErrInternal.Wrapf(err, "parse JWT claims")
	}
	if claims.Sub == "" {
		return nil, xcodes.ErrInternal.New("id_token missing sub claim")
	}
	return &claims, nil
}

// verifyIDToken validates an Apple ID token: signature against Apple's JWKS,
// issuer, audience (our client_id), and expiry. Defense-in-depth — the token
// was just returned from Apple's token endpoint over TLS, but verifying the
// claims protects us against misconfigured endpoints, log leaks, and replay
// of stale tokens.
func (p *Provider) verifyIDToken(ctx context.Context, idToken string) (*idTokenClaims, error) {
	// First parse unverified to extract sub + header kid. The sub sanity
	// check at the end ties the verified parse back to this preview.
	unverified, err := decodeIDToken(idToken)
	if err != nil {
		return nil, err
	}

	// Fetch Apple's JWKS (cached).
	jwks, err := p.jwksCache.get(ctx, p.jwksURL)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrapf(err, "fetch apple jwks")
	}

	// Parse + verify. jwt.Keyfunc returns the verification key for the token's
	// kid; returning an error from it aborts verification.
	token, err := jwt.ParseWithClaims(idToken, jwt.MapClaims{}, func(t *jwt.Token) (any, error) {
		// Apple signs with RS256.
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, xcodes.ErrInternal.New(fmt.Sprintf("unexpected signing method %v", t.Header["alg"]))
		}
		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, xcodes.ErrInternal.New("id_token header missing kid")
		}
		key, ok := jwks.keyByID(kid)
		if !ok {
			return nil, xcodes.ErrInternal.New(fmt.Sprintf("no jwks key for kid %q", kid))
		}
		return key, nil
	},
		jwt.WithIssuer(appleIssuer),
		jwt.WithAudience(p.clientID),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrapf(err, "verify id_token")
	}
	if !token.Valid {
		return nil, xcodes.ErrInternal.New("id_token not valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, xcodes.ErrInternal.New("id_token claims have unexpected type")
	}
	out := &idTokenClaims{}
	if v, ok := claims["sub"].(string); ok {
		out.Sub = v
	}
	if v, ok := claims["email"].(string); ok {
		out.Email = v
	}
	if v, ok := claims["email_verified"].(string); ok {
		out.EmailVerified = v
	}
	if v, ok := claims["is_private_email"].(string); ok {
		out.IsPrivateEmail = v
	}
	if out.Sub == "" {
		return nil, xcodes.ErrInternal.New("id_token missing sub claim")
	}
	// Sanity: the verified sub must match what the unverified decode returned.
	if unverified.Sub != out.Sub {
		return nil, xcodes.ErrInternal.New("id_token sub claim mismatch")
	}
	return out, nil
}

// generateClientSecret builds the ES256-signed JWT that Apple requires as
// client_secret on the token endpoint. The signature is the raw r||s
// concatenation (JOSE / IEEE P1363 format), not ASN.1 DER.
func (p *Provider) generateClientSecret() (string, error) {
	now := time.Now()
	header := jwtHeader{Alg: "ES256", Kid: p.keyID, Typ: "JWT"}
	claims := clientSecretClaims{
		Iss: p.teamID,
		Iat: now.Unix(),
		Exp: now.Add(p.clientSecretTTL).Unix(),
		Aud: appleAudience,
		Sub: p.clientID,
	}
	headerB, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsB, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerB) + "." +
		base64.RawURLEncoding.EncodeToString(claimsB)

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, p.privateKey, digest[:])
	if err != nil {
		return "", xcodes.ErrInternal.Wrapf(err, "sign client_secret")
	}
	sig := make([]byte, 2*p256CoordBytes)
	r.FillBytes(sig[0:p256CoordBytes])
	s.FillBytes(sig[p256CoordBytes : 2*p256CoordBytes])

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseP256PrivateKey parses a PEM-encoded PKCS#8 P-256 ECDSA private key
// (the .p8 format issued by the Apple developer portal).
func parseP256PrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	if strings.TrimSpace(pemStr) == "" {
		return nil, xcodes.ErrInternal.New("empty private key")
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, xcodes.ErrInternal.New("no PEM block in private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrapf(err, "parse PKCS#8")
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, xcodes.ErrInternal.New(fmt.Sprintf("key is not ECDSA (got %T)", key))
	}
	if ecKey.Curve != elliptic.P256() {
		return nil, xcodes.ErrInternal.New("key curve is not P-256")
	}
	return ecKey, nil
}

// appleJWKSCache memoizes Apple's JWKS for jwksCacheTTL. The cache is safe
// for concurrent use; the first call after expiry triggers a single fetch.
type appleJWKSCache struct {
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey // keyed by kid
	fetched time.Time
	ttl     time.Duration
}

// jwkJSON is the relevant subset of an Apple JWKS entry.
type jwkJSON struct {
	KTY string `json:"kty"`
	KID string `json:"kid"`
	USE string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// jwksSetJSON is the relevant subset of Apple's JWKS document.
type jwksSetJSON struct {
	Keys []jwkJSON `json:"keys"`
}

// get returns the cached JWKS, refreshing when stale. The fetch is
// serialized under the write lock so concurrent callers don't duplicate work.
func (c *appleJWKSCache) get(ctx context.Context, override string) (*appleJWKSSet, error) {
	c.mu.RLock()
	if c.fresh() && len(c.keys) > 0 {
		keys := c.keys
		c.mu.RUnlock()
		return &appleJWKSSet{keys: keys}, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check under write lock — another goroutine may have refreshed.
	if c.fresh() && len(c.keys) > 0 {
		return &appleJWKSSet{keys: c.keys}, nil
	}

	jwksURL := appleJWKSURL
	if override != "" {
		jwksURL = override
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := httpx.Default.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := httpx.CheckStatus(resp); err != nil {
		return nil, err
	}
	var set jwksSetJSON
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, xcodes.ErrInternal.Wrapf(err, "decode jwks")
	}

	parsed := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, jwk := range set.Keys {
		if jwk.KTY != "RSA" || jwk.USE != "sig" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil {
			return nil, xcodes.ErrInternal.Wrapf(err, "decode jwk n for kid %s", jwk.KID)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil {
			return nil, xcodes.ErrInternal.Wrapf(err, "decode jwk e for kid %s", jwk.KID)
		}
		var exp int
		for _, b := range eBytes {
			exp = exp<<8 + int(b)
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: exp}
		parsed[jwk.KID] = pub
	}
	if len(parsed) == 0 {
		return nil, xcodes.ErrInternal.New("jwks contained no usable RSA signing keys")
	}
	c.keys = parsed
	c.fetched = time.Now()
	return &appleJWKSSet{keys: parsed}, nil
}

func (c *appleJWKSCache) fresh() bool {
	return time.Since(c.fetched) < c.ttl
}

// appleJWKSSet is a snapshot of the JWKS handed out by the cache.
type appleJWKSSet struct {
	keys map[string]*rsa.PublicKey
}

func (s *appleJWKSSet) keyByID(kid string) (*rsa.PublicKey, bool) {
	k, ok := s.keys[kid]
	return k, ok
}
