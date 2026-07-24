# OAuth Security Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the "fake parameter redirect" attack surface on the OAuth flow — `return_to` allowlist default, `return_to` URL scheme/host validation, state overwrite, missing return_to re-validation, missing PKCE, and session_id leakage via redirect URL — and document the BFF-side state↔cookie binding contract.

**Architecture:** user-service stays gRPC-only and stays unauthenticated (per prior decision). Hardening happens at four layers: (1) input validation (`validateReturnTo` defaults + URL scheme/host/userinfo check, nil-deref), (2) state store atomicity (`SetNX`, single-use unchanged), (3) output re-validation (`entry.ReturnTo` checked again at SocialLogin/BindOAuthIdentity exit), (4) session delivery (one-time short code RPC pair replaces raw session_id in URL). PKCE added via `RedirectProvider` / `SocialProvider` interface change; verifier stored in the existing Redis state entry — no new infrastructure beyond the short-code Redis keyspace. BFF-side state↔cookie binding is documented as a MUST contract; no user-service code change for it.

**Tech Stack:** Go 1.22+, `golang.org/x/oauth2` (already in deps, supports PKCE via `GenerateVerifier`/`S256ChallengeOption`/`VerifierOption`), `github.com/redis/go-redis/v9`, `github.com/stretchr/testify/require`, `github.com/alicebob/miniredis/v2` (already used via `redisx.NewTestClient`).

---

## Background

The previous review surfaced these issues (severity-ordered):

| # | Issue | Severity | File:line |
|---|-------|----------|-----------|
| 1 | `validateReturnTo` allows any URL when allowlist empty | CRITICAL | `internal/service/social/social.go:138-140` |
| 2 | `storeCallerState` uses `Set` (overwrites existing state) | CRITICAL | `internal/service/social/social.go:259` |
| 3 | `validateReturnTo` nil-derefs when `oauth.GitHub==nil` | HIGH | `internal/service/social/social.go:128` |
| 4 | SocialLogin trusts `entry.ReturnTo` without re-validation | HIGH | `internal/service/social/social.go:417,437` |
| 5 | No PKCE on any redirect provider | MED | `internal/identity/{github,google,wechat,apple}/*.go` |
| 6 | `GetOAuthURL` unauthenticated, no rate limit (already on follow-ups #7) | LOW | `internal/service/social/social.go:201` |
| 7 | `return_to` URL itself never validated (scheme/host/userinfo) | HIGH | `internal/service/social/social.go` (missing) |
| 8 | `session_id` lands in callback URL → referer/log/history leak | HIGH | README demo pattern (architectural) |

This plan addresses #1-#5, #7, #8. #6 is documented but deferred (item #7 in `docs/follow-ups-2026-07-02.md` already covers IP rate limiting).

## Design decisions (with rationale)

**D1 — `AllowedRedirectURLs` empty + non-empty `return_to` → reject.** Operator who wants the old "allow any" behavior must set `AllowArbitraryRedirectURLs: true`. Rationale: secure by default; the original "empty = allow any (operator hasn't locked it down yet)" comment was an open door that an attacker can walk through before the operator notices. A startup log warns when the escape hatch is on.

**D2 — `storeCallerState` uses `SetNX`.** If a caller-supplied state collides with one already in Redis, return `ErrBadRequest`. Rationale: caller-supplied state is for BFFs that bind state to their own cookie; if the key already exists, someone else issued it (legitimate flow race, or attacker trying the overwrite attack). Either way, reject and let the BFF retry with a fresh state.

**D3 — `SocialLogin` and `BindOAuthIdentity` re-validate `entry.ReturnTo` after consuming state.** Catches: (a) operator tightens allowlist between GetOAuthURL and SocialLogin (state still has old return_to in Redis for up to 10 min), (b) any future bug that lets an unvalidated return_to land in the state store. Cost: one allowlist walk per callback — negligible.

**D4 — `validateReturnTo` returns `nil` (allow) when `return_to == ""`.** Empty return_to means "no redirect needed" (BFF handles it differently). Allow this regardless of allowlist. SocialLogin's response carries an empty return_to; callback service knows that means "no 302 to deliver".

**D5 — `validateReturnTo` nil-checks each provider block.** Same pattern as `providerRedirectURL` (social.go:95-113).

**D6 — PKCE via interface signature change.** Add `codeChallenge string` to `RedirectProvider.GetAuthURL`, add `codeVerifier string` to `SocialProvider.ExchangeCode`. WeChat and MiniProgram accept the new param but ignore it (documented). social.Service generates the verifier, computes S256 challenge, stores verifier in `oauthStateEntry`, threads both through. Rationale: PKCE verifier must outlive GetAuthURL (it's needed at SocialLogin time, a separate request); the natural place is the existing state entry.

**D7 — Document BFF state↔cookie binding as MUST, not implement it.** Per prior decision (follow-ups doc item #2 — user rejected server-side auth interceptor), user-service cannot verify caller identity. The state-cookie binding must happen at the BFF / callback service layer. user-service's role: provide the `state` field on `GetOAuthURLRequest` (already exists), document the contract, show example code.

**D8 — Validate the URL itself, not just allowlist membership.** Even when `AllowArbitraryRedirectURLs=true` (dev/staging escape hatch) or when an allowlist entry is misconfigured, the URL scheme must NOT be in the dangerous-scheme denylist (`javascript` / `data` / `file` / `vbscript` / `about` / `blob` — case-insensitive), the URL must not contain userinfo or backslashes, and must have a scheme + host. Mobile deep link schemes (`myapp://`, `com.example.app://`) intentionally PASS — operators list them explicitly in `AllowedRedirectURLs`. Allowlist entries validated at startup so a bad config fails fast.

**D9 — One-time short code replaces raw `session_id` in callback URL.** Today's README demo puts `session_id` in URL query (`return_to?session_id=...`), which leaks via Referer / browser history / CDN logs / browser extensions / screenshots. user-service adds two RPCs: `IssueSessionCode(session_id) → code` (callback service calls after SocialLogin) and `ExchangeSessionCode(code) → (session_id, user_id)` (business side calls once to redeem). Code is 32 random bytes base64url-encoded, stored in Redis under `session:shortcode:` with 5-min TTL, consumed atomically via `GETDEL` (one-time use). Business side sets its own domain cookie after exchange.

## File structure

**Modified files:**
- `internal/service/social/social.go` — `validateReturnTo` (D1, D4, D5), `validateReturnToURL` new helper (D8), `validateOAuthConfig` extended to validate allowlist entries (D8), `storeCallerState` (D2), `SocialLogin` + `BindOAuthIdentity` re-validation (D3), `issueState` + `oauthStateEntry` for PKCE (D6)
- `pkg/config/config.go` — add `AllowArbitraryRedirectURLs bool` per provider; add `SessionCodeTTL` to `SessionConfig`
- `internal/identity/provider.go` — change `RedirectProvider.GetAuthURL` and `SocialProvider.ExchangeCode` signatures (D6)
- `internal/identity/github/github.go` — PKCE
- `internal/identity/google/google.go` — PKCE
- `internal/identity/apple/apple.go` — PKCE
- `internal/identity/tencent/wechat/wechat.go` — signature only (no PKCE)
- `internal/identity/tencent/mini/provider.go` — signature only (no PKCE)
- `internal/service/session/session.go` — `IssueSessionCode` + `ExchangeSessionCode` RPC handlers (D9)
- `internal/service/session/manager.go` — `codeTTL` field; existing `Manager` is reused
- `api/proto/user/v1/user.proto` — new RPCs `IssueSessionCode` + `ExchangeSessionCode` + messages (D9)
- `README.md` — BFF contract section + short-code demo (D7, D9)

**New files:**
- `internal/service/social/social_test.go` — covers all 5 fix areas + URL validation + PKCE wiring
- `internal/service/social/pkce.go` — `generatePKCEPair()` helper (verifier + S256 challenge)
- `internal/service/session/code.go` — `IssueSessionCode` / `ExchangeSessionCode` Manager methods
- `internal/service/session/code_test.go` — short-code round-trip, one-time use, expiry

**Test fixtures already available:** `redisx.NewTestClient(t)` (miniredis-backed), see `internal/service/session/manager_test.go` for pattern.

---

## Phase A — Critical fixes (no interface changes)

These are independent of each other and can be merged in any order. Do them first; they ship real security value with minimal blast radius.

### Task A1: Fix `validateReturnTo` nil-deref

**Files:**
- Modify: `internal/service/social/social.go:121-147`
- Test: `internal/service/social/social_test.go` (new file, this task creates it)

- [ ] **Step 1: Write the failing test**

Create `internal/service/social/social_test.go`:

```go
package social

import (
	"testing"

	pb "user-service/gen/user/v1"
	"user-service/pkg/config"

	"github.com/stretchr/testify/require"
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/service/social/ -run TestValidateReturnTo_NilProviderConfig -v
```

Expected: FAIL with panic `runtime error: invalid memory address or nil pointer dereference`.

- [ ] **Step 3: Write minimal implementation**

Replace the body of `validateReturnTo` in `internal/service/social/social.go`:

```go
// validateReturnTo enforces the per-provider allowlist for business return
// URLs. Empty return_to is always allowed (means "no redirect needed").
// Empty allowlist + non-empty return_to is governed by
// cfg.AllowArbitraryRedirectURLs (Task A2 — for now, retain current
// allow-any behavior; Task A2 flips the default).
func validateReturnTo(oauth *config.OAuthConfig, provider pb.IdentityProvider, returnTo string) error {
	if returnTo == "" {
		return nil
	}
	if oauth == nil {
		return nil
	}
	var allowed []string
	switch provider {
	case pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB:
		if oauth.GitHub == nil {
			return nil
		}
		allowed = oauth.GitHub.AllowedRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE:
		if oauth.Google == nil {
			return nil
		}
		allowed = oauth.Google.AllowedRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT:
		if oauth.WeChat == nil {
			return nil
		}
		allowed = oauth.WeChat.AllowedRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_APPLE:
		if oauth.Apple == nil {
			return nil
		}
		allowed = oauth.Apple.AllowedRedirectURLs
	default:
		return nil
	}
	if len(allowed) == 0 {
		return nil
	}
	for _, a := range allowed {
		if a == returnTo {
			return nil
		}
	}
	return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to %q is not in the allowlist for provider %s", returnTo, provider))
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/service/social/ -run TestValidateReturnTo_NilProviderConfig -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/social/social.go internal/service/social/social_test.go
git commit -m "fix(social): nil-check provider block in validateReturnTo"
```

---

### Task A2: Add `AllowArbitraryRedirectURLs` flag, flip default to deny

**Files:**
- Modify: `pkg/config/config.go:122-155` (4 provider config structs)
- Modify: `internal/service/social/social.go:121-147` (validateReturnTo)
- Test: `internal/service/social/social_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/social/social_test.go`:

```go
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
			AllowedRedirectURLs:      nil,
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/service/social/ -run TestValidateReturnTo -v
```

Expected: `TestValidateReturnTo_DefaultDeny` FAIL (current code returns nil); `TestValidateReturnTo_OptInArbitrary` PASS (coincidentally, since current code allows any); `TestValidateReturnTo_EmptyReturnToAlwaysAllowed` PASS.

- [ ] **Step 3: Add config field**

In `pkg/config/config.go`, add `AllowArbitraryRedirectURLs bool` to all 4 provider config structs. Example for GitHub (apply identical pattern to Google, WeChat, Apple):

```go
type OAuthGitHubConfig struct {
	ClientID                   string
	ClientSecret               string
	RedirectURL                string
	AllowedRedirectURLs        []string // exact-match allowlist; empty + non-empty return_to → reject
	AllowArbitraryRedirectURLs bool     // escape hatch for dev/staging; log warning at startup when true
}
```

- [ ] **Step 4: Update `validateReturnTo`**

```go
func validateReturnTo(oauth *config.OAuthConfig, provider pb.IdentityProvider, returnTo string) error {
	if returnTo == "" {
		return nil
	}
	if oauth == nil {
		return nil
	}
	var allowed []string
	var allowArbitrary bool
	switch provider {
	case pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB:
		if oauth.GitHub == nil {
			return nil
		}
		allowed = oauth.GitHub.AllowedRedirectURLs
		allowArbitrary = oauth.GitHub.AllowArbitraryRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE:
		if oauth.Google == nil {
			return nil
		}
		allowed = oauth.Google.AllowedRedirectURLs
		allowArbitrary = oauth.Google.AllowArbitraryRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT:
		if oauth.WeChat == nil {
			return nil
		}
		allowed = oauth.WeChat.AllowedRedirectURLs
		allowArbitrary = oauth.WeChat.AllowArbitraryRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_APPLE:
		if oauth.Apple == nil {
			return nil
		}
		allowed = oauth.Apple.AllowedRedirectURLs
		allowArbitrary = oauth.Apple.AllowArbitraryRedirectURLs
	default:
		return nil
	}
	if allowArbitrary {
		return nil
	}
	if len(allowed) == 0 {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to %q rejected: allowlist empty and AllowArbitraryRedirectURLs=false for provider %s", returnTo, provider))
	}
	for _, a := range allowed {
		if a == returnTo {
			return nil
		}
	}
	return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to %q is not in the allowlist for provider %s", returnTo, provider))
}
```

- [ ] **Step 5: Run tests to verify pass**

```bash
go test ./internal/service/social/ -run TestValidateReturnTo -v
```

Expected: all 4 validateReturnTo tests PASS (including the 3 new ones plus the nil-config one from Task A1).

- [ ] **Step 6: Surface escape-hatch warning to operator**

Per CLAUDE.md "库代码（internal/ 中的业务逻辑）不直接打日志" — `validateOAuthConfig` should NOT call `slog.Warn` directly, and `New()` MUST NOT print to stderr (`fmt.Fprintln(os.Stderr, ...)`) — that violates the same rule. Instead, `New()` returns the warnings slice up to `cmd/server/main.go`, which logs them via slog at startup.

Refactor: change `validateOAuthConfig` to return `(errors []error, warnings []string)`. In `internal/service/social/social.go`:

```go
// validateOAuthConfig checks each configured provider's RedirectURL is a valid
// http(s) URL with a host, and collects warnings about risky-but-allowed
// configurations (escape hatch on). Returns errors and warnings separately so
// the caller (cmd/server) can log warnings without violating the "internal/
// doesn't log" rule.
func validateOAuthConfig(cfg *config.OAuthConfig) (errs []string, warnings []string) {
	if cfg == nil {
		return nil, nil
	}
	type prov struct {
		name                  string
		url                   string
		allowArbitrary        bool
	}
	configured := make([]prov, 0, 4)
	if cfg.GitHub != nil {
		configured = append(configured, prov{"github", cfg.GitHub.RedirectURL, cfg.GitHub.AllowArbitraryRedirectURLs})
	}
	if cfg.Google != nil {
		configured = append(configured, prov{"google", cfg.Google.RedirectURL, cfg.Google.AllowArbitraryRedirectURLs})
	}
	if cfg.WeChat != nil {
		configured = append(configured, prov{"wechat", cfg.WeChat.RedirectURL, cfg.WeChat.AllowArbitraryRedirectURLs})
	}
	if cfg.Apple != nil {
		configured = append(configured, prov{"apple", cfg.Apple.RedirectURL, cfg.Apple.AllowArbitraryRedirectURLs})
	}
	for _, p := range configured {
		if err := validateRedirectURL(p.name, p.url); err != nil {
			errs = append(errs, err.Error())
		}
		if p.allowArbitrary {
			warnings = append(warnings, fmt.Sprintf("oauth.%s.allow_arbitrary_redirect_urls is true — dev/staging only; prod must set AllowedRedirectURLs", p.name))
		}
	}
	return errs, warnings
}
```

Change `New()` signature to return `(*Service, []string, error)` — the `[]string` is the warnings slice, surfaced to `cmd/server` for slog logging:

```go
// New creates the social Service. The returned []string carries advisory
// warnings (e.g. escape-hatch-on) — cmd/server logs them via slog. Returns
// nil warnings when oauth config is clean.
func New(/* ... */, oauth *config.OAuthConfig) (*Service, []string, error) {
	errs, warnings := validateOAuthConfig(oauth)
	if len(errs) > 0 {
		return nil, nil, xcodes.ErrInternal.New(fmt.Sprintf("oauth config: %s", strings.Join(errs, "; ")))
	}
	return &Service{ /* ... */ }, warnings, nil
}
```

Update `cmd/server/main.go` (and any other caller — see Task B1 step 2's grep) to consume the new signature:

```go
socialSvc, warnings, err := social.New(/* ... */, oauthCfg)
if err != nil {
    return err
}
for _, w := range warnings {
    slog.Warn(w)
}
```

The `os` and `fmt` imports are no longer needed in social.go for the warning path — keep `fmt` only if other code paths still use it (Sprintf for error messages).

**Verify callers** — `social.New()` is called from `internal/service/service.go:354` (not from `cmd/server/main.go` directly). The signature change `(*Service, error)` → `(*Service, []string, error)` will break the build there. Run:

```bash
grep -rn "socialsvc\.New\|social\.New\b" --include="*.go" .
```

Update every caller to consume the new `[]string` warnings slice and log each entry via `slog.Warn`. As of writing, only `internal/service/service.go:354` matches; re-run the grep to confirm no other caller was introduced since.

- [ ] **Step 7: Run all tests + lint**

```bash
make test
make lint
```

Expected: PASS, no lint errors.

- [ ] **Step 8: Commit**

```bash
git add pkg/config/config.go internal/service/social/social.go internal/service/social/social_test.go
git commit -m "feat(social): default-deny return_to allowlist with explicit opt-in"
```

---

### Task A3: `return_to` URL scheme/host/userinfo validation

**Files:**
- Modify: `internal/service/social/social.go` — add `validateReturnToURL` helper, call from `validateReturnTo`, validate allowlist entries in `validateOAuthConfig`
- Test: `internal/service/social/social_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/social/social_test.go`:

```go
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
		"content://com.android.contacts/data/1",          // Android content provider
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
		"/auth/done",          // relative path
		"auth/done",           // relative no leading slash
		"//app.example.com/",  // scheme-relative (no scheme)
		"#fragment",           // fragment only
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
			RedirectURL:         "https://callback.example.com/cb",
			AllowedRedirectURLs: []string{"myapp://oauth/callback"},
		},
	}
	errs, _ := validateOAuthConfig(cfg)
	require.Empty(t, errs, "custom scheme allowlist entry should be accepted")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/service/social/ -run 'TestValidateReturnToURL|TestValidateOAuthConfig_(RejectsBadAllowlistEntry|AcceptsCustomSchemeAllowlistEntry)' -v
```

Expected: FAIL — `validateReturnToURL` undefined; `validateOAuthConfig` doesn't loop allowlist.

- [ ] **Step 3: Implement `validateReturnToURL`**

In `internal/service/social/social.go`, add this helper near `validateReturnTo` (above or below — keep them adjacent):

```go
// dangerousReturnToSchemes are URL schemes that can execute code or load
// local resources when used as a 302 target — rejected outright regardless
// of allowlist membership. Comparison is case-insensitive.
//
// This is a DENYLIST, not an allowlist — mobile deep link schemes
// ("myapp://", "com.example.app://") intentionally pass, since they're how
// iOS/Android apps receive OAuth callbacks. Operators must list them
// explicitly in AllowedRedirectURLs.
var dangerousReturnToSchemes = map[string]bool{
	"javascript": true,
	"data":       true,
	"file":       true,
	"vbscript":   true,
	"about":      true,
	"blob":       true,
	"intent":     true, // Android intent:// — can launch arbitrary components
	"content":    true, // Android content:// — exposes content provider data
}

// validateReturnToURL checks the URL itself is safe to be a 302 target,
// regardless of allowlist. Defense-in-depth:
//   - Rejects CR/LF — response-splitting if the value lands in Location header.
//   - Rejects known-dangerous schemes (javascript/data/file/intent/...) — XSS.
//   - Rejects userinfo — phishing pattern (https://victim@evil.com).
//   - Rejects backslash — URL parser confusion (Chrome vs Go net/url).
//   - Rejects relative URLs / fragment-only — BFF must resolve.
//   - Rejects empty scheme — same as relative.
//
// Empty returnTo is allowed (means "no redirect needed" — module mode).
//
// This check runs BEFORE the allowlist check, so it applies even when
// AllowArbitraryRedirectURLs=true. Stops XSS via return_to even when the
// escape hatch is on.
func validateReturnToURL(returnTo string) error {
	if returnTo == "" {
		return nil
	}
	if strings.ContainsAny(returnTo, "\r\n") {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to contains CR/LF (response-splitting risk): %q", returnTo))
	}
	if strings.Contains(returnTo, "\\") {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to contains backslash (URL parser confusion risk): %q", returnTo))
	}
	u, err := url.Parse(returnTo)
	if err != nil {
		return xcodes.ErrBadRequest.Wrap(err)
	}
	if u.Scheme == "" {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to must have a scheme (relative URLs not supported — BFF must resolve): %q", returnTo))
	}
	if dangerousReturnToSchemes[strings.ToLower(u.Scheme)] {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to uses blocked scheme %q: %q", u.Scheme, returnTo))
	}
	if u.User != nil {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to must not contain userinfo: %q", returnTo))
	}
	if u.Host == "" {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to must have a host (absolute URL with authority required): %q", returnTo))
	}
	return nil
}
```

`net/url` is already imported (line 8). `strings` is already imported (line 9). `user-service/pkg/xcodes` is already imported (line 22).

- [ ] **Step 4: Wire into `validateReturnTo`**

Update `validateReturnTo` to call `validateReturnToURL` first:

```go
func validateReturnTo(oauth *config.OAuthConfig, provider pb.IdentityProvider, returnTo string) error {
	if err := validateReturnToURL(returnTo); err != nil {
		return err
	}
	if returnTo == "" {
		return nil
	}
	if oauth == nil {
		return nil
	}
	// ... rest unchanged (allowlist lookup)
}
```

- [ ] **Step 5: Validate allowlist entries in `validateOAuthConfig`**

Update the `prov` struct in `validateOAuthConfig` to carry the allowlist slice, and loop each entry through `validateReturnToURL`:

```go
func validateOAuthConfig(cfg *config.OAuthConfig) (errs []string, warnings []string) {
	if cfg == nil {
		return nil, nil
	}
	type prov struct {
		name                  string
		url                   string
		allowArbitrary        bool
		allowedRedirectURLs   []string
	}
	configured := make([]prov, 0, 4)
	if cfg.GitHub != nil {
		configured = append(configured, prov{"github", cfg.GitHub.RedirectURL, cfg.GitHub.AllowArbitraryRedirectURLs, cfg.GitHub.AllowedRedirectURLs})
	}
	if cfg.Google != nil {
		configured = append(configured, prov{"google", cfg.Google.RedirectURL, cfg.Google.AllowArbitraryRedirectURLs, cfg.Google.AllowedRedirectURLs})
	}
	if cfg.WeChat != nil {
		configured = append(configured, prov{"wechat", cfg.WeChat.RedirectURL, cfg.WeChat.AllowArbitraryRedirectURLs, cfg.WeChat.AllowedRedirectURLs})
	}
	if cfg.Apple != nil {
		configured = append(configured, prov{"apple", cfg.Apple.RedirectURL, cfg.Apple.AllowArbitraryRedirectURLs, cfg.Apple.AllowedRedirectURLs})
	}
	for _, p := range configured {
		if err := validateRedirectURL(p.name, p.url); err != nil {
			errs = append(errs, err.Error())
		}
		for i, entry := range p.allowedRedirectURLs {
			if err := validateReturnToURL(entry); err != nil {
				errs = append(errs, fmt.Sprintf("%s.allowed_redirect_urls[%d]: %s", p.name, i, err))
			}
		}
		if p.allowArbitrary {
			warnings = append(warnings, fmt.Sprintf("oauth.%s.allow_arbitrary_redirect_urls is true — dev/staging only; prod must set AllowedRedirectURLs", p.name))
		}
	}
	return errs, warnings
}
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
go test ./internal/service/social/ -run 'TestValidateReturnToURL|TestValidateOAuthConfig_RejectsBadAllowlistEntry' -v
```

Expected: all PASS.

- [ ] **Step 7: Run full test suite + lint**

```bash
make test
make lint
```

- [ ] **Step 8: Commit**

```bash
git add internal/service/social/social.go internal/service/social/social_test.go
git commit -m "feat(social): validate return_to URL scheme/host/userinfo"
```

---

### Task A4: `storeCallerState` use `SetNX`

**Files:**
- Modify: `internal/service/social/social.go:248-263`
- Test: `internal/service/social/social_test.go`

- [ ] **Step 1: Write the failing test**

Append to `social_test.go`:

```go
import (
	// add to existing imports:
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestService spins up a social.Service backed by miniredis for state
// store tests. Returns the service and a cleanup func.
func newTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
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
	victimState, err := svc.issueState(ctx, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://victim.example.com/done")
	require.NoError(t, err)
	require.NotEmpty(t, victimState)

	// Attacker tries to overwrite the same key with their own return_to.
	err = svc.storeCallerState(ctx, victimState, pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB, "https://evil.example.com/")
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
		"https://app.example.com/done")
	require.NoError(t, err)

	entry, err := svc.consumeState(ctx, state)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Equal(t, pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE.String(), entry.Provider)
	require.Equal(t, "https://app.example.com/done", entry.ReturnTo)
	require.NotZero(t, entry.CreatedAt)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/service/social/ -run TestStoreCallerState_RejectsExisting -v
```

Expected: FAIL — `storeCallerState` returns nil (current code uses `Set` which silently overwrites), and the consumed entry has the attacker's return_to.

- [ ] **Step 3: Implement**

In `internal/service/social/social.go`, replace `storeCallerState`:

```go
// storeCallerState records a caller-supplied state in Redis with the same TTL
// and payload shape as issueState. Used when the caller brings their own state
// (e.g. PKCE-style state bound to the BFF's own cookie). The caller's state is
// used as-is for the Redis key. Rejects if the key already exists — prevents
// an attacker who learned the state UUID from overwriting its return_to.
func (s *Service) storeCallerState(ctx context.Context, state string, provider pb.IdentityProvider, returnTo string) error {
	entry := oauthStateEntry{
		Provider:  provider.String(),
		ReturnTo:  returnTo,
		CreatedAt: time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	key := "oauth:state:" + state
	ok, err := s.rdb.SetNX(ctx, key, payload, oauthStateTTL).Result()
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if !ok {
		return xcodes.ErrBadRequest.New("state already exists; generate a fresh state value")
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/service/social/ -run TestStoreCallerState_RejectsExisting -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/social/social.go internal/service/social/social_test.go
git commit -m "fix(social): use SetNX in storeCallerState to prevent overwrite"
```

---

### Task A5: Re-validate `return_to` at SocialLogin / BindOAuthIdentity exit

**Files:**
- Modify: `internal/service/social/social.go` — `SocialLogin` (line ~382-439) and `BindOAuthIdentity` (line ~279-374)
- Test: `internal/service/social/social_test.go`

- [ ] **Step 1: Write the failing test**

Append to `social_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/service/social/ -run TestSocialLogin_RevalidatesReturnTo -v
```

Expected: FAIL — `SocialLogin` consumes the state, then proceeds to `ExchangeCode` and fails there (provider not configured). The return_to check doesn't exist yet.

- [ ] **Step 3: Implement — re-validate before returning response**

In `internal/service/social/social.go` `SocialLogin`, after consuming state and verifying provider match, BEFORE ExchangeCode, add:

```go
if err := validateReturnTo(s.oauth, req.Provider, entry.ReturnTo); err != nil {
    return nil, xcodes.ErrBadRequest.Wrap(err)
}
```

Insert this block immediately after the `entry.Provider != req.Provider.String()` check (around line 396). Rationale for placement: reject before doing the network call to the OAuth provider — fail fast.

Apply the same change to `BindOAuthIdentity`. Insert after the state provider-match check (around line 299). Note: `BindOAuthIdentity` doesn't currently use `entry.ReturnTo` in its response, but validating it is still correct — if the state's return_to is invalid, the state itself shouldn't be trusted.

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/service/social/ -run TestSocialLogin_RevalidatesReturnTo -v
```

Expected: PASS — `SocialLogin` now rejects with "return_to ... not in the allowlist" before reaching ExchangeCode.

- [ ] **Step 5: Run full test suite**

```bash
make test
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/service/social/social.go internal/service/social/social_test.go
git commit -m "fix(social): re-validate return_to at SocialLogin/BindOAuthIdentity"
```

---

### Task A6: Phase A integration test — full GetOAuthURL → SocialLogin flow

This task locks in the Phase A fixes with an end-to-end style test using a mock provider, ensuring the fixes compose correctly.

**Files:**
- Modify: `internal/service/social/social_test.go`

- [ ] **Step 1: Write the integration test**

Append to `social_test.go`:

```go
// mockProvider is a minimal RedirectProvider for integration tests. It
// returns canned URLs and ExchangeCode results without hitting any network.
//
// Signature note: this mock uses the POST-Phase-B interface signatures
// (4-arg GetAuthURL, 3-arg ExchangeCode). If Phase B is skipped or
// deferred, revert this mock to the pre-B signatures
// (GetAuthURL(_ context.Context, _, _ string); ExchangeCode(_ context.Context, _ string))
// or the package will fail to compile. Search for "POST-B MOCK" to find
// the revert points.
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
```

Add imports: `"user-service/internal/identity"`.

- [ ] **Step 2: Run test**

```bash
go test ./internal/service/social/ -run TestGetOAuthURL_DefaultDenyEndToEnd -v
```

Expected: PASS — validateReturnTo runs before issueState/storeCallerState in `GetOAuthURL` (verify by reading social.go:201-242 — yes, current order is correct).

- [ ] **Step 3: Commit**

```bash
git add internal/service/social/social_test.go
git commit -m "test(social): integration test for default-deny GetOAuthURL path"
```

---

## Phase B — PKCE hardening

Phase B adds defense against OAuth `code` interception (RFC 7636 / RFC 9700). It changes the `RedirectProvider` and `SocialProvider` interfaces, so all 4 redirect providers + MiniProgram must be updated. **Phase B can be skipped** if the operator judges TLS-only sufficient for their threat model; the Phase A fixes alone close the most critical holes.

### Task B1: Update `SocialProvider` and `RedirectProvider` interfaces

**Files:**
- Modify: `internal/identity/provider.go`

- [ ] **Step 1: Update interfaces**

Replace `internal/identity/provider.go:14-32`:

```go
// SocialProvider is the core interface for social login providers.
// All social providers must implement ExchangeCode to exchange a code for user info.
//
// codeVerifier is the PKCE code_verifier (RFC 7636) issued by user-service at
// GetOAuthURL time and threaded back through SocialLogin. Providers that do
// not use PKCE (WeChat qrconnect, WeChat Mini Program) accept the value but
// ignore it.
type SocialProvider interface {
	Provider() userv1.IdentityProvider

	ExchangeCode(ctx context.Context, code, codeVerifier string) (*SocialResult, error)
}

// RedirectProvider is an optional interface for providers that require redirect-based OAuth.
// Providers like GitHub, Google, WeChat (web) implement this in addition to SocialProvider.
//
// codeChallenge is the PKCE code_challenge (S256-encoded, RFC 7636) computed
// from the verifier issued at the same time. Providers that do not use PKCE
// accept the value but ignore it.
type RedirectProvider interface {
	SocialProvider

	GetAuthURL(ctx context.Context, redirectURL, state, codeChallenge string) (string, error)
}
```

- [ ] **Step 2: Verify build fails as expected**

```bash
go build ./...
```

Expected: FAIL — compile errors in github, google, apple, wechat, mini providers (old signatures), and social.go (passing wrong number of args).

- [ ] **Step 3: Commit (broken build — will be fixed by B2-B6)**

The remaining Phase B tasks fix the build incrementally. We commit at the end of B6 when all providers and social.go are updated.

---

### Task B2: Update GitHub provider for PKCE

**Files:**
- Modify: `internal/identity/github/github.go:54-60` (GetAuthURL) and `62-106` (ExchangeCode)
- Modify: `internal/identity/github/github_test.go:32-42, 75-77, 113-115` (signature updates)

- [ ] **Step 1: Update tests first (TDD)**

In `internal/identity/github/github_test.go`, update the GetAuthURL test:

```go
func TestProvider_GetAuthURL(t *testing.T) {
	p := New("id", "secret", "http://default/cb")
	url, err := p.GetAuthURL(context.Background(), "", "state-123", "")
	require.NoError(t, err)
	require.Contains(t, url, "state=state-123")
	require.Contains(t, url, "client_id=id")
	require.NotContains(t, url, "code_challenge", "empty codeChallenge must not add PKCE params")

	url, err = p.GetAuthURL(context.Background(), "http://override/cb", "s2", "challenge-abc")
	require.NoError(t, err)
	require.Contains(t, url, "redirect_uri=http%3A%2F%2Foverride%2Fcb")
	require.Contains(t, url, "code_challenge=challenge-abc")
	require.Contains(t, url, "code_challenge_method=S256")
}
```

Update the ExchangeCode tests to pass a verifier arg. In `TestProvider_ExchangeCode_Success` (around line 75):

```go
result, err := p.ExchangeCode(context.Background(), "test-code", "verifier-xyz")
```

In `TestProvider_ExchangeCode_NicknameFallbackToLogin` (around line 113):

```go
result, err := p.ExchangeCode(context.Background(), "code", "verifier")
```

In `TestProvider_ExchangeCode_TokenError` (around line 133):

```go
_, err := p.ExchangeCode(context.Background(), "bad-code", "verifier")
```

Add a new test for PKCE verification on the token endpoint:

```go
// TestProvider_ExchangeCode_PKCEVerifies verifies the code_verifier reaches
// the token endpoint as expected. The mock token handler fails the test if
// it sees a verifier when none was sent, or vice versa.
func TestProvider_ExchangeCode_PKCEVerifies(t *testing.T) {
	var sawVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sawVerifier = r.PostForm.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
	p.userInfoURL = srv.URL + "/user"

	_, _ = p.ExchangeCode(context.Background(), "code", "my-verifier")
	require.Equal(t, "my-verifier", sawVerifier, "code_verifier must reach token endpoint")
}
```

- [ ] **Step 2: Run tests — expected to fail**

```bash
go test ./internal/identity/github/ -v
```

Expected: FAIL — signature mismatch (GetAuthURL takes 3 args, ExchangeCode takes 2).

- [ ] **Step 3: Update provider implementation**

In `internal/identity/github/github.go`, replace GetAuthURL:

```go
// GetAuthURL returns the OAuth authorization URL. The optional redirectURL,
// when non-empty, is applied per-call via oauth2.SetAuthURLParam so we never
// mutate the shared config (which would race with concurrent callers).
// codeChallenge is the PKCE S256 code_challenge; empty disables PKCE.
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state, codeChallenge string) (string, error) {
	opts := []oauth2.AuthCodeOption{}
	if redirectURL != "" {
		opts = append(opts, oauth2.SetAuthURLParam("redirect_uri", redirectURL))
	}
	if codeChallenge != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_challenge", codeChallenge))
		opts = append(opts, oauth2.SetAuthURLParam("code_challenge_method", "S256"))
	}
	return p.config.AuthCodeURL(state, opts...), nil
}
```

Replace ExchangeCode signature and PKCE wiring:

```go
// ExchangeCode exchanges the OAuth code for user info. codeVerifier is the
// PKCE code_verifier matching the challenge sent at GetAuthURL time; empty
// disables PKCE.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	opts := []oauth2.AuthCodeOption{}
	if codeVerifier != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	}
	token, err := p.config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}

	// ... (rest of existing function body unchanged from line 69 onward)
}
```

- [ ] **Step 4: Run tests — expected to pass**

```bash
go test ./internal/identity/github/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/identity/github/github.go internal/identity/github/github_test.go
git commit -m "feat(github): add PKCE support to OAuth flow"
```

---

### Task B3: Update Google provider for PKCE

Google supports PKCE per RFC 7636 (and recommends it for all web clients).

**Files:**
- Modify: `internal/identity/google/google.go:49-55` (GetAuthURL) and `58-96` (ExchangeCode)
- Modify: `internal/identity/google/google_test.go` (signature updates + new PKCE assertion test)

- [ ] **Step 1: Update tests**

In `internal/identity/google/google_test.go`, find every `GetAuthURL(` call and add a 4th arg. Find every `ExchangeCode(` call and add a 2nd arg. Specific edits:

For `TestProvider_GetAuthURL` (the existing test that asserts URL contains expected params):
```go
func TestProvider_GetAuthURL(t *testing.T) {
	p := New("id", "secret", "http://default/cb")
	url, err := p.GetAuthURL(context.Background(), "", "state-123", "")
	require.NoError(t, err)
	require.Contains(t, url, "state=state-123")
	require.Contains(t, url, "client_id=id")
	require.NotContains(t, url, "code_challenge", "empty codeChallenge must not add PKCE params")

	url, err = p.GetAuthURL(context.Background(), "http://override/cb", "s2", "challenge-abc")
	require.NoError(t, err)
	require.Contains(t, url, "code_challenge=challenge-abc")
	require.Contains(t, url, "code_challenge_method=S256")
}
```

For `TestProvider_ExchangeCode_Success` (the existing test with httptest mock), update the call:
```go
result, err := p.ExchangeCode(context.Background(), "test-code", "verifier-xyz")
```

Add a new test verifying the verifier reaches the token endpoint:
```go
// TestProvider_ExchangeCode_PKCEVerifies verifies the code_verifier is sent
// to Google's token endpoint when provided.
func TestProvider_ExchangeCode_PKCEVerifies(t *testing.T) {
	var sawVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sawVerifier = r.PostForm.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
	p.userInfoURL = srv.URL + "/userinfo"

	_, _ = p.ExchangeCode(context.Background(), "code", "my-verifier")
	require.Equal(t, "my-verifier", sawVerifier)
}
```

- [ ] **Step 2: Run tests — expected to fail**

```bash
go test ./internal/identity/google/ -v
```

Expected: FAIL — signature mismatch on GetAuthURL and ExchangeCode.

- [ ] **Step 3: Update provider implementation**

In `internal/identity/google/google.go`, replace GetAuthURL (lines 49-55):

```go
// GetAuthURL returns the OAuth authorization URL. The optional redirectURL,
// when non-empty, is applied per-call via oauth2.SetAuthURLParam so we never
// mutate the shared config (which would race with concurrent callers).
// codeChallenge is the PKCE S256 code_challenge; empty disables PKCE.
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state, codeChallenge string) (string, error) {
	opts := []oauth2.AuthCodeOption{}
	if redirectURL != "" {
		opts = append(opts, oauth2.SetAuthURLParam("redirect_uri", redirectURL))
	}
	if codeChallenge != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_challenge", codeChallenge))
		opts = append(opts, oauth2.SetAuthURLParam("code_challenge_method", "S256"))
	}
	return p.config.AuthCodeURL(state, opts...), nil
}
```

Replace ExchangeCode signature and PKCE wiring (line 58):

```go
// ExchangeCode exchanges the OAuth code for user info. codeVerifier is the
// PKCE code_verifier matching the challenge sent at GetAuthURL time; empty
// disables PKCE.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	opts := []oauth2.AuthCodeOption{}
	if codeVerifier != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	}
	token, err := p.config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}

	// ... rest of existing function body unchanged (line 64 onward — userInfoURL setup, HTTP fetch, JSON decode, SocialResult mapping)
}
```

- [ ] **Step 4: Run tests — expected to pass**

```bash
go test ./internal/identity/google/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/identity/google/google.go internal/identity/google/google_test.go
git commit -m "feat(google): add PKCE support to OAuth flow"
```

---

### Task B4: Update Apple provider for PKCE

Apple supports PKCE since 2022.

**Files:**
- Modify: `internal/identity/apple/apple.go:109-126` (GetAuthURL) and `130-182` (ExchangeCode)
- Modify: `internal/identity/apple/apple_test.go` (signature updates)

- [ ] **Step 1: Update tests**

In `internal/identity/apple/apple_test.go`, update GetAuthURL test cases to pass `codeChallenge` arg. Add a case verifying `code_challenge` and `code_challenge_method=S256` appear in the URL when challenge is non-empty.

- [ ] **Step 2: Run tests — expected to fail**

```bash
go test ./internal/identity/apple/ -v
```

- [ ] **Step 3: Update GetAuthURL**

In `apple.go:118-125`, augment the `q := url.Values{}` block:

```go
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
```

- [ ] **Step 4: Update ExchangeCode**

In `apple.go:130-182`, change signature to `func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string)`. Add to the form:

```go
form.Set("grant_type", "authorization_code")
form.Set("code", code)
form.Set("client_id", p.clientID)
form.Set("client_secret", clientSecret)
form.Set("redirect_uri", p.redirectURL)
if codeVerifier != "" {
	form.Set("code_verifier", codeVerifier)
}
```

- [ ] **Step 5: Run tests — expected to pass**

```bash
go test ./internal/identity/apple/ -v
```

- [ ] **Step 6: Commit**

```bash
git add internal/identity/apple/apple.go internal/identity/apple/apple_test.go
git commit -m "feat(apple): add PKCE support to OAuth flow"
```

---

### Task B5: Update WeChat provider (signature only — no PKCE)

WeChat qrconnect does not document PKCE support. Provider accepts the params for interface conformance but ignores them.

**Files:**
- Modify: `internal/identity/tencent/wechat/wechat.go:53-62` (GetAuthURL) and `64-114` (ExchangeCode)
- Modify: `internal/identity/tencent/wechat/wechat_test.go` (signature updates)

- [ ] **Step 1: Update tests**

In `wechat_test.go`, update GetAuthURL / ExchangeCode calls to pass the new args (empty strings are fine since they're ignored).

- [ ] **Step 2: Run tests — expected to fail**

```bash
go test ./internal/identity/tencent/wechat/ -v
```

- [ ] **Step 3: Update provider signatures**

```go
// GetAuthURL returns the OAuth authorization URL. codeChallenge is accepted
// for interface conformance but ignored — WeChat qrconnect does not document
// PKCE support (as of 2024-12). If WeChat adds PKCE later, wire it here
// (q.Set("code_challenge", codeChallenge); q.Set("code_challenge_method", "S256")).
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state, codeChallenge string) (string, error) {
	_ = codeChallenge // accepted for interface conformance; WeChat qrconnect has no PKCE
	opts := []oauth2.AuthCodeOption{}
	if redirectURL != "" {
		opts = append(opts, oauth2.SetAuthURLParam("redirect_uri", redirectURL))
	}
	return p.config.AuthCodeURL(state, opts...), nil
}

// ExchangeCode exchanges the OAuth code for user info. codeVerifier is
// accepted for interface conformance but ignored — see GetAuthURL doc.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	_ = codeVerifier // accepted for interface conformance; WeChat qrconnect has no PKCE
	token, err := p.config.Exchange(ctx, code)
	// ... rest unchanged
}
```

**Why named params instead of `_`:** golangci-lint's `unused-parameter` / `revive` may flag `_` in method signatures on interface implementations; named params with an explicit `_ = paramName` discard are lint-clean and self-document the no-op intent.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/identity/tencent/wechat/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/identity/tencent/wechat/wechat.go internal/identity/tencent/wechat/wechat_test.go
git commit -m "feat(wechat): accept PKCE params for interface conformance (no-op)"
```

---

### Task B6: Update MiniProgram provider (signature only — not a redirect flow)

MiniProgram uses `wx.login()` → `code2session`, not the redirect-based OAuth flow. Update signatures for interface conformance; no PKCE applies.

**Files:**
- Modify: `internal/identity/tencent/mini/provider.go`
- Modify: `internal/identity/tencent/mini/provider_test.go`, `client_test.go`, `manager_test.go` (any direct ExchangeCode calls)

- [ ] **Step 1: Update tests**

Search for `ExchangeCode(` calls in mini tests, add a second arg `""`:

```bash
grep -n "ExchangeCode(" internal/identity/tencent/mini/
```

For each match, change `ExchangeCode(ctx, code)` → `ExchangeCode(ctx, code, "")`.

- [ ] **Step 2: Run tests — expected to fail**

```bash
go test ./internal/identity/tencent/mini/ -v
```

- [ ] **Step 3: Update provider**

In `internal/identity/tencent/mini/provider.go`, find `ExchangeCode` and change signature to:

```go
// ExchangeCode exchanges the wx.login code for session info via
// code2session. codeVerifier is accepted for interface conformance but
// ignored — Mini Program is not a redirect-based OAuth flow, PKCE does
// not apply.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	_ = codeVerifier // accepted for interface conformance; Mini Program has no PKCE
	// ... body unchanged
}
```

**Why named param instead of `_`:** see Task B5 step 3 note — golangci-lint clean, self-documenting no-op.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/identity/tencent/mini/ -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/identity/tencent/mini/
git commit -m "feat(mini): accept codeVerifier arg for interface conformance (no-op)"
```

---

### Task B7: PKCE helper + state entry schema migration

Add the PKCE pair generator and extend `oauthStateEntry` to carry the verifier.

**Files:**
- Create: `internal/service/social/pkce.go`
- Create: `internal/service/social/pkce_test.go`
- Modify: `internal/service/social/social.go:41-45` (oauthStateEntry)

- [ ] **Step 1: Write the failing test**

Create `internal/service/social/pkce_test.go`:

```go
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
```

- [ ] **Step 2: Run test — expected to fail**

```bash
go test ./internal/service/social/ -run TestGeneratePKCEPair -v
```

Expected: FAIL — `generatePKCEPair` undefined.

- [ ] **Step 3: Implement**

Create `internal/service/social/pkce.go`:

```go
package social

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
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
```

- [ ] **Step 4: Run test**

```bash
go test ./internal/service/social/ -run TestGeneratePKCEPair -v
```

Expected: PASS.

- [ ] **Step 5: Extend `oauthStateEntry`**

In `internal/service/social/social.go:41-45`:

```go
type oauthStateEntry struct {
	Provider     string `json:"provider"`
	ReturnTo     string `json:"return_to"`
	CodeVerifier string `json:"code_verifier,omitempty"` // empty for providers that don't use PKCE (WeChat, MiniProgram)
	CreatedAt    int64  `json:"created_at"`
}
```

**Backward compatibility note:** entries written before this change unmarshal cleanly (Go's `encoding/json` ignores unknown fields, and `code_verifier` is `omitempty` so old entries deserialize with an empty verifier). However, an in-flight state entry from a pre-deploy GetOAuthURL — consumed by a post-deploy SocialLogin — will carry an empty `CodeVerifier`, so PKCE-enabled providers (GitHub/Google/Apple) will hit the token endpoint with no `code_verifier` and **fail the exchange**. The state TTL is 10 min, so the blast window is bounded. user-service is not yet in production (no live state entries at deploy time), so no migration is needed; if that changes, drain in-flight state entries (wait one TTL after deploy before routing traffic) or have the token endpoint fall back to non-PKCE for one TTL window.

- [ ] **Step 6: Commit**

```bash
git add internal/service/social/pkce.go internal/service/social/pkce_test.go internal/service/social/social.go
git commit -m "feat(social): add PKCE pair generator and extend state entry"
```

---

### Task B8: Wire PKCE through social.Service

Generate verifier+challenge in `GetOAuthURL`, store verifier in state, read verifier in `SocialLogin`/`BindOAuthIdentity`, pass to provider.

**Files:**
- Modify: `internal/service/social/social.go` — `GetOAuthURL`, `SocialLogin`, `BindOAuthIdentity`, `issueState`, `storeCallerState`

- [ ] **Step 1: Write the failing test**

Append to `social_test.go`:

```go
// TestGetOAuthURL_PKCEVerifierStored verifies that GetOAuthURL generates a
// PKCE pair, stores the verifier in the state entry, and passes the S256
// challenge to the provider's GetAuthURL.
func TestGetOAuthURL_PKCEVerifierStored(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	var seenChallenge string
	svc.oauth = &config.OAuthConfig{
		GitHub: &config.OAuthGitHubConfig{
			RedirectURL:           "http://localhost/cb",
			AllowedRedirectURLs:   []string{"https://app.example.com/done"},
		},
	}
	svc.socialProviders = map[pb.IdentityProvider]identity.SocialProvider{
		pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: &mockProvider{
			provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
			authURL:  "https://github.com/login/oauth/authorize",
		},
	}
	// Override mockProvider.GetAuthURL to capture the challenge.
	svc.socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB] = &pkceCapturingProvider{
		seenChallenge: &seenChallenge,
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
```

Add imports: `"crypto/sha256"`, `"encoding/base64"`.

- [ ] **Step 2: Run test — expected to fail**

```bash
go test ./internal/service/social/ -run TestGetOAuthURL_PKCEVerifierStored -v
```

Expected: FAIL — `oauthStateEntry.CodeVerifier` doesn't exist yet... wait, B7 added it. Should fail because `GetOAuthURL` doesn't call `generatePKCEPair` and doesn't pass challenge to provider. The state entry's CodeVerifier will be empty.

- [ ] **Step 3: Update `issueState` and `storeCallerState` to accept verifier**

```go
func (s *Service) issueState(ctx context.Context, provider pb.IdentityProvider, returnTo, codeVerifier string) (string, error) {
	state := uuid.New().String()
	entry := oauthStateEntry{
		Provider:     provider.String(),
		ReturnTo:     returnTo,
		CodeVerifier: codeVerifier,
		CreatedAt:    time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return "", xcodes.ErrInternal.Wrap(err)
	}
	key := "oauth:state:" + state
	ok, err := s.rdb.SetNX(ctx, key, payload, oauthStateTTL).Result()
	if err != nil {
		return "", xcodes.ErrInternal.Wrap(err)
	}
	if !ok {
		return "", xcodes.ErrInternal.New("state collision, please retry")
	}
	return state, nil
}

func (s *Service) storeCallerState(ctx context.Context, state string, provider pb.IdentityProvider, returnTo, codeVerifier string) error {
	entry := oauthStateEntry{
		Provider:     provider.String(),
		ReturnTo:     returnTo,
		CodeVerifier: codeVerifier,
		CreatedAt:     time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	key := "oauth:state:" + state
	ok, err := s.rdb.SetNX(ctx, key, payload, oauthStateTTL).Result()
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if !ok {
		return xcodes.ErrBadRequest.New("state already exists; generate a fresh state value")
	}
	return nil
}
```

- [ ] **Step 4: Update `GetOAuthURL`**

```go
func (s *Service) GetOAuthURL(ctx context.Context, req *pb.GetOAuthURLRequest) (*pb.GetOAuthURLResponse, error) {
	prov, ok := s.socialProviders[req.Provider]
	if !ok {
		return nil, xcodes.ErrBadRequest.New("unsupported provider")
	}
	redirectProv, ok := prov.(identity.RedirectProvider)
	if !ok {
		return nil, xcodes.ErrBadRequest.New("provider does not support redirect-based login")
	}
	if err := validateReturnTo(s.oauth, req.Provider, req.ReturnTo); err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}

	providerRedirect, err := providerRedirectURL(s.oauth, req.Provider)
	if err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}

	// Generate PKCE pair regardless of provider — providers that don't use
	// PKCE simply ignore the challenge. The verifier is stored in the state
	// entry and threaded back through SocialLogin.
	verifier, challenge, err := generatePKCEPair()
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	state := req.State
	if state == "" {
		generated, err := s.issueState(ctx, req.Provider, req.ReturnTo, verifier)
		if err != nil {
			return nil, err
		}
		state = generated
	} else {
		if err := s.storeCallerState(ctx, state, req.Provider, req.ReturnTo, verifier); err != nil {
			return nil, err
		}
	}

	url, err := redirectProv.GetAuthURL(ctx, providerRedirect, state, challenge)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &pb.GetOAuthURLResponse{Url: url, State: state}, nil
}
```

- [ ] **Step 5: Update `SocialLogin` to pass verifier**

Find the `prov.ExchangeCode(ctx, req.Code)` call in `SocialLogin` (around line 402) and change to:

```go
result, err := prov.ExchangeCode(ctx, req.Code, entry.CodeVerifier)
```

Same change in `BindOAuthIdentity` (around line 305).

The MiniProgram methods (`MiniProgramLogin`, `MiniProgramPhoneLogin`) call ExchangeCode directly — update those too with `""`:

```go
result, err := prov.ExchangeCode(ctx, req.Code, "")
```

And `mpProv.ExchangeCode(ctx, req.LoginCode, "")` for MiniProgramPhoneLogin.

**Why the asymmetry between `entry.CodeVerifier` (SocialLogin/BindOAuthIdentity) and `""` (MiniProgram methods):** MiniProgram does not go through `GetOAuthURL` — there is no `oauth:state:` entry, hence no verifier to thread. Both branches reach the same `ExchangeCode(ctx, code, codeVerifier string)` interface method; WeChat (qrconnect, reached via SocialLogin) receives a non-empty verifier but ignores it (see B5), MiniProgram receives `""` and ignores it. End-state is identical: no PKCE applied. The two paths look different but behave the same.

- [ ] **Step 6: Run test — expected to pass**

```bash
go test ./internal/service/social/ -v
```

Expected: all tests PASS, including the PKCE one.

- [ ] **Step 7: Verify full build**

```bash
go build ./...
make lint
make test
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/service/social/social.go internal/service/social/social_test.go
git commit -m "feat(social): wire PKCE verifier through OAuth state and provider calls"
```

---

## Phase C — Documentation

Phase C documents the BFF-side state↔cookie binding contract (Login CSRF defense) and updates the callback service demo to use the new PKCE-aware code. **Phase C is required** regardless of whether Phase B is done — the contract is the same.

### Task C1: README — state↔cookie binding BFF contract

**Files:**
- Modify: `README.md` — section "OAuth 重定向登录（统一回调架构）"

- [ ] **Step 1: Add a "Security contract" subsection**

After the existing flow diagram (around line 130), insert a new subsection:

```markdown
### BFF 安全契约（MUST）

`SocialLogin` 防得住"假 state"，防不住"真 state 被偷换"。下面的契约 BFF 必须遵守，否则会有 Login CSRF 风险（攻击者把自己的 code 喂进受害者的 state，让受害者登入攻击者账号）：

1. **state 必须由 BFF 生成并绑 cookie**。`GetOAuthURLRequest.state` 字段不为空时，user-service 会原样存下来；BFF 应当：
   - 生成一个高熵随机 `state`（32 字节起）
   - 把 `state` 的 HMAC（用 BFF 自己的 cookie secret）写入 HttpOnly cookie
   - 浏览器跳去 OAuth 提供方时带着 state
   - 提供方跳回统一回调服务时，回调服务**必须验证**：query 里的 `state` 能匹配上 cookie 里的 HMAC
   - 不匹配直接 400，不调 `SocialLogin`

2. **`return_to` 必须用 user-service 的 allowlist 校验**，不允许 BFF 自行"信任前端传的 return_to"。配置：`cfg.OAuth.{provider}.AllowedRedirectURLs`。开发期可用 `AllowArbitraryRedirectURLs=true` 逃生，但生产**禁止**开启。

3. **PKCE 由 user-service 自动启用**（GitHub / Google / Apple；WeChat 不支持），BFF 无需参与。

4. **session_id 不能放进 return_to 的 URL query**。统一回调服务应该：
   - 生成一个一次性短 code（5 分钟过期）
   - 把 `session_id → short_code` 写入 Redis
   - 302 到 `return_to?code=<short_code>`
   - 业务方拿 short_code 调 user-service 换 session_id（这个 RPC 待补，参考 follow-ups #1）
   
   URL 里的 session_id 会进 referer / 日志 / 浏览器历史，等于把 token 贴墙上。

5. **state 一次性消费**。user-service 用 Redis `GETDEL` 原子读删，重放会被拒。BFF 也应当在 cookie 里标记"已使用"，防止双重提交。

下面是符合契约的 callback service 关键片段（替换上文 50 行 demo 里的对应部分）：
```

- [ ] **Step 2: Add a state-cookie verification code snippet**

After the contract section, add:

````markdown
```go
// 统一回调服务：验证 state cookie → 调 SocialLogin → 跳业务方
func oauthCallback(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    state := r.URL.Query().Get("state")
    if code == "" || state == "" {
        redirectOAuthError(w, r, "missing_params")
        return
    }

    // 1. 验证 state ↔ cookie 绑定（Login CSRF 防御，MUST）
    cookieState, err := r.Cookie("oauth_state")
    if err != nil || cookieState.Value == "" {
        redirectOAuthError(w, r, "missing_state_cookie")
        return
    }
    expectedState := hmacState(cookieState.Value, cookieSecret) // BFF 自己的 secret
    if !hmac.Equal([]byte(state), []byte(expectedState)) {
        redirectOAuthError(w, r, "state_cookie_mismatch")
        return
    }

    // 2. 调 user-service（state 已经在 GetOAuthURL 时绑过 PKCE verifier）
    resp, err := userClient.SocialLogin(r.Context(), &userv1.SocialLoginRequest{
        Provider: provider,
        Code:     code,
        State:    state,
    })
    if err != nil {
        redirectOAuthError(w, r, classifyOAuthError(err))
        return
    }

    // 3. 一次性短 code 模式：不把 session_id 放 URL，放 Redis 短 code
    shortCode := generateShortCode()
    if err := redisClient.Set(r.Context(), "session:shortcode:"+shortCode,
        resp.SessionId, 5*time.Minute).Err(); err != nil {
        redirectOAuthError(w, r, "internal_error")
        return
    }

    // 4. 清掉 oauth_state cookie，重定向到 return_to
    http.SetCookie(w, &http.Cookie{
        Name: "oauth_state", MaxAge: -1, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
    })
    redirectURL := resp.ReturnTo + "?code=" + shortCode
    http.Redirect(w, r, redirectURL, http.StatusFound)
}

// hmacState returns HMAC-SHA256(secret, nonce) as a hex string. The BFF
// stores nonce in a cookie, sends HMAC as the OAuth state; on callback,
// recompute HMAC(cookie_nonce) and compare to query state.
func hmacState(nonce string, secret []byte) string {
    h := hmac.New(sha256.New, secret)
    h.Write([]byte(nonce))
    return hex.EncodeToString(h.Sum(nil))
}
```
````

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(readme): document BFF state-cookie binding security contract"
```

---

### Task C2: Update OAuth config example with allowlist

**Files:**
- Modify: `config.yaml` (or `config.example.yaml` if present)

- [ ] **Step 1: Check for existing example config**

```bash
ls config*.yaml
```

- [ ] **Step 2: Add example with allowlist populated**

If `config.yaml` exists, ensure OAuth section shows the new `AllowArbitraryRedirectURLs` flag and a populated `AllowedRedirectURLs`:

```yaml
oauth:
  github:
    client_id: "..."
    client_secret: "..."
    redirect_url: "https://auth.corp.com/oauth/callback/github"
    allowed_redirect_urls:
      - "https://app.corp.com/auth/done"
      - "https://admin.corp.com/auth/done"
    # allow_arbitrary_redirect_urls: false  # NEVER enable in prod; dev/staging escape hatch only
  google:
    # ... same pattern
```

- [ ] **Step 3: Commit**

```bash
git add config.yaml
git commit -m "docs(config): show populated OAuth allowlist in example"
```

---

### Task C3: Add `IssueSessionCode` + `ExchangeSessionCode` RPC pair

Replaces the "raw `session_id` in callback URL" pattern (Referer/log/history leak) with a one-time short code. Callback service issues the code after `SocialLogin`, business side exchanges it for `session_id` once, then sets its own domain cookie.

**Files:**
- Modify: `api/proto/user/v1/user.proto` — new RPCs + 4 messages
- Modify: `internal/service/session/session.go` — RPC handlers
- Create: `internal/service/session/code.go` — Manager methods
- Create: `internal/service/session/code_test.go` — tests
- Modify: `internal/service/session/manager.go` — add `codeTTL` field
- Modify: `pkg/config/config.go` — add `SessionCodeTTL` to `SessionConfig`

- [ ] **Step 1: Add proto definitions**

In `api/proto/user/v1/user.proto`, after the `GetSession` RPC (around line 348), add:

```proto
  // IssueSessionCode mints a one-time short code that references the given
  // session_id. Used by the OAuth callback service to hand the session back
  // to the business side via URL query without leaking session_id into
  // referer/logs/history. Business side calls ExchangeSessionCode within
  // ~5 min to trade the code for session_id + user_id. One-time use.
  rpc IssueSessionCode(IssueSessionCodeRequest) returns (IssueSessionCodeResponse) {
    option (google.api.http) = {post: "/v1/sessions/issue-code" body: "*"};
  }

  // ExchangeSessionCode trades a one-time short code (issued by
  // IssueSessionCode) for session_id + user_id. Atomic one-time use
  // (GETDEL); replay returns ErrSessionInvalid. Business side sets its
  // own domain cookie from the returned session_id.
  rpc ExchangeSessionCode(ExchangeSessionCodeRequest) returns (ExchangeSessionCodeResponse) {
    option (google.api.http) = {post: "/v1/sessions/exchange" body: "*"};
  }
```

Add the messages near the existing `GetSessionRequest`/`GetSessionResponse`:

```proto
message IssueSessionCodeRequest {
  string session_id = 1 [(buf.validate.field).string = {min_len: 1, max_len: 128}];
}

message IssueSessionCodeResponse {
  // code: 32 random bytes base64url-encoded (~43 chars). Pass via URL
  // query to the business side. TTL ~5 min, one-time use.
  string code = 1;
}

message ExchangeSessionCodeRequest {
  string code = 1 [(buf.validate.field).string = {min_len: 1, max_len: 128}];
}

message ExchangeSessionCodeResponse {
  string session_id = 1;
  int64 user_id = 2;
}
```

- [ ] **Step 2: Regenerate proto bindings**

```bash
make proto
```

Expected: `gen/user/v1/user.pb.go` etc. updated with the two new methods on the UserServiceClient and server interface.

- [ ] **Step 3: Write the failing tests**

Create `internal/service/session/code_test.go`:

```go
package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestManager(t *testing.T) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &Manager{client: rdb, codeTTL: 5 * time.Minute}, mr
}

func TestIssueAndExchangeSessionCode_RoundTrip(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	code, err := m.IssueSessionCode(ctx, "sess-123")
	require.NoError(t, err)
	require.Len(t, code, 43, "base64url(32 bytes) = 43 chars")

	sid, err := m.ExchangeSessionCode(ctx, code)
	require.NoError(t, err)
	require.Equal(t, "sess-123", sid)
}

func TestExchangeSessionCode_OneTimeUse(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	code, err := m.IssueSessionCode(ctx, "sess-123")
	require.NoError(t, err)

	_, err = m.ExchangeSessionCode(ctx, code)
	require.NoError(t, err)

	_, err = m.ExchangeSessionCode(ctx, code)
	require.Error(t, err, "second exchange must fail — one-time use")
}

func TestExchangeSessionCode_Expired(t *testing.T) {
	m, mr := newTestManager(t)
	ctx := context.Background()

	code, err := m.IssueSessionCode(ctx, "sess-123")
	require.NoError(t, err)

	mr.FastForward(6 * time.Minute)

	_, err = m.ExchangeSessionCode(ctx, code)
	require.Error(t, err, "expired code must fail")
}

func TestIssueSessionCode_Random(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	seen := make(map[string]bool, 50)
	for i := 0; i < 50; i++ {
		code, err := m.IssueSessionCode(ctx, "sess")
		require.NoError(t, err)
		require.False(t, seen[code], "collision at iter %d", i)
		seen[code] = true
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

```bash
go test ./internal/service/session/ -run 'TestIssueAndExchangeSessionCode|TestExchangeSessionCode|TestIssueSessionCode_Random' -v
```

Expected: FAIL — `Manager.IssueSessionCode` undefined.

- [ ] **Step 5: Implement Manager methods**

Create `internal/service/session/code.go`:

```go
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/redis/go-redis/v9"

	"user-service/internal/xcodes"
)

// sessionCodeKeyPrefix is the Redis namespace for one-time short codes
// minted by IssueSessionCode. Distinct from the session-data keyspace
// (sessionKeyPrefix) so TTLs and access patterns don't interfere.
const sessionCodeKeyPrefix = "session:shortcode:"

// IssueSessionCode mints a one-time short code referencing sessionID.
// The code is 32 random bytes base64url-encoded (~43 chars). Stored in
// Redis under sessionCodeKeyPrefix with TTL = m.codeTTL. One-time use —
// exchange via ExchangeSessionCode consumes it atomically (GETDEL).
//
// Returns the code; caller (callback service) puts it in the URL query
// when 302'ing to return_to instead of leaking session_id.
func (m *Manager) IssueSessionCode(ctx context.Context, sessionID string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", xcodes.ErrInternal.Wrapf(err, "read random bytes for session code")
	}
	code := base64.RawURLEncoding.EncodeToString(raw[:])
	key := sessionCodeKeyPrefix + code
	if err := m.client.Set(ctx, key, sessionID, m.codeTTL).Err(); err != nil {
		return "", xcodes.ErrInternal.Wrapf(err, "redis set session code")
	}
	return code, nil
}

// ExchangeSessionCode trades a one-time code for the underlying session_id.
// Atomic GETDEL — replay returns an error. Empty code is rejected at the
// service layer (validation), but defensive check here too.
func (m *Manager) ExchangeSessionCode(ctx context.Context, code string) (string, error) {
	if code == "" {
		return "", xcodes.ErrBadRequest.New("empty session code")
	}
	key := sessionCodeKeyPrefix + code
	sid, err := m.client.GetDel(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", xcodes.ErrSessionInvalid.New("session code not found or already used")
		}
		return "", xcodes.ErrInternal.Wrapf(err, "redis getdel session code")
	}
	return sid, nil
}
```

- [ ] **Step 6: Add `codeTTL` field to Manager**

In `internal/service/session/manager.go`, add the field and wire it in `NewManager`:

```go
type Manager struct {
	client             *redis.Client
	ttl                time.Duration
	maxSessions        int
	keyPrefix          string
	userSessionsPrefix string
	codeTTL            time.Duration // TTL for one-time session short codes (IssueSessionCode)
}

const defaultSessionCodeTTL = 5 * time.Minute

func NewManager(client *redis.Client, cfg *config.SessionConfig) *Manager {
	codeTTL := defaultSessionCodeTTL
	if cfg.SessionCodeTTL > 0 {
		codeTTL = cfg.SessionCodeTTL
	}
	return &Manager{
		client:             client,
		ttl:                cfg.TTL,
		maxSessions:        cfg.MaxSessions,
		keyPrefix:          cfg.KeyPrefix,
		userSessionsPrefix: cfg.UserSessionsPrefix,
		codeTTL:            codeTTL,
	}
}
```

In `pkg/config/config.go`, extend `SessionConfig`:

```go
type SessionConfig struct {
	TTL                time.Duration `default:"168h"`
	MaxSessions        int           `default:"5"`
	KeyPrefix          string        `default:"user:session"`
	UserSessionsPrefix string        `default:"user:user_sessions"`
	SessionCodeTTL     time.Duration `default:"5m"` // one-time short code TTL for IssueSessionCode
}
```

- [ ] **Step 7: Implement RPC handlers**

In `internal/service/session/session.go`, add:

```go
func (s *Service) IssueSessionCode(ctx context.Context, req *pb.IssueSessionCodeRequest) (*pb.IssueSessionCodeResponse, error) {
	if req.SessionId == "" {
		return nil, xcodes.ErrBadRequest.New("session_id is required")
	}
	code, err := s.mgr.IssueSessionCode(ctx, req.SessionId)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &pb.IssueSessionCodeResponse{Code: code}, nil
}

func (s *Service) ExchangeSessionCode(ctx context.Context, req *pb.ExchangeSessionCodeRequest) (*pb.ExchangeSessionCodeResponse, error) {
	if req.Code == "" {
		return nil, xcodes.ErrBadRequest.New("code is required")
	}
	sid, err := s.mgr.ExchangeSessionCode(ctx, req.Code)
	if err != nil {
		// Not-found / expired / replay all map to ErrSessionInvalid — caller
		// can't distinguish, which is intentional (no information leak).
		return nil, xcodes.ErrSessionInvalid.Wrap(err)
	}
	data, err := s.mgr.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	return &pb.ExchangeSessionCodeResponse{
		SessionId: sid,
		UserId:    data.UserID,
	}, nil
}
```

- [ ] **Step 8: Run tests**

```bash
go test ./internal/service/session/ -v
go build ./...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add api/proto/user/v1/user.proto gen/ internal/service/session/ pkg/config/config.go
git commit -m "feat(session): add IssueSessionCode + ExchangeSessionCode RPC pair"
```

---

### Task C4: Update README callback demo with short-code pattern

Replace the raw `session_id` in URL demo with the short-code flow.

**Files:**
- Modify: `README.md` — the callback service demo and surrounding docs

- [ ] **Step 1: Locate the demo section**

Find the existing callback demo in `README.md` (around line 170, the `oauthCallback` handler).

- [ ] **Step 2: Rewrite the demo**

Replace the existing `oauthCallback` function body with:

```go
func oauthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		redirectOAuthError(w, r, "missing_params")
		return
	}

	// 1. Verify state ↔ cookie binding (Login CSRF defense, see §Security contract)
	cookieState, err := r.Cookie("oauth_state")
	if err != nil || cookieState.Value == "" {
		redirectOAuthError(w, r, "missing_state_cookie")
		return
	}
	expectedState := hmacState(cookieState.Value, cookieSecret)
	if !hmac.Equal([]byte(state), []byte(expectedState)) {
		redirectOAuthError(w, r, "state_cookie_mismatch")
		return
	}

	// 2. SocialLogin (state has PKCE verifier bound from GetOAuthURL time)
	resp, err := userClient.SocialLogin(r.Context(), &userv1.SocialLoginRequest{
		Provider: provider,
		Code:     code,
		State:    state,
	})
	if err != nil {
		redirectOAuthError(w, r, classifyOAuthError(err))
		return
	}

	// 3. Mint a one-time short code for the business side. NEVER put
	//    session_id in URL query — it leaks via Referer, browser history,
	//    CDN logs, browser extensions, screenshots.
	issueResp, err := userClient.IssueSessionCode(r.Context(), &userv1.IssueSessionCodeRequest{
		SessionId: resp.SessionId,
	})
	if err != nil {
		redirectOAuthError(w, r, "internal_error")
		return
	}

	// 4. Clear oauth_state cookie, redirect to return_to with short code.
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_state", MaxAge: -1, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	redirectURL := resp.ReturnTo + "?code=" + issueResp.GetCode()
	http.Redirect(w, r, redirectURL, http.StatusFound)
}
```

Add a new section explaining the business-side exchange step:

```markdown
### 业务方收到 short code 后的处理（MUST）

业务方的 `return_to` handler 必须做三件事：

1. 拿到 URL query 里的 `code`
2. 调 user-service 的 `ExchangeSessionCode(code)` 换 `session_id` + `user_id`
3. 用 `session_id` set **自己域名**的 HttpOnly + Secure + SameSite=Lax cookie

代码示例：

\`\`\`go
func handleAuthDone(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    if code == "" {
        http.Error(w, "missing code", http.StatusBadRequest)
        return
    }
    resp, err := userClient.ExchangeSessionCode(r.Context(), &userv1.ExchangeSessionCodeRequest{
        Code: code,
    })
    if err != nil {
        http.Error(w, "invalid or expired code", http.StatusUnauthorized)
        return
    }
    http.SetCookie(w, &http.Cookie{
        Name:     "usid",
        Value:    resp.SessionId,
        Path:     "/",
        HttpOnly: true,
        Secure:   true,
        SameSite: http.SameSiteLaxMode,
        MaxAge:   7 * 24 * 3600, // match session TTL
    })
    // Redirect to the actual app UI
    http.Redirect(w, r, "/", http.StatusFound)
}
\`\`\`

**注意**：
- `code` 是一次性的，刷新页面或重复访问会失败 — 业务方应在拿到 code 后立刻 set cookie 并跳走
- 如果业务方在多个顶级域（`a.com` / `b.com`），每个域都要走自己的 `return_to` handler，user-service 不参与跨域 cookie 共享
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(readme): use one-time short code pattern in callback demo"
```

---

### Task C5: Document "as Go module" usage pattern

user-service can be embedded as a Go module in a larger application (in-process), bypassing the gRPC + unified callback service architecture. In module mode the embedding app's own HTTP handler IS the OAuth callback, so `return_to` is unused and `AllowedRedirectURLs` is irrelevant. This task adds a README section documenting the pattern so module-mode users don't have to reverse-engineer the unified-callback design.

**Files:**
- Modify: `README.md` — new section "作为 Go 模块使用（in-process）"

- [ ] **Step 1: Locate insertion point**

In `README.md`, find the end of the "OAuth 重定向登录（统一回调架构）" section (after Task C1's BFF contract + Task C4's short-code demo). Add the new section immediately after it, before the next H2.

- [ ] **Step 2: Add the section**

Insert the following Markdown:

````markdown
## 作为 Go 模块使用（in-process 模式）

user-service 既可以独立部署为 gRPC 服务（配合统一回调 HTTP 服务，见上文），也可以**作为 Go 模块嵌入到你的应用进程里**。模块模式下整个 OAuth 流从 `GetOAuthURL` 到 `SocialLogin` 都在同一个进程，不需要统一回调服务，也不需要 `return_to` 路由。

### 模块模式 vs 服务模式

| 维度 | gRPC 服务模式（统一回调架构） | 模块模式（in-process） |
|------|------------------------------|----------------------|
| OAuth 提供方注册的回调 URL | 一个独立的"统一回调服务"地址 | **嵌入应用自己的 HTTP handler** |
| 调 `SocialLogin` 的人 | 统一回调服务 | **嵌入应用直接调** |
| `return_to` 的作用 | 告诉回调服务"业务方在哪" | **用不到，留空** |
| `AllowedRedirectURLs` | 业务方白名单，必配 | **不需要配** |
| `AllowArbitraryRedirectURLs` | 逃生口 | **不需要配** |
| `state` ↔ cookie 绑定 | BFF 必须做 | 嵌入应用自己用任何 session 机制 |
| `IssueSessionCode` / `ExchangeSessionCode` RPC | callback 服务用 | **不用** — 嵌入应用直接拿 `session_id` |

### 模块模式接入示例

```go
package main

import (
    "context"
    "net/http"

    "user-service/internal/identity/github"
    "user-service/internal/service/social"
    "user-service/pkg/config"
)

func main() {
    // 1. 构造 social.Service，RedirectURL 指向嵌入应用自己的路由
    githubProv := github.New("client-id", "client-secret",
        "https://myapp.com/auth/github/callback") // ← 嵌入应用的回调 endpoint

    cfg := &config.OAuthConfig{
        GitHub: &config.OAuthGitHubConfig{
            ClientID:     "client-id",
            ClientSecret: "client-secret",
            RedirectURL:  "https://myapp.com/auth/github/callback",
            // AllowedRedirectURLs 不配 — 模块模式用不到
            // AllowArbitraryRedirectURLs 不配 — 默认 false
        },
    }

    socialSvc, err := social.New(db, sessionMgr,
        map[pb.IdentityProvider]identity.SocialProvider{
            pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: githubProv,
        },
        gid, rdb, cfg)
    if err != nil {
        panic(err)
    }

    // 2. 用户点"用 GitHub 登录" → 跳 OAuth 提供方
    http.HandleFunc("/login/github", func(w http.ResponseWriter, r *http.Request) {
        // 模块模式关键：return_to 留空
        resp, err := socialSvc.GetOAuthURL(r.Context(), &pb.GetOAuthURLRequest{
            Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
            ReturnTo: "", // ← 模块模式标志
        })
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        http.Redirect(w, r, resp.Url, http.StatusFound)
    })

    // 3. GitHub 回调到嵌入应用的路由（路径必须和 cfg.RedirectURL 一致）
    http.HandleFunc("/auth/github/callback", func(w http.ResponseWriter, r *http.Request) {
        code := r.URL.Query().Get("code")
        state := r.URL.Query().Get("state")
        if code == "" || state == "" {
            http.Error(w, "missing params", http.StatusBadRequest)
            return
        }

        resp, err := socialSvc.SocialLogin(r.Context(), &pb.SocialLoginRequest{
            Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
            Code:     code,
            State:    state,
        })
        if err != nil {
            http.Error(w, err.Error(), http.StatusUnauthorized)
            return
        }

        // 4. 嵌入应用自己 set cookie、自己跳自己的页面
        http.SetCookie(w, &http.Cookie{
            Name:     "usid",
            Value:    resp.SessionId,
            Path:     "/",
            HttpOnly: true,
            Secure:   true,
            SameSite: http.SameSiteLaxMode,
            MaxAge:   7 * 24 * 3600,
        })
        http.Redirect(w, r, "/dashboard", http.StatusFound) // ← 自己跳，不靠 return_to
    })

    http.ListenAndServe(":8080", nil)
}
```

### 模块模式下的 state ↔ cookie 绑定（仍然 MUST）

即便在模块模式，**Login CSRF 风险依然存在**（攻击者把自己的 OAuth `code` 喂进受害者的 state）。嵌入应用必须自己绑：

```go
// 启动登录时
nonce := generateRandomString(32)
setCookie(w, "oauth_state", nonce) // 嵌入应用自己用 securecookie
state := hmacSHA256(nonce, appSecret) // 把 nonce 转 state

socialSvc.GetOAuthURL(ctx, &pb.GetOAuthURLRequest{
    Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
    ReturnTo: "",
    State:    state, // ← 嵌入应用自己传 state（走 caller-supplied state 路径）
})

// 回调时验证
cookieNonce := readCookie(r, "oauth_state")
expectedState := hmacSHA256(cookieNonce, appSecret)
if !hmac.Equal([]byte(r.URL.Query().Get("state")), []byte(expectedState)) {
    http.Error(w, "state mismatch", http.StatusBadRequest)
    return
}
// 再调 SocialLogin
```

或者更简单：直接把 nonce 作为 state 传，回调时比 cookie 就行（只要 nonce 足够随机，HMAC 不是必须）。

### 多实例部署的注意

模块模式下，state 存在 Redis 里（`oauth:state:<state>`），所以**多实例部署没问题** — 任何实例都能消费任何实例发的 state。session 也走 Redis。唯一要在嵌入应用层处理的是：cookie 的 `Domain` 要一致，或者用 sticky session。

### 什么时候用模块模式

- 嵌入应用已经有自己的 HTTP 服务（不想再起 gRPC + 回调服务）
- 单一业务方（不需要"多业务共用一套 OAuth App"）
- 想要更简单的部署拓扑（一个进程搞定）
- 内部工具 / 中后台 / 单体应用

什么时候**不要**用模块模式：

- 多业务方共用 OAuth（必须走统一回调架构）
- 跨顶级域 cookie 共享（模块模式不解决跨域）
- 想要 user-service 独立升级 / 扩缩容（模块模式把 user-service 编进嵌入应用二进制）
````

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(readme): document Go module (in-process) usage pattern"
```

---

## Self-review checklist

After completing all tasks, run through these:

- [ ] **Spec coverage** — every issue from the review (Critical #1-5 + HIGH #7, #8 + Med documented) has at least one task implementing or documenting the fix:
  - #1 (allowlist default) → Task A2
  - #2 (state overwrite) → Task A4
  - #3 (nil deref) → Task A1
  - #4 (return_to re-validation) → Task A5
  - #5 (PKCE) → Tasks B1-B8
  - #7 (URL scheme/host validation) → Task A3
  - #8 (session_id URL leak) → Tasks C3, C4
  - #6 (rate limit) → documented as deferred to follow-ups #7

- [ ] **Build clean** — `go build ./...` passes.

- [ ] **All tests pass** — `make test` returns 0.

- [ ] **Lint clean** — `make lint` returns 0.

- [ ] **No security regression** — re-read each fix; confirm no new hole introduced. Specifically:
  - PKCE verifier is not logged.
  - State entry JSON does not leak verifier in error messages.
  - Allowlist opt-in flag warns loudly at startup.

- [ ] **Doc accuracy** — README example matches actual interface signatures.

- [ ] **Follow-ups doc updated** — append completed items to `docs/follow-ups-2026-07-02.md` "已完成" section.

---

## Execution notes

- **Phase A is self-contained and ships real security value.** If time-boxed, do Phase A only and revisit B/C. A3 (URL validation) and A4 (SetNX) are the smallest, highest-leverage fixes — start there.
- **Phase B has a build break between B1 and B6** — do not push between these commits. If using subagent-driven development, the subagent for B1 should be told explicitly that downstream tasks will fix the build.
- **Phase C is now mostly docs + one RPC pair (C3).** C1, C2, C4, C5 are docs-only and can be done in parallel with B. C3 changes proto + session service and adds 2 RPCs — small but a build-deploy event. C5 is purely additive docs for module-mode users; no code change.
- **Task ordering within Phase A matters:** A1 → A2 → A3 → A4 → A5 → A6. A3 depends on A2's `validateOAuthConfig` shape; A6 integration test depends on all prior.
- **C3 must complete before C4** — README demo in C4 calls RPCs that C3 adds.
