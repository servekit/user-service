package clientinfo_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/servekit/user-service/pkg/clientinfo"
)

func TestWrap_StampsClientInfoFromXFFAndUA(t *testing.T) {
	var got http.Header
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header
	})

	r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2, 10.0.0.3")
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0")

	clientinfo.Wrap(next).ServeHTTP(httptest.NewRecorder(), r)

	require.Equal(t, "203.0.113.7", got.Get("Grpc-Metadata-X-Client-Ip"))
	require.Equal(t, r.Header.Get("User-Agent"), got.Get("Grpc-Metadata-X-Client-Ua"))
}

func TestWrap_StripsInboundSpoofedHeaders(t *testing.T) {
	var got http.Header
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header
	})

	r := httptest.NewRequest("GET", "/api/v1/things", nil)
	r.Header.Set("Grpc-Metadata-X-Client-Ip", "1.2.3.4") // spoofed
	r.Header.Set("X-Client-Ua", "spoofed-ua")            // spoofed

	clientinfo.Wrap(next).ServeHTTP(httptest.NewRecorder(), r)

	// No proxy in the test request, so the fallback (RemoteAddr host) is
	// stamped — never the spoofed value.
	require.Equal(t, "192.0.2.1", got.Get("Grpc-Metadata-X-Client-Ip"))
	require.Empty(t, got.Get("Grpc-Metadata-X-Client-Ua")) // no UA on the request
	require.Empty(t, got.Get("X-Client-Ua"))
}

func TestFromCtx_ReadsEdgeMetadata(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"X-Client-Ip", "203.0.113.7", // canonical casing, as the gateway leaves it
		"X-Client-Ua", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	))

	ci := clientinfo.FromCtx(ctx)
	require.Equal(t, "203.0.113.7", ci.IP)
	require.Equal(t, "iOS 17.2", ci.OS)
	require.Equal(t, "Safari 17.0", ci.Browser) // Version/ token, not the OS build
	require.Equal(t, "iPhone", ci.Device)
	require.Contains(t, ci.UserAgent, "iPhone")
}

func TestFromCtx_NoMetadataReturnsZero(t *testing.T) {
	require.Equal(t, clientinfo.ClientInfo{}, clientinfo.FromCtx(context.Background()))
}

func TestParseUA(t *testing.T) {
	tests := []struct {
		name        string
		ua          string
		wantOS      string
		wantBrowser string
		wantDevice  string
	}{
		{
			name:        "chrome on windows",
			ua:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			wantOS:      "Windows 10/11",
			wantBrowser: "Chrome 120.0.0.0",
		},
		{
			name:        "safari on macos",
			ua:          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
			wantOS:      "macOS 10.15.7",
			wantBrowser: "Safari 17.1",
		},
		{
			name:        "safari on iphone",
			ua:          "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			wantOS:      "iOS 17.2",
			wantBrowser: "Safari 17.0",
			wantDevice:  "iPhone",
		},
		{
			name:        "wechat on android with model",
			ua:          "Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/119.0.6045.163 Mobile Safari/537.36 XWEB/1190055 MMWEBID/123 MicroMessenger/8.0.42",
			wantOS:      "Android 14",
			wantBrowser: "WeChat 8.0.42",
			wantDevice:  "Pixel 8 Pro",
		},
		{
			name:        "edge on windows",
			ua:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.2210.91",
			wantOS:      "Windows 10/11",
			wantBrowser: "Edge 120.0.2210.91",
		},
		{
			name:        "firefox on linux",
			ua:          "Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0",
			wantOS:      "Linux",
			wantBrowser: "Firefox 121.0",
		},
		{
			name:        "ipad",
			ua:          "Mozilla/5.0 (iPad; CPU OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1",
			wantOS:      "iOS 16.6",
			wantBrowser: "Safari 16.6",
			wantDevice:  "iPad",
		},
		{
			name:        "grpc client",
			ua:          "grpc-go/1.83.2",
			wantBrowser: "gRPC 1.83.2",
		},
		{
			name:        "okhttp (android app)",
			ua:          "okhttp/4.12.0",
			wantBrowser: "okhttp 4.12.0",
		},
		{
			name:        "go http client",
			ua:          "Go-http-client/2.0",
			wantBrowser: "Go 2.0",
		},
		{
			name:        "python requests",
			ua:          "python-requests/2.31.0",
			wantBrowser: "python-requests 2.31.0",
		},
		{
			name:        "postman",
			ua:          "PostmanRuntime/7.36.0",
			wantBrowser: "Postman 7.36.0",
		},
		{
			name:        "curl",
			ua:          "curl/8.4.0",
			wantBrowser: "curl 8.4.0",
		},
		{
			name: "empty",
			ua:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osName, browser, device := clientinfo.ParseUA(tt.ua)
			require.Equal(t, tt.wantOS, osName)
			require.Equal(t, tt.wantBrowser, browser)
			require.Equal(t, tt.wantDevice, device)
		})
	}
}

func TestIsApiClient(t *testing.T) {
	require.True(t, clientinfo.IsApiClient("okhttp/4.12.0"))
	require.True(t, clientinfo.IsApiClient("Go-http-client/2.0"))
	require.False(t, clientinfo.IsApiClient("Mozilla/5.0 (Macintosh) Chrome/120.0.0.0"))
	require.False(t, clientinfo.IsApiClient(""))
}
