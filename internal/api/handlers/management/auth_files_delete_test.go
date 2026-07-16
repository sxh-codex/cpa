package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestDeleteAuthFile_UsesAuthPathFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auth")
	externalDir := filepath.Join(tempDir, "external")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}
	if errMkdirExternal := os.MkdirAll(externalDir, 0o700); errMkdirExternal != nil {
		t.Fatalf("failed to create external dir: %v", errMkdirExternal)
	}

	fileName := "codex-user@example.com-plus.json"
	shadowPath := filepath.Join(authDir, fileName)
	realPath := filepath.Join(externalDir, fileName)
	if errWriteShadow := os.WriteFile(shadowPath, []byte(`{"type":"codex","email":"shadow@example.com"}`), 0o600); errWriteShadow != nil {
		t.Fatalf("failed to write shadow file: %v", errWriteShadow)
	}
	if errWriteReal := os.WriteFile(realPath, []byte(`{"type":"codex","email":"real@example.com"}`), 0o600); errWriteReal != nil {
		t.Fatalf("failed to write real file: %v", errWriteReal)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:          "legacy/" + fileName,
		FileName:    fileName,
		Provider:    "codex",
		Status:      coreauth.StatusError,
		Unavailable: true,
		Attributes: map[string]string{
			"path": realPath,
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "real@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
	deleteCtx.Request = deleteReq
	h.DeleteAuthFile(deleteCtx)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	}
	if _, errStatReal := os.Stat(realPath); !os.IsNotExist(errStatReal) {
		t.Fatalf("expected managed auth file to be removed, stat err: %v", errStatReal)
	}
	if _, errStatShadow := os.Stat(shadowPath); errStatShadow != nil {
		t.Fatalf("expected shadow auth file to remain, stat err: %v", errStatShadow)
	}

	listRec := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRec)
	listReq := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	listCtx.Request = listReq
	h.ListAuthFiles(listCtx)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var listPayload map[string]any
	if errUnmarshal := json.Unmarshal(listRec.Body.Bytes(), &listPayload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	filesRaw, ok := listPayload["files"].([]any)
	if !ok {
		t.Fatalf("expected files array, payload: %#v", listPayload)
	}
	if len(filesRaw) != 0 {
		t.Fatalf("expected removed auth to be hidden from list, got %d entries", len(filesRaw))
	}
}

func TestRemoveInvalidAuthFilesUsesConfiguredAuthDir(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auth")
	targetDir := filepath.Join(authDir, "removed", "invalid")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	invalidName := "invalid-user.json"
	activeName := "active-user.json"
	invalidPath := filepath.Join(authDir, invalidName)
	activePath := filepath.Join(authDir, activeName)
	if errWrite := os.WriteFile(invalidPath, []byte(`{"type":"antigravity"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write invalid auth file: %v", errWrite)
	}
	if errWrite := os.WriteFile(activePath, []byte(`{"type":"antigravity"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write active auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	invalidAuth := &coreauth.Auth{
		ID:            "invalid-auth",
		FileName:      invalidName,
		Provider:      "antigravity",
		Status:        coreauth.Status(invalidAuthStatus),
		StatusMessage: invalidAuthReason,
		Attributes: map[string]string{
			"path": invalidPath,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	activeAuth := &coreauth.Auth{
		ID:       "active-auth",
		FileName: activeName,
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": activePath,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, errRegister := manager.Register(context.Background(), invalidAuth); errRegister != nil {
		t.Fatalf("failed to register invalid auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), activeAuth); errRegister != nil {
		t.Fatalf("failed to register active auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/remove-invalid", nil)
	h.RemoveInvalidAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected remove-invalid status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Removed   int      `json:"removed"`
		TargetDir string   `json:"target_dir"`
		Files     []string `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode remove-invalid payload: %v", errUnmarshal)
	}
	if payload.Removed != 1 {
		t.Fatalf("removed = %d, want 1", payload.Removed)
	}
	if payload.TargetDir != targetDir {
		t.Fatalf("target_dir = %q, want %q", payload.TargetDir, targetDir)
	}
	if len(payload.Files) != 1 || !strings.HasPrefix(payload.Files[0], invalidName+".removed-") {
		t.Fatalf("files = %#v, want moved invalid file", payload.Files)
	}
	if _, errStat := os.Stat(invalidPath); !os.IsNotExist(errStat) {
		t.Fatalf("expected invalid auth source file to be moved, stat err: %v", errStat)
	}
	if _, errStat := os.Stat(filepath.Join(targetDir, payload.Files[0])); errStat != nil {
		t.Fatalf("expected moved invalid auth file in target dir: %v", errStat)
	}
	if _, errStat := os.Stat(activePath); errStat != nil {
		t.Fatalf("expected active auth source file to remain: %v", errStat)
	}
	if _, ok := manager.GetByID(invalidAuth.ID); ok {
		t.Fatalf("expected invalid runtime auth to be removed")
	}
	if _, ok := manager.GetByID(activeAuth.ID); !ok {
		t.Fatalf("expected active runtime auth to remain")
	}
}

func TestRemove401InvalidAuthFilesUsesConfiguredAuthDir(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auth")
	targetDir := filepath.Join(authDir, "removed", "401-invalid")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	tokenInvalidName := "token-invalid-user.json"
	invalidName := "invalid-workspace-user.json"
	activeName := "active-user.json"
	tokenInvalidPath := filepath.Join(authDir, tokenInvalidName)
	invalidPath := filepath.Join(authDir, invalidName)
	activePath := filepath.Join(authDir, activeName)
	if errWrite := os.WriteFile(tokenInvalidPath, []byte(`{"type":"antigravity"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write token invalid auth file: %v", errWrite)
	}
	if errWrite := os.WriteFile(invalidPath, []byte(`{"type":"antigravity"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write invalid workspace auth file: %v", errWrite)
	}
	if errWrite := os.WriteFile(activePath, []byte(`{"type":"antigravity"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write active auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	tokenInvalidAuth := &coreauth.Auth{
		ID:            "token-invalid-auth",
		FileName:      tokenInvalidName,
		Provider:      "antigravity",
		Status:        coreauth.Status(tokenInvalidAuthStatus),
		StatusMessage: tokenInvalidAuthReason,
		Attributes: map[string]string{
			"path": tokenInvalidPath,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	invalidAuth := &coreauth.Auth{
		ID:            "invalid-workspace-auth",
		FileName:      invalidName,
		Provider:      "antigravity",
		Status:        coreauth.Status(invalidAuthStatus),
		StatusMessage: invalidAuthReason,
		Attributes: map[string]string{
			"path": invalidPath,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	activeAuth := &coreauth.Auth{
		ID:       "active-auth",
		FileName: activeName,
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": activePath,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, errRegister := manager.Register(context.Background(), tokenInvalidAuth); errRegister != nil {
		t.Fatalf("failed to register token invalid auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), invalidAuth); errRegister != nil {
		t.Fatalf("failed to register invalid workspace auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), activeAuth); errRegister != nil {
		t.Fatalf("failed to register active auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/remove-401-invalid", nil)
	h.Remove401InvalidAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected remove-401-invalid status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Removed   int      `json:"removed"`
		TargetDir string   `json:"target_dir"`
		Files     []string `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode remove-401-invalid payload: %v", errUnmarshal)
	}
	if payload.Removed != 1 {
		t.Fatalf("removed = %d, want 1", payload.Removed)
	}
	if payload.TargetDir != targetDir {
		t.Fatalf("target_dir = %q, want %q", payload.TargetDir, targetDir)
	}
	if len(payload.Files) != 1 || !strings.HasPrefix(payload.Files[0], tokenInvalidName+".removed-") {
		t.Fatalf("files = %#v, want moved token invalid file", payload.Files)
	}
	if _, errStat := os.Stat(tokenInvalidPath); !os.IsNotExist(errStat) {
		t.Fatalf("expected token invalid auth source file to be moved, stat err: %v", errStat)
	}
	if _, errStat := os.Stat(filepath.Join(targetDir, payload.Files[0])); errStat != nil {
		t.Fatalf("expected moved token invalid auth file in target dir: %v", errStat)
	}
	if _, errStat := os.Stat(invalidPath); errStat != nil {
		t.Fatalf("expected invalid workspace auth source file to remain: %v", errStat)
	}
	if _, errStat := os.Stat(activePath); errStat != nil {
		t.Fatalf("expected active auth source file to remain: %v", errStat)
	}
	if _, ok := manager.GetByID(tokenInvalidAuth.ID); ok {
		t.Fatalf("expected token invalid runtime auth to be removed")
	}
	if _, ok := manager.GetByID(invalidAuth.ID); !ok {
		t.Fatalf("expected invalid workspace runtime auth to remain")
	}
	if _, ok := manager.GetByID(activeAuth.ID); !ok {
		t.Fatalf("expected active runtime auth to remain")
	}
}

func TestDeleteAuthFile_FallbackToAuthDirPath(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "fallback-user.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
	deleteCtx.Request = deleteReq
	h.DeleteAuthFile(deleteCtx)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	}
	if _, errStat := os.Stat(filePath); !os.IsNotExist(errStat) {
		t.Fatalf("expected auth file to be removed from auth dir, stat err: %v", errStat)
	}
}

func TestDeleteAuthFile_RemovesRuntimeAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "runtime-remove-user.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","email":"runtime@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "runtime-remove-auth",
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "runtime@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
	deleteCtx.Request = deleteReq
	h.DeleteAuthFile(deleteCtx)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	}
	if _, ok := manager.GetByID(record.ID); ok {
		t.Fatalf("expected runtime auth %q to be removed", record.ID)
	}
}
