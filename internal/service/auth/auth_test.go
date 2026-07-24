package auth

import (
	"testing"

	pb "github.com/servekit/user-service/gen/user/v1"

	"github.com/stretchr/testify/require"
)

func TestResolveLoginTarget(t *testing.T) {
	tests := []struct {
		name       string
		method     pb.LoginMethod
		username   string
		email      string
		rc         string
		phone      string
		wantLookup string
		wantKey    string
	}{
		{
			name:       "username password",
			method:     pb.LoginMethod_LOGIN_METHOD_USERNAME_PASSWORD,
			username:   "alice",
			wantLookup: "alice",
			wantKey:    "",
		},
		{
			name:       "email password",
			method:     pb.LoginMethod_LOGIN_METHOD_EMAIL_PASSWORD,
			email:      "u@example.com",
			wantLookup: "u@example.com",
			wantKey:    "u@example.com",
		},
		{
			name:       "email code",
			method:     pb.LoginMethod_LOGIN_METHOD_EMAIL_CODE,
			email:      "u@example.com",
			wantLookup: "u@example.com",
			wantKey:    "u@example.com",
		},
		{
			name:       "phone password normalizes inputs",
			method:     pb.LoginMethod_LOGIN_METHOD_PHONE_PASSWORD,
			rc:         "cn", // lowercase gets uppercased
			phone:      " 13800138000 ",
			wantLookup: "CN|13800138000",
			wantKey:    "CN|13800138000",
		},
		{
			name:       "phone code",
			method:     pb.LoginMethod_LOGIN_METHOD_PHONE_CODE,
			rc:         "US",
			phone:      "5551234567",
			wantLookup: "US|5551234567",
			wantKey:    "US|5551234567",
		},
		{
			name:       "unspecified method returns empty",
			method:     pb.LoginMethod_LOGIN_METHOD_UNSPECIFIED,
			wantLookup: "",
			wantKey:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &pb.LoginRequest{
				Method:     tt.method,
				Username:   tt.username,
				Email:      tt.email,
				RegionCode: tt.rc,
				Phone:      tt.phone,
			}
			lookup, key := resolveLoginTarget(req)
			require.Equal(t, tt.wantLookup, lookup, "lookup target")
			require.Equal(t, tt.wantKey, key, "captcha key")
		})
	}
}

func TestSplitPhoneKey(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		wantRC string
		wantP  string
	}{
		{name: "happy path", key: "CN|13800138000", wantRC: "CN", wantP: "13800138000"},
		{name: "no separator returns empty rc", key: "13800138000", wantRC: "", wantP: "13800138000"},
		{name: "empty rc side", key: "|13800138000", wantRC: "", wantP: "13800138000"},
		{name: "empty phone side", key: "CN|", wantRC: "CN", wantP: ""},
		{name: "empty key", key: "", wantRC: "", wantP: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, p := splitPhoneKey(tt.key)
			require.Equal(t, tt.wantRC, rc, "region code")
			require.Equal(t, tt.wantP, p, "phone")
		})
	}
}

func TestValidateDeliverySpec(t *testing.T) {
	tests := []struct {
		name    string
		req     *pb.SendVerificationCodeRequest
		wantErr string // empty = expect no error
	}{
		// Email channel — both fields required.
		{
			name:    "email missing subject",
			req:     &pb.SendVerificationCodeRequest{Channel: pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL, EmailBody: "body"},
			wantErr: "email_subject and email_body are required",
		},
		{
			name:    "email missing body",
			req:     &pb.SendVerificationCodeRequest{Channel: pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL, EmailSubject: "subj"},
			wantErr: "email_subject and email_body are required",
		},
		{
			name: "email happy path",
			req: &pb.SendVerificationCodeRequest{
				Channel:      pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL,
				EmailSubject: "subj",
				EmailBody:    "body",
			},
		},
		// SMS + CN — template_id + sign_name required.
		{
			name: "CN sms missing template_id",
			req: &pb.SendVerificationCodeRequest{
				Channel:    pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode: "CN",
				SignName:   "sign",
			},
			wantErr: "sms_template_id is required for CN",
		},
		{
			name: "CN sms missing sign_name",
			req: &pb.SendVerificationCodeRequest{
				Channel:       pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode:    "CN",
				SmsTemplateId: "SMS_123",
			},
			wantErr: "sign_name is required for CN",
		},
		{
			name: "CN sms provides content instead of template",
			req: &pb.SendVerificationCodeRequest{
				Channel:    pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode: "CN",
				SmsContent: "Your code is 1234",
				SignName:   "sign",
			},
			wantErr: "sms_template_id is required for CN",
		},
		{
			name: "CN sms happy path",
			req: &pb.SendVerificationCodeRequest{
				Channel:         pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode:      "CN",
				SmsTemplateId:   "SMS_123",
				SmsCodeParamKey: "code",
				SignName:        "sign",
			},
		},
		// SMS + international — exactly one of content or template required.
		{
			name: "intl sms missing both content and template",
			req: &pb.SendVerificationCodeRequest{
				Channel:    pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode: "US",
			},
			wantErr: "international (non-CN) SMS requires exactly one",
		},
		{
			name: "intl sms sets both content and template",
			req: &pb.SendVerificationCodeRequest{
				Channel:       pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode:    "US",
				SmsContent:    "Your code is {code}",
				SmsTemplateId: "SMS_123",
			},
			wantErr: "international (non-CN) SMS requires exactly one",
		},
		{
			name: "intl sms with content (raw-content vendor)",
			req: &pb.SendVerificationCodeRequest{
				Channel:    pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode: "US",
				SmsContent: "Your code is {code}",
			},
		},
		{
			name: "intl sms with template (template-based intl vendor)",
			req: &pb.SendVerificationCodeRequest{
				Channel:       pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
				RegionCode:    "US",
				SmsTemplateId: "SMS_123",
			},
		},
		// SMS with empty region — deferred to downstream target validation.
		{
			name: "sms no region yet defers",
			req: &pb.SendVerificationCodeRequest{
				Channel: pb.VerificationChannel_VERIFICATION_CHANNEL_SMS,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeliverySpec(tt.req)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
