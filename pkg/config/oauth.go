package config

// IsConfigured reports whether the GitHub provider carries real credentials.
// A provider block that is nil OR has an empty ClientID is treated as
// "not configured" and is skipped by service construction and OAuth
// validation — not built, not validated, no fail-fast on its redirect_url.
func (c *OAuthGitHubConfig) IsConfigured() bool { return c != nil && c.ClientID != "" }

// IsConfigured reports whether the Google provider carries real credentials.
// See OAuthGitHubConfig.IsConfigured for the nil/empty policy.
func (c *OAuthGoogleConfig) IsConfigured() bool { return c != nil && c.ClientID != "" }

// IsConfigured reports whether the WeChat provider carries real credentials.
// Covers both IDENTITY_PROVIDER_WECHAT and IDENTITY_PROVIDER_WECHAT_MINIPROGRAM
// (MiniProgram reuses WeChat.AppID via wechatMgr). See
// OAuthGitHubConfig.IsConfigured for the nil/empty policy.
func (c *OAuthWeChatConfig) IsConfigured() bool { return c != nil && c.AppID != "" }

// IsConfigured reports whether the Apple provider carries real credentials.
// All four fields are required: apple.New parses PrivateKey at construction
// (fails on empty), and TeamID/KeyID/ClientID are needed for a functional
// client_secret JWT and ID-token audience check. See
// OAuthGitHubConfig.IsConfigured for the nil/empty policy.
func (c *OAuthAppleConfig) IsConfigured() bool {
	return c != nil &&
		c.TeamID != "" &&
		c.KeyID != "" &&
		c.ClientID != "" &&
		c.PrivateKey != ""
}
