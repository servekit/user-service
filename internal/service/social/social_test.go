package social

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/servekit/go-common/redisx"

	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/identity"
	"github.com/servekit/user-service/pkg/config"
)

// TestValidateReturnTo_NilProviderConfig verifies validateReturnTo does not
// panic when oauth is non-nil but the specific provider block is nil. This
// was a nil-deref bug — oauth.GitHub.AllowedRedirectURLs crashed if the
// operator left GitHub unconfigured.
func TestValidateReturnTo_NilProviderConfig(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: nil, // explicitly unset
	}
	err := validateReturnTo(cfg, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://app.example.com/done")
	require.NoError(t, err, "nil provider block should be treated as 'no allowlist configured'")
}

// TestValidateReturnTo_DefaultDeny verifies that an empty allowlist rejects
// non-empty return_to — secure-by-default. Operator must set
// AllowArbitraryRedirectURLs=true to opt back into the legacy allow-any
// behavior (intended for dev/staging only).
func TestValidateReturnTo_DefaultDeny(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			AllowedRedirectURLs: nil, // empty
			// AllowArbitraryRedirectURLs defaults to false
		},
	}
	err := validateReturnTo(cfg, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://app.example.com/done")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in the allowlist")
}

// TestValidateReturnTo_OptInArbitrary verifies the escape hatch works.
func TestValidateReturnTo_OptInArbitrary(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			AllowedRedirectURLs:        nil,
			AllowArbitraryRedirectURLs: true,
		},
	}
	err := validateReturnTo(cfg, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://evil.example.com/")
	require.NoError(t, err)
}

// TestValidateReturnTo_EmptyReturnToAlwaysAllowed verifies the "no redirect
// needed" case still works regardless of allowlist state.
func TestValidateReturnTo_EmptyReturnToAlwaysAllowed(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{}, // empty allowlist, default deny
	}
	err := validateReturnTo(cfg, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "")
	require.NoError(t, err)
}

// TestValidateReturnTo_NilOAuthConfig verifies that a nil cfg.OAuth
// (no OAuth configured at all) rejects non-empty return_to. Defense-in-
// depth: without this, a future caller that constructs the service
// without cfg.OAuth (e.g. a module-mode misuse) would silently bypass
// the entire return_to check.
func TestValidateReturnTo_NilOAuthConfig(t *testing.T) {
	err := validateReturnTo(nil, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://app.example.com/done")
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth config is nil")
}

// TestValidateReturnToURL_RejectsDangerousSchemes verifies return_to is
// rejected when scheme is a known code-execution / local-resource scheme.
// Stops "javascript:alert(document.cookie)" / "data:" / "file:" XSS via
// return_to. Other custom schemes (mobile deep links like "myapp://...")
// are allowed — defense-in-depth uses a denylist, not an allowlist.
func TestValidateReturnToURL_RejectsDangerousSchemes(t *testing.T) {
	cases := []string{
		"javascript:alert(1)",
		"JavaScript:alert(1)", // case-insensitive
		"data:text/html,<script>alert(1)</script>",
		"file:///etc/passwd",
		"vbscript:msgbox",
		"about:blank",
		"blob:https://example.com/abc",
		"intent://anything#Intent;package=com.evil;end", // Android intent injection
		"content://com.android.contacts/data/1",         // Android content provider
	}
	for _, tc := range cases {
		err := validateReturnToURL(tc)
		require.Error(t, err, "scheme must be rejected for %q", tc)
	}
}

// TestValidateReturnToURL_RejectsCRLF verifies CR/LF in return_to is
// rejected. Defense against response-splitting if the value reaches a
// Location header in the callback service (out of user-service's direct
// control, but reject here as defense-in-depth).
func TestValidateReturnToURL_RejectsCRLF(t *testing.T) {
	cases := []string{
		"https://app.example.com/\r\nSet-Cookie: evil=1",
		"https://app.example.com/\nLocation: https://evil.com",
		"https://app.example.com/\rX-Inject: yes",
	}
	for _, tc := range cases {
		err := validateReturnToURL(tc)
		require.Error(t, err, "CR/LF must be rejected: %q", tc)
		require.Contains(t, err.Error(), "CR/LF")
	}
}

// TestValidateReturnToURL_AcceptsHTTPHTTPS verifies the legitimate
// http/https case (the most common production setup).
func TestValidateReturnToURL_AcceptsHTTPHTTPS(t *testing.T) {
	cases := []string{
		"http://localhost:3000/done",
		"https://app.example.com/auth/done",
		"https://app.example.com/auth/done?next=dashboard",
		"https://app.example.com/auth/done#fragment",
	}
	for _, tc := range cases {
		err := validateReturnToURL(tc)
		require.NoError(t, err, "valid URL rejected: %q", tc)
	}
}

// TestValidateReturnToURL_AcceptsCustomSchemes verifies that mobile deep
// link schemes (myapp://, com.example.app://) PASS scheme validation.
// Custom schemes are how iOS / Android apps receive OAuth callbacks
// outside the browser. They are caught by the exact-match allowlist
// later, not by the scheme check — operators put "myapp://callback" in
// AllowedRedirectURLs explicitly.
func TestValidateReturnToURL_AcceptsCustomSchemes(t *testing.T) {
	cases := []string{
		"myapp://callback",
		"myapp://oauth/callback",
		"com.example.app://oauth",
		"wechatmobile://callback",
	}
	for _, tc := range cases {
		err := validateReturnToURL(tc)
		require.NoError(t, err, "custom scheme rejected: %q", tc)
	}
}

// TestValidateReturnToURL_RejectsUserinfo verifies return_to with userinfo
// is rejected — common phishing pattern. Even if allowlist contained the
// exact string, the URL "https://victim.com@evil.com/" must not pass.
func TestValidateReturnToURL_RejectsUserinfo(t *testing.T) {
	err := validateReturnToURL("https://victim.com@evil.com/")
	require.Error(t, err)
	require.Contains(t, err.Error(), "userinfo")
}

// TestValidateReturnToURL_RejectsBackslash verifies backslash in the URL
// is rejected. URL parsers disagree on what "https://app.com\@evil.com"
// means — Chrome treats \ as part of host, Go net/url treats it as path.
// Reject to avoid parser-confusion attacks.
func TestValidateReturnToURL_RejectsBackslash(t *testing.T) {
	err := validateReturnToURL("https://app.example.com\\@evil.com/")
	require.Error(t, err)
	require.Contains(t, err.Error(), "backslash")
}

// TestValidateReturnToURL_RequiresAbsoluteURL verifies that relative URLs
// and scheme-relative URLs are rejected. user-service does not guess the
// base URL — BFF must resolve relatives before calling GetOAuthURL.
func TestValidateReturnToURL_RequiresAbsoluteURL(t *testing.T) {
	cases := []string{
		"/auth/done",         // relative path
		"auth/done",          // relative no leading slash
		"//app.example.com/", // scheme-relative (no scheme)
		"#fragment",          // fragment only
	}
	for _, tc := range cases {
		err := validateReturnToURL(tc)
		require.Error(t, err, "must reject non-absolute URL: %q", tc)
	}
}

// TestValidateReturnToURL_EmptyStringOK verifies that empty return_to is
// allowed — means "no redirect needed" (module mode / direct callback
// pattern where the embedding app handles its own post-login routing).
func TestValidateReturnToURL_EmptyStringOK(t *testing.T) {
	err := validateReturnToURL("")
	require.NoError(t, err)
}

// TestValidateOAuthConfig_RejectsBadAllowlistEntry verifies that bad URLs
// in the allowlist cause startup to fail (validateOAuthConfig returns the
// error). Catches operator typos like "javascript:..." or relative paths
// in config.
func TestValidateOAuthConfig_RejectsBadAllowlistEntry(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			ClientID:            "github-client-id", // configured → must validate
			RedirectURL:         "https://callback.example.com/cb",
			AllowedRedirectURLs: []string{"javascript:alert(1)"},
		},
	}
	errs, _ := validateOAuthConfig(cfg)
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0], "javascript")
}

// TestValidateOAuthConfig_AcceptsCustomSchemeAllowlistEntry verifies that
// mobile deep link schemes in the allowlist are accepted at startup. The
// operator must explicitly list "myapp://callback" — the denylist only
// blocks known-dangerous schemes.
func TestValidateOAuthConfig_AcceptsCustomSchemeAllowlistEntry(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			ClientID:            "github-client-id", // configured → exercises allowlist validation
			RedirectURL:         "https://callback.example.com/cb",
			AllowedRedirectURLs: []string{"myapp://oauth/callback"},
		},
	}
	errs, _ := validateOAuthConfig(cfg)
	require.Empty(t, errs, "custom scheme allowlist entry should be accepted")
}

// TestValidateOAuthConfig_SkipsEmptyCredsProviders verifies that provider
// blocks present in config but WITHOUT credentials (e.g. a placeholder
// &OAuthGitHubConfig{}) are treated as not-configured and skipped — no
// "redirect_url is required" error. Lets embedders boot with a minimal config
// (nil or empty-creds providers) without a caller-side NormalizeConfig.
func TestValidateOAuthConfig_SkipsEmptyCredsProviders(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{}, // non-nil, empty creds → skipped
		Google: &config.OAuthGoogleConfig{},
		WeChat: &config.OAuthWeChatConfig{},
		Apple:  &config.OAuthAppleConfig{},
	}
	errs, _ := validateOAuthConfig(cfg)
	require.Empty(t, errs, "empty-creds provider blocks should be skipped, not validated")
}

// TestValidateOAuthConfig_FailFastOnConfiguredBadRedirect verifies that a
// provider WITH credentials but an empty/malformed redirect_url still errors
// at startup. Production fail-fast is preserved: misconfigured providers are
// NOT silently skipped.
func TestValidateOAuthConfig_FailFastOnConfiguredBadRedirect(t *testing.T) {
	cfg := &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			ClientID:    "github-client-id", // configured → must validate
			RedirectURL: "",                 // bad → must error
		},
	}
	errs, _ := validateOAuthConfig(cfg)
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0], "redirect_url is required")
}

// newTestService spins up a social.Service backed by miniredis for state
// store tests. Returns the service and a cleanup func.
func newTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	rdb := redisx.NewTestClient(t)
	svc := &Service{
		rdb:   rdb,
		oauth: &config.OAuthConfig{}, // empty config; tests configure per-case
	}
	return svc, func() { _ = rdb.Close() }
}

// TestStoreCallerState_RejectsExisting verifies that a caller-supplied state
// cannot overwrite an existing state entry. Defense against the
// state-overwrite attack: attacker observes victim's UUID state and races
// to overwrite its return_to with attacker-controlled URL.
func TestStoreCallerState_RejectsExisting(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Victim issues a state (server-side via issueState).
	victimState, err := svc.issueState(ctx, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://victim.example.com/done", "")
	require.NoError(t, err)
	require.NotEmpty(t, victimState)

	// Attacker tries to overwrite the same key with their own return_to.
	err = svc.storeCallerState(ctx, victimState, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://evil.example.com/", "")
	require.Error(t, err, "storeCallerState must reject when key already exists")
	require.Contains(t, err.Error(), "state already exists")

	// Verify the original entry is intact.
	entry, err := svc.consumeState(ctx, victimState)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Equal(t, "https://victim.example.com/done", entry.ReturnTo, "attacker must not have overwritten victim's return_to")
}

// TestStoreCallerState_HappyPath verifies the success path: first write of
// a caller-supplied state succeeds, the key is persisted with the right TTL
// window, and the payload round-trips through consumeState with the
// provider and return_to intact. Without this test, a regression that
// silently drops the payload (e.g. wrong Redis key prefix, marshal bug)
// would only surface via downstream integration tests.
func TestStoreCallerState_HappyPath(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()
	state := "caller-supplied-state-uuid-abc"

	err := svc.storeCallerState(ctx, state,
		pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE,
		"https://app.example.com/done", "")
	require.NoError(t, err)

	entry, err := svc.consumeState(ctx, state)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Equal(t, pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE.String(), entry.Provider)
	require.Equal(t, "https://app.example.com/done", entry.ReturnTo)
	require.NotZero(t, entry.CreatedAt)
}

// TestSocialLogin_RevalidatesReturnTo verifies that even when a state entry
// holds a return_to that would currently be rejected by validateReturnTo
// (e.g. operator tightened allowlist between GetOAuthURL and SocialLogin),
// SocialLogin refuses to echo it back. Defense-in-depth: state's stored
// return_to is not trusted blindly.
func TestSocialLogin_RevalidatesReturnTo(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	// Configure: allowlist only permits victim URLs.
	svc.oauth = &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			AllowedRedirectURLs: []string{"https://app.example.com/done"},
		},
	}

	ctx := context.Background()

	// Simulate a state that was issued when allowlist was looser (or via the
	// storeCallerState-overwrite bug fixed in A4, or any future bug). Its
	// stored return_to would NOT pass current validation.
	err := svc.rdb.Set(ctx, "oauth:state:stale-state", mustMarshal(t, &oauthStateEntry{
		Provider:  "IDENTITY_PROVIDER_GITHUB",
		ReturnTo:  "https://evil.example.com/", // not in current allowlist
		CreatedAt: time.Now().Unix(),
	}), oauthStateTTL).Err()
	require.NoError(t, err)

	// SocialLogin should reject — even though the state entry exists and
	// matches provider. We don't actually exchange code here (no provider
	// configured); we expect rejection BEFORE ExchangeCode is reached.
	_, err = svc.SocialLogin(ctx, &pb.SocialLoginRequest{
		Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
		Code:     "fake-code", // won't be used — return_to check fires first
		State:    "stale-state",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "return_to")
}

// mustMarshal is a tiny helper for tests that need to write oauthStateEntry
// directly to Redis.
func mustMarshal(t *testing.T, e *oauthStateEntry) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	require.NoError(t, err)
	return b
}

// mockProvider is a minimal RedirectProvider for integration tests. It
// returns canned URLs and ExchangeCode results without hitting any network.
// POST-B signature (GetAuthURL 4-arg, ExchangeCode 3-arg) — matches the
// Phase B interface changes.
type mockProvider struct {
	provider    pb.IdentityProvider
	authURL     string
	exchangeOut *identity.SocialResult
	exchangeErr error
}

func (m *mockProvider) Provider() pb.IdentityProvider { return m.provider }
func (m *mockProvider) GetAuthURL(_ context.Context, _, _, _ string) (string, error) {
	return m.authURL, nil
}
func (m *mockProvider) ExchangeCode(_ context.Context, _, _ string) (*identity.SocialResult, error) {
	return m.exchangeOut, m.exchangeErr
}

// TestGetOAuthURL_DefaultDenyEndToEnd verifies the full path: GetOAuthURL
// with a disallowed return_to fails before any state is written to Redis.
// Locks in the Phase A fixes (default-deny allowlist + state-store ordering):
// validateReturnTo runs before issueState/storeCallerState, so a rejected
// return_to leaves no Redis footprint.
func TestGetOAuthURL_DefaultDenyEndToEnd(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	svc.oauth = &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			AllowedRedirectURLs: []string{"https://app.example.com/done"},
		},
	}
	svc.socialProviders = map[pb.IdentityProvider]identity.SocialProvider{
		pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: &mockProvider{
			provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
			authURL:  "https://github.com/login/oauth/authorize?...",
		},
	}

	ctx := context.Background()
	_, err := svc.GetOAuthURL(ctx, &pb.GetOAuthURLRequest{
		Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
		ReturnTo: "https://evil.example.com/",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in the allowlist")

	// Verify no state was written.
	keys, err := svc.rdb.Keys(ctx, "oauth:state:*").Result()
	require.NoError(t, err)
	require.Empty(t, keys, "no state should be persisted when return_to is rejected")
}

// TestGetOAuthURL_PKCEVerifierStored verifies that GetOAuthURL generates a
// PKCE pair, stores the verifier in the state entry, and passes the S256
// challenge to the provider's GetAuthURL.
func TestGetOAuthURL_PKCEVerifierStored(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	var seenChallenge string
	svc.oauth = &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			RedirectURL:         "http://localhost/cb",
			AllowedRedirectURLs: []string{"https://app.example.com/done"},
		},
	}
	svc.socialProviders = map[pb.IdentityProvider]identity.SocialProvider{
		pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: &pkceCapturingProvider{
			seenChallenge: &seenChallenge,
		},
	}

	ctx := context.Background()
	resp, err := svc.GetOAuthURL(ctx, &pb.GetOAuthURLRequest{
		Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
		ReturnTo: "https://app.example.com/done",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.State)
	require.NotEmpty(t, seenChallenge, "provider must have received a code_challenge")

	// Verify state entry holds the verifier.
	raw, err := svc.rdb.Get(ctx, "oauth:state:"+resp.State).Bytes()
	require.NoError(t, err)
	var entry oauthStateEntry
	require.NoError(t, json.Unmarshal(raw, &entry))
	require.NotEmpty(t, entry.CodeVerifier, "state entry must hold PKCE verifier")

	// Challenge must equal S256(verifier).
	digest := sha256.Sum256([]byte(entry.CodeVerifier))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(digest[:]), seenChallenge)
}

type pkceCapturingProvider struct {
	seenChallenge *string
}

func (p *pkceCapturingProvider) Provider() pb.IdentityProvider {
	return pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB
}
func (p *pkceCapturingProvider) GetAuthURL(_ context.Context, _, _, codeChallenge string) (string, error) {
	*p.seenChallenge = codeChallenge
	return "https://github.com/login/oauth/authorize?state=x", nil
}
func (p *pkceCapturingProvider) ExchangeCode(_ context.Context, _, _ string) (*identity.SocialResult, error) {
	return &identity.SocialResult{}, nil
}
