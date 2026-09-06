package convert

import (
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/clientinfo"
)

// NewRegisterProfile maps the captured caller environment onto the 1:1
// register-profile row. The UA hash is filled by dal.CreateRegisterProfile.
func NewRegisterProfile(userID int64, ci clientinfo.ClientInfo) *models.UserRegisterProfile {
	return &models.UserRegisterProfile{
		UserID:    userID,
		IP:        ci.IP,
		UserAgent: ci.UserAgent,
		Device:    ci.Device,
	}
}
