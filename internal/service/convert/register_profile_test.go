package convert_test

import (
	"testing"

	"github.com/servekit/user-service/internal/service/convert"
	"github.com/servekit/user-service/pkg/clientinfo"
)

func TestNewRegisterProfile_MapsClientInfo(t *testing.T) {
	ci := clientinfo.ClientInfo{
		IP:        "203.0.113.7",
		UserAgent: "Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) Chrome/152.0.0.0 Mobile Safari/537.36",
		Device:    "Pixel 8 Pro",
	}
	p := convert.NewRegisterProfile(42, ci)
	if p.UserID != 42 || p.IP != ci.IP || p.UserAgent != ci.UserAgent || p.Device != ci.Device {
		t.Fatalf("profile = %+v, want clientinfo mapped verbatim", p)
	}
	// Hash is the DAL's job (CreateRegisterProfile), not the constructor's.
	if p.UserAgentHash != "" {
		t.Fatalf("constructor must not pre-fill hash, got %q", p.UserAgentHash)
	}
}
