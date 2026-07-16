package management

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestMarkInvalidAuthFromAPICallRequiresExactInactiveWorkspaceError(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:        "invalid-workspace-auth",
		Provider:  "antigravity",
		Status:    coreauth.StatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := &Handler{authManager: manager}
	body := []byte(`{"error":{"code":"biscuit_baker_service_auth_credential_error_status","message":"user is not an active member of the selected workspace"}}`)
	h.markInvalidAuthFromAPICall(context.Background(), auth, http.StatusForbidden, body)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth %q missing", auth.ID)
	}
	if string(updated.Status) != invalidAuthStatus {
		t.Fatalf("status = %q, want %q", updated.Status, invalidAuthStatus)
	}
	if updated.StatusMessage != invalidAuthReason {
		t.Fatalf("status_message = %q, want %q", updated.StatusMessage, invalidAuthReason)
	}
	if updated.LastError == nil || updated.LastError.Code != invalidAuthErrorCode || updated.LastError.HTTPStatus != http.StatusForbidden {
		t.Fatalf("last_error = %+v, want invalid auth error", updated.LastError)
	}

	auth2 := &coreauth.Auth{
		ID:        "ordinary-403-auth",
		Provider:  "antigravity",
		Status:    coreauth.StatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, errRegister := manager.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}
	h.markInvalidAuthFromAPICall(context.Background(), auth2, http.StatusForbidden, []byte(`{"error":{"code":"forbidden","message":"access denied"}}`))
	unchanged, ok := manager.GetByID(auth2.ID)
	if !ok {
		t.Fatalf("auth %q missing", auth2.ID)
	}
	if unchanged.Status != coreauth.StatusActive || unchanged.StatusMessage != "" {
		t.Fatalf("ordinary 403 changed auth: status=%q status_message=%q", unchanged.Status, unchanged.StatusMessage)
	}
}

func TestMarkInvalidAuthFromAPICallRequiresExactTokenInvalidError(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:        "token-invalid-auth",
		Provider:  "antigravity",
		Status:    coreauth.StatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := &Handler{authManager: manager}
	body := []byte(`{"error":{"message":"` + tokenInvalidAuthMessage + `","type":"authentication_error","code":"auth_unavailable"}}`)
	h.markInvalidAuthFromAPICall(context.Background(), auth, http.StatusUnauthorized, body)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth %q missing", auth.ID)
	}
	if string(updated.Status) != tokenInvalidAuthStatus {
		t.Fatalf("status = %q, want %q", updated.Status, tokenInvalidAuthStatus)
	}
	if updated.StatusMessage != tokenInvalidAuthReason {
		t.Fatalf("status_message = %q, want %q", updated.StatusMessage, tokenInvalidAuthReason)
	}
	if updated.LastError == nil || updated.LastError.Code != tokenInvalidAuthErrorCode || updated.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("last_error = %+v, want token invalid error", updated.LastError)
	}
	if updated.LastError.Message != tokenInvalidAuthMessage {
		t.Fatalf("last_error.message = %q, want %q", updated.LastError.Message, tokenInvalidAuthMessage)
	}

	auth2 := &coreauth.Auth{
		ID:        "ordinary-401-auth",
		Provider:  "antigravity",
		Status:    coreauth.StatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, errRegister := manager.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}
	h.markInvalidAuthFromAPICall(context.Background(), auth2, http.StatusUnauthorized, []byte(`{"error":{"message":"missing token","type":"authentication_error","code":"auth_unavailable"}}`))
	unchanged, ok := manager.GetByID(auth2.ID)
	if !ok {
		t.Fatalf("auth %q missing", auth2.ID)
	}
	if unchanged.Status != coreauth.StatusActive || unchanged.StatusMessage != "" {
		t.Fatalf("ordinary 401 changed auth: status=%q status_message=%q", unchanged.Status, unchanged.StatusMessage)
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}
