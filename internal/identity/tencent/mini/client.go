package mini

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/servekit/user-service/pkg/xcodes"

	"github.com/servekit/go-common/xerr"
)

// Client calls WeChat Mini Program APIs for a single appid.
type Client struct {
	appID      string
	secret     string
	baseURL    string
	httpClient *http.Client
}

const (
	defaultBaseURL           = "https://api.weixin.qq.com"
	code2SessionPath         = "/sns/jscode2session"
	checkSessionKeyPath      = "/wxa/checksession"
	getStableAccessTokenPath = "/cgi-bin/stable_token"
	getPhoneNumberPath       = "/wxa/business/getuserphonenumber"

	loginGrantType       = "authorization_code"
	accessTokenGrantType = "client_credential"
	sigMethodHMACSHA256  = "hmac_sha256"
)

// NewClient creates a new WeChat Mini Program API client.
func NewClient(appID, secret string) *Client {
	return &Client{
		appID:   appID,
		secret:  secret,
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a client with a custom base URL (for testing).
func NewClientWithBaseURL(appID, secret, baseURL string) *Client {
	c := NewClient(appID, secret)
	c.baseURL = baseURL
	return c
}

// SignIn calls jscode2session to exchange a wx.login() code for openid + session_key.
func (c *Client) SignIn(ctx context.Context, code string) (*LoginResp, error) {
	params := url.Values{}
	params.Add("appid", c.appID)
	params.Add("secret", c.secret)
	params.Add("js_code", code)
	params.Add("grant_type", loginGrantType)

	body, err := c.get(ctx, code2SessionPath, params)
	if err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}

	resp := &LoginResp{}
	if err := json.Unmarshal(body, resp); err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	if err := checkErr(xcodes.ErrOAuthExchangeFailed, resp.ErrCode, resp.ErrMsg, "jscode2session"); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetStableAccessToken returns a stable access token.
func (c *Client) GetStableAccessToken(ctx context.Context, forceRefresh bool) (*AccessTokenResp, error) {
	reqBody := map[string]any{
		"grant_type":    accessTokenGrantType,
		"appid":         c.appID,
		"secret":        c.secret,
		"force_refresh": forceRefresh,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	body, err := c.post(ctx, getStableAccessTokenPath, nil, bodyBytes)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	resp := &AccessTokenResp{}
	if err := json.Unmarshal(body, resp); err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	if err := checkErr(xcodes.ErrInternal, resp.ErrCode, resp.ErrMsg, "getStableAccessToken"); err != nil {
		return nil, err
	}
	return resp, nil
}

// CheckLoginStatus checks if the session key is still valid. Returns false
// (without error) when WeChat reports the session as expired via errcode,
// because that is the answer the caller asked for, not a transport failure.
func (c *Client) CheckLoginStatus(ctx context.Context, accessToken, sessionKey, openID string) (bool, error) {
	signature := signSessionKey(sessionKey)
	params := url.Values{}
	params.Add("openid", openID)
	params.Add("access_token", accessToken)
	params.Add("signature", signature)
	params.Add("sig_method", sigMethodHMACSHA256)

	body, err := c.get(ctx, checkSessionKeyPath, params)
	if err != nil {
		return false, xcodes.ErrInternal.Wrap(err)
	}

	var resp CheckSessionResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, xcodes.ErrInternal.Wrap(err)
	}
	return resp.ErrCode == 0, nil
}

// GetPhoneNumber exchanges a phone-number code for the user's phone number.
func (c *Client) GetPhoneNumber(ctx context.Context, accessToken, code string) (*PhoneNumberResp, error) {
	reqBody := map[string]string{"code": code}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	params := url.Values{}
	params.Add("access_token", accessToken)

	body, err := c.post(ctx, getPhoneNumberPath, params, bodyBytes)
	if err != nil {
		return nil, xcodes.ErrOAuthFailed.Wrap(err)
	}

	resp := &PhoneNumberResp{}
	if err := json.Unmarshal(body, resp); err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	if err := checkErr(xcodes.ErrOAuthFailed, resp.ErrCode, resp.ErrMsg, "getPhoneNumber"); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	reqURL := c.baseURL + path
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) post(ctx context.Context, path string, params url.Values, body []byte) ([]byte, error) {
	reqURL := c.baseURL + path
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// --- internal helpers ---

func signSessionKey(sessionKey string) string {
	h := hmac.New(sha256.New, []byte(sessionKey))
	h.Write([]byte(sessionKey))
	return hex.EncodeToString(h.Sum(nil))
}

// checkErr maps a WeChat errcode onto the supplied xerr code, including the
// endpoint name in the message so callers can tell which API rejected the
// request when triaging.
func checkErr(code xerr.Code, errCode int, errMsg, endpoint string) error {
	if errCode == 0 {
		return nil
	}
	return code.New(fmt.Sprintf("wechat %s: errcode=%d msg=%s", endpoint, errCode, errMsg))
}
