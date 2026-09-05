package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	userv1 "github.com/servekit/api/gen/go/user/v1"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// generateTestKey generates a fresh P-256 ECDSA key and returns its PEM-encoded
// PKCS#8 form.
func generateTestKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// newTestProvider builds a Provider with a valid key and the supplied Config
// overrides applied. Returns the Provider directly — construction errors fail
// the test.
func newTestProvider(t *testing.T, overrides ...func(*Config)) *Provider {
	t.Helper()
	cfg := Config{
		ClientID:      "cid",
		TeamID:        "team",
		KeyID:         "key",
		RedirectURL:   "http://cb",
		PrivateKeyPEM: generateTestKey(t),
	}
	for _, o := range overrides {
		o(&cfg)
	}
	p, err := New(cfg)
	require.NoError(t, err)
	return p
}

// signIDTokenRS256 builds a real RS256-signed Apple-style ID token using the
// supplied key + kid, plus the standard Apple iss/aud/exp claims. Tests that
// exercise verifyIDToken must use this rather than ad-hoc unsigned tokens.
func signIDTokenRS256(t *testing.T, key *rsa.PrivateKey, kid, clientID, sub, email string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":              appleIssuer,
		"aud":              clientID,
		"sub":              sub,
		"email":            email,
		"email_verified":   "true",
		"is_private_email": "false",
		"iat":              time.Now().Unix(),
		"exp":              time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

// jwksHandler returns an http.HandlerFunc that serves a JWKS document
// containing the supplied public key under kid.
func jwksHandler(t *testing.T, key *rsa.PublicKey, kid string) http.HandlerFunc {
	t.Helper()
	nBytes := key.N.Bytes()
	eBig := big.NewInt(int64(key.E))
	eBytes := eBig.Bytes()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{
				{
					"kty": "RSA",
					"kid": kid,
					"use": "sig",
					"alg": "RS256",
					"n":   base64.RawURLEncoding.EncodeToString(nBytes),
					"e":   base64.RawURLEncoding.EncodeToString(eBytes),
				},
			},
		})
	}
}

// newRSAKey generates a fresh RSA key for test signing.
func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func TestProvider_New_Success(t *testing.T) {
	pemStr := generateTestKey(t)
	p, err := New(Config{
		ClientID:      "com.example.svc",
		TeamID:        "TEAM123",
		KeyID:         "KEY456",
		RedirectURL:   "http://cb",
		PrivateKeyPEM: pemStr,
	})
	require.NoError(t, err)
	require.NotNil(t, p)
	require.Equal(t, "com.example.svc", p.clientID)
	require.Equal(t, "TEAM123", p.teamID)
	require.Equal(t, "KEY456", p.keyID)
	require.Equal(t, "http://cb", p.redirectURL)
	require.NotNil(t, p.privateKey)
	require.Equal(t, defaultClientSecretTTL, p.clientSecretTTL)
}

func TestProvider_New_EmptyKey(t *testing.T) {
	_, err := New(Config{PrivateKeyPEM: ""})
	require.Error(t, err)
}

func TestProvider_New_InvalidPEM(t *testing.T) {
	_, err := New(Config{PrivateKeyPEM: "not a pem"})
	require.Error(t, err)
}

func TestProvider_New_NonECDSAKey(t *testing.T) {
	// RSA key in PKCS#8 should be rejected.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	require.NoError(t, err)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	_, err = New(Config{PrivateKeyPEM: pemStr})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not ECDSA")
}

func TestProvider_New_CustomTTL(t *testing.T) {
	p := newTestProvider(t, func(c *Config) { c.ClientSecretTTL = 5 * time.Minute })
	require.Equal(t, 5*time.Minute, p.clientSecretTTL)
}

func TestProvider_New_ZeroTTLFallsBackToDefault(t *testing.T) {
	p := newTestProvider(t) // no override → default
	require.Equal(t, defaultClientSecretTTL, p.clientSecretTTL)
}

func TestProvider_Provider(t *testing.T) {
	p := newTestProvider(t)
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_APPLE, p.Provider())
}

func TestProvider_GetAuthURL(t *testing.T) {
	p := newTestProvider(t, func(c *Config) {
		c.ClientID = "com.example"
		c.RedirectURL = "http://default/cb"
	})

	rawURL, err := p.GetAuthURL(context.Background(), "", "apple-state", "")
	require.NoError(t, err)
	require.Contains(t, rawURL, "https://appleid.apple.com/auth/authorize?")
	require.Contains(t, rawURL, "client_id=com.example")
	require.Contains(t, rawURL, "redirect_uri=http%3A%2F%2Fdefault%2Fcb")
	require.Contains(t, rawURL, "response_type=code+id_token")
	require.Contains(t, rawURL, "scope=email")
	require.Contains(t, rawURL, "state=apple-state")
	require.Contains(t, rawURL, "response_mode=form_post")
	require.NotContains(t, rawURL, "code_challenge")
}

func TestProvider_GetAuthURL_PKCE(t *testing.T) {
	p := newTestProvider(t, func(c *Config) {
		c.ClientID = "com.example"
		c.RedirectURL = "http://default/cb"
	})

	rawURL, err := p.GetAuthURL(context.Background(), "", "st", "challenge-xyz")
	require.NoError(t, err)
	require.Contains(t, rawURL, "code_challenge=challenge-xyz")
	require.Contains(t, rawURL, "code_challenge_method=S256")
}

func TestProvider_GetAuthURL_OverrideRedirect(t *testing.T) {
	p := newTestProvider(t, func(c *Config) { c.RedirectURL = "http://default" })
	rawURL, err := p.GetAuthURL(context.Background(), "http://override", "st", "")
	require.NoError(t, err)
	require.Contains(t, rawURL, "redirect_uri=http%3A%2F%2Foverride")
}

func TestProvider_ExchangeCode_Success(t *testing.T) {
	// Sign a real RS256 id_token with a test key + kid; the JWKS handler
	// publishes the matching public key so verifyIDToken can validate it.
	signingKey := newRSAKey(t)
	const kid = "SIGNER123"
	idToken := signIDTokenRS256(t, signingKey, kid, "com.example", "apple-uid-123", "user@privaterelay.appleid.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		require.NoError(t, r.ParseForm())
		require.Equal(t, "authorization_code", r.PostForm.Get("grant_type"))
		require.Equal(t, "the-code", r.PostForm.Get("code"))
		require.Equal(t, "com.example", r.PostForm.Get("client_id"))
		require.Equal(t, "http://cb", r.PostForm.Get("redirect_uri"))
		// client_secret must be a non-empty JWT with 3 segments.
		secret := r.PostForm.Get("client_secret")
		require.NotEmpty(t, secret)
		require.Len(t, strings.Split(secret, "."), 3)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "apple-access-tok",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      idToken,
			"refresh_token": "apple-refresh",
		})
	})
	mux.HandleFunc("/auth/keys", jwksHandler(t, &signingKey.PublicKey, kid))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestProvider(t, func(c *Config) { c.ClientID = "com.example" })
	p.tokenURL = srv.URL + "/auth/token"
	p.jwksURL = srv.URL + "/auth/keys"

	result, err := p.ExchangeCode(context.Background(), "the-code", "")
	require.NoError(t, err)
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_APPLE, result.Provider)
	require.Equal(t, "apple-uid-123", result.ProviderUID)
	require.Equal(t, "user@privaterelay.appleid.com", result.Email)
	require.Equal(t, "apple-access-tok", result.AccessToken)
}

func TestProvider_ExchangeCode_PKCEVerifies(t *testing.T) {
	// Sign a real RS256 id_token with a test key + kid; the JWKS handler
	// publishes the matching public key so verifyIDToken can validate it.
	signingKey := newRSAKey(t)
	const kid = "SIGNER456"
	idToken := signIDTokenRS256(t, signingKey, kid, "com.example", "apple-uid-pkce", "pkce@privaterelay.appleid.com")

	var capturedVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		capturedVerifier = r.PostForm.Get("code_verifier")
		require.Equal(t, "verifier-xyz", capturedVerifier)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
			"id_token":     idToken,
		})
	})
	mux.HandleFunc("/auth/keys", jwksHandler(t, &signingKey.PublicKey, kid))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestProvider(t, func(c *Config) { c.ClientID = "com.example" })
	p.tokenURL = srv.URL + "/auth/token"
	p.jwksURL = srv.URL + "/auth/keys"

	result, err := p.ExchangeCode(context.Background(), "the-code", "verifier-xyz")
	require.NoError(t, err)
	require.Equal(t, "verifier-xyz", capturedVerifier)
	require.Equal(t, "apple-uid-pkce", result.ProviderUID)
}

func TestProvider_ExchangeCode_TokenEndpointError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.tokenURL = srv.URL

	_, err := p.ExchangeCode(context.Background(), "bad-code", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "400")
}

func TestProvider_ExchangeCode_EmptyIDToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
		})
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.tokenURL = srv.URL

	_, err := p.ExchangeCode(context.Background(), "code", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty id_token")
}

func TestProvider_ExchangeCode_MalformedIDToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token": "not.a.jwt",
		})
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.tokenURL = srv.URL

	_, err := p.ExchangeCode(context.Background(), "code", "")
	require.Error(t, err)
}

func TestGenerateClientSecret_Format(t *testing.T) {
	p := newTestProvider(t, func(c *Config) {
		c.ClientID = "com.example"
		c.TeamID = "TEAM123"
		c.KeyID = "KEY456"
	})

	secret, err := p.generateClientSecret()
	require.NoError(t, err)
	parts := strings.Split(secret, ".")
	require.Len(t, parts, 3)

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header jwtHeader
	require.NoError(t, json.Unmarshal(headerJSON, &header))
	require.Equal(t, jwtHeader{Alg: "ES256", Kid: "KEY456", Typ: "JWT"}, header)

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims clientSecretClaims
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	require.Equal(t, "TEAM123", claims.Iss)
	require.Equal(t, "https://appleid.apple.com", claims.Aud)
	require.Equal(t, "com.example", claims.Sub)
	require.Greater(t, claims.Exp, claims.Iat)
	require.InDelta(t, defaultClientSecretTTL.Seconds(), float64(claims.Exp-claims.Iat), 1)

	// Signature must be 64 raw bytes (P-256 r||s).
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	require.Len(t, sig, 64)
}
