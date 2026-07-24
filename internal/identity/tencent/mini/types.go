// Package mini provides a WeChat Mini Program API client with access token management,
// plus a SocialProvider implementation for mini-program login.
package mini

// Config holds configuration for the Mini Program Manager.
type Config struct {
	// Credentials maps appid to secret. Supports multiple mini programs.
	Credentials map[string]string

	// OnRefreshError, if non-nil, is invoked when a background access-token
	// refresh fails. The Manager itself stays silent on background errors;
	// callers wire logging/metrics through this hook. If nil, errors are
	// dropped (the next call will retry the refresh via singleflight).
	OnRefreshError func(appID string, err error)
}

// LoginResp is the response from jscode2session.
type LoginResp struct {
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
	OpenID     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionID    string `json:"unionid"`
}

// AccessTokenResp is the response from getStableAccessToken.
type AccessTokenResp struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// PhoneNumberResp is the response from getPhoneNumber.
type PhoneNumberResp struct {
	ErrCode   int        `json:"errcode"`
	ErrMsg    string     `json:"errmsg"`
	PhoneInfo *PhoneInfo `json:"phone_info"`
}

// CheckSessionResp is the response from checksession. ErrCode == 0 means
// the session_key is still valid; any non-zero code indicates the session
// has expired or been revoked.
type CheckSessionResp struct {
	ErrCode int `json:"errcode"`
}

// PhoneInfo contains the user's phone number details.
type PhoneInfo struct {
	PhoneNumber     string `json:"phoneNumber"`
	PurePhoneNumber string `json:"purePhoneNumber"`
	CountryCode     string `json:"countryCode"`
}
