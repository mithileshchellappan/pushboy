package fcm

import (
	"strings"
	"testing"
)

func TestParseFCMError(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		fallback      string
		wantContains  string
		rejectContain string
	}{
		{
			name: "unregistered detail",
			body: `{
				"error": {
					"code": 404,
					"message": "Requested entity was not found.",
					"status": "NOT_FOUND",
					"details": [{"@type": "type.googleapis.com/google.firebase.fcm.v1.FcmError", "errorCode": "UNREGISTERED"}]
				}
			}`,
			fallback:     "404 Not Found",
			wantContains: "registration-token-not-registered",
		},
		{
			name:         "unregistered detail case insensitive",
			body:         `{"error":{"details":[{"errorCode":"unregistered"}]}}`,
			fallback:     "404 Not Found",
			wantContains: "registration-token-not-registered",
		},
		{
			name:          "message only is generic fcm error",
			body:          `{"error":{"code":503,"message":"backend unavailable","status":"UNAVAILABLE"}}`,
			fallback:      "503 Service Unavailable",
			wantContains:  "backend unavailable",
			rejectContain: "registration-token-not-registered",
		},
		{
			name:         "malformed json falls back",
			body:         `{`,
			fallback:     "500 Internal Server Error",
			wantContains: "failed to send notification: 500 Internal Server Error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseFCMError([]byte(tt.body), tt.fallback)
			if err == nil {
				t.Fatalf("parseFCMError error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("parseFCMError error = %q, want containing %q", err, tt.wantContains)
			}
			if tt.rejectContain != "" && strings.Contains(err.Error(), tt.rejectContain) {
				t.Fatalf("parseFCMError error = %q, should not contain %q", err, tt.rejectContain)
			}
		})
	}
}
