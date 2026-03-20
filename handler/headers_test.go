package handler_test

import (
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

func TestHeaderConstants_NotEmpty(t *testing.T) {
	headers := []struct {
		name  string
		value string
	}{
		{"HeaderAppID", handler.HeaderAppID},
		{"HeaderSchemaName", handler.HeaderSchemaName},
		{"HeaderAppPublicID", handler.HeaderAppPublicID},
		{"HeaderRequestID", handler.HeaderRequestID},
		{"HeaderPlatformUserID", handler.HeaderPlatformUserID},
		{"HeaderPlatformUserPublicID", handler.HeaderPlatformUserPublicID},
		{"HeaderModuleID", handler.HeaderModuleID},
		{"HeaderAuthType", handler.HeaderAuthType},
		{"HeaderInternalSecret", handler.HeaderInternalSecret},
	}

	for _, h := range headers {
		if h.value == "" {
			t.Errorf("%s is empty", h.name)
		}
	}
}

func TestHeaderConstants_HaveXMSPrefix(t *testing.T) {
	xmsHeaders := []struct {
		name  string
		value string
	}{
		{"HeaderAppID", handler.HeaderAppID},
		{"HeaderSchemaName", handler.HeaderSchemaName},
		{"HeaderAppPublicID", handler.HeaderAppPublicID},
		{"HeaderRequestID", handler.HeaderRequestID},
		{"HeaderPlatformUserID", handler.HeaderPlatformUserID},
		{"HeaderPlatformUserPublicID", handler.HeaderPlatformUserPublicID},
		{"HeaderModuleID", handler.HeaderModuleID},
		{"HeaderAuthType", handler.HeaderAuthType},
	}

	for _, h := range xmsHeaders {
		if !strings.HasPrefix(h.value, "X-MS-") {
			t.Errorf("%s = %q, expected X-MS- prefix", h.name, h.value)
		}
	}
}

func TestHeaderConstants_NoDuplicates(t *testing.T) {
	all := []string{
		handler.HeaderAppID,
		handler.HeaderSchemaName,
		handler.HeaderAppPublicID,
		handler.HeaderRequestID,
		handler.HeaderPlatformUserID,
		handler.HeaderPlatformUserPublicID,
		handler.HeaderModuleID,
		handler.HeaderAuthType,
		handler.HeaderInternalSecret,
	}

	seen := make(map[string]bool)
	for _, v := range all {
		if seen[v] {
			t.Errorf("duplicate header constant: %q", v)
		}
		seen[v] = true
	}
}

func TestAuthTypeConstants_ValidValues(t *testing.T) {
	types := []struct {
		name  string
		value string
	}{
		{"AuthTypePlatform", handler.AuthTypePlatform},
		{"AuthTypeAnonymous", handler.AuthTypeAnonymous},
		{"AuthTypeInternal", handler.AuthTypeInternal},
	}

	for _, at := range types {
		if at.value == "" {
			t.Errorf("%s is empty", at.name)
		}
	}
}
