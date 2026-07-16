package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/openaiusage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesIncludesZeroOpenAIUsageForOAuthJSONWithoutHistory(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "codex-user.json")
	if err := os.WriteFile(authPath, []byte(`{"email":"user@example.com"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex-oauth-json",
		Provider: "codex",
		FileName: "codex-user.json",
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			coreauth.AttributePath:     authPath,
			"email":                    "user@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	h.openAIUsageStore = fakeOpenAIUsageStore{}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(payload.Files))
	}

	usageRaw, ok := payload.Files[0]["openai_usage"].(map[string]any)
	if !ok {
		t.Fatalf("openai_usage missing or invalid: %#v", payload.Files[0]["openai_usage"])
	}
	if usageRaw["auth_index"] == "" {
		t.Fatalf("auth_index missing in openai_usage: %#v", usageRaw)
	}
	if usageRaw["auth_file_name"] != "codex-user.json" {
		t.Fatalf("auth_file_name = %#v, want codex-user.json", usageRaw["auth_file_name"])
	}
	if usageRaw["account_email"] != "user@example.com" {
		t.Fatalf("account_email = %#v, want user@example.com", usageRaw["account_email"])
	}
	if usageRaw["request_count"] != float64(0) || usageRaw["estimated_cost_nano_usd"] != float64(0) {
		t.Fatalf("usage counters are not zero: %#v", usageRaw)
	}
	if usageRaw["estimated_cost_usd"] != openaiusage.FormatUSD(0) {
		t.Fatalf("estimated_cost_usd = %#v, want %q", usageRaw["estimated_cost_usd"], openaiusage.FormatUSD(0))
	}
}

func TestListAuthFilesIncludesZeroOpenAIUsageWhenRuntimeFileNameIsEmpty(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "codex-empty-filename.json")
	if err := os.WriteFile(authPath, []byte(`{"email":"user@example.com"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex-empty-filename.json",
		Provider: "codex",
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			coreauth.AttributePath:     authPath,
			"email":                    "user@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	h.openAIUsageStore = fakeOpenAIUsageStore{}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(payload.Files))
	}

	usageRaw, ok := payload.Files[0]["openai_usage"].(map[string]any)
	if !ok {
		t.Fatalf("openai_usage missing or invalid: %#v", payload.Files[0]["openai_usage"])
	}
	if usageRaw["auth_file_name"] != "codex-empty-filename.json" {
		t.Fatalf("auth_file_name = %#v, want codex-empty-filename.json", usageRaw["auth_file_name"])
	}
}

func TestListAuthFilesIncludesZeroOpenAIUsageForOAuthMetadataWithoutExplicitKind(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "codex-oauth-metadata.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"token","email":"user@example.com"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex-oauth-metadata",
		Provider: "codex",
		FileName: "codex-oauth-metadata.json",
		Attributes: map[string]string{
			coreauth.AttributePath: authPath,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"email":        "user@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	h.openAIUsageStore = fakeOpenAIUsageStore{}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(payload.Files))
	}

	usageRaw, ok := payload.Files[0]["openai_usage"].(map[string]any)
	if !ok {
		t.Fatalf("openai_usage missing or invalid: %#v", payload.Files[0]["openai_usage"])
	}
	if usageRaw["request_count"] != float64(0) {
		t.Fatalf("request_count = %#v, want 0", usageRaw["request_count"])
	}
	if usageRaw["estimated_cost_usd"] != openaiusage.FormatUSD(0) {
		t.Fatalf("estimated_cost_usd = %#v, want %q", usageRaw["estimated_cost_usd"], openaiusage.FormatUSD(0))
	}
}

func TestListAuthFilesDoesNotIncludeOpenAIUsageForAPIKeyJSON(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "codex-api-key.json")
	if err := os.WriteFile(authPath, []byte(`{"api_key":"test-key"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex-api-key",
		Provider: "codex",
		FileName: "codex-api-key.json",
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: "test-key",
			coreauth.AttributePath:   authPath,
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	h.openAIUsageStore = fakeOpenAIUsageStore{}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(payload.Files))
	}
	if _, ok := payload.Files[0]["openai_usage"]; ok {
		t.Fatalf("openai_usage present for API key auth: %#v", payload.Files[0]["openai_usage"])
	}
	if payload.Files[0]["account_type"] != "api_key" {
		t.Fatalf("account_type = %#v, want api_key", payload.Files[0]["account_type"])
	}
}
