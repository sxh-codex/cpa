package auth

import (
	"context"
	"net/http"
	"testing"
)

func TestManager_MarkResultInvalidWorkspaceAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "invalid-workspace-auth",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	upstreamMessage := "Personal access token owner is not an active member of the selected workspace."
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5",
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusForbidden,
			Message:    `{"error":{"message":"` + upstreamMessage + `","type":null,"code":"` + invalidWorkspaceAuthErrorCode + `","param":null},"status":403}`,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if string(updated.Status) != invalidWorkspaceAuthStatus {
		t.Fatalf("status = %q, want %q", updated.Status, invalidWorkspaceAuthStatus)
	}
	if updated.StatusMessage != invalidWorkspaceAuthReason {
		t.Fatalf("status_message = %q, want %q", updated.StatusMessage, invalidWorkspaceAuthReason)
	}
	if updated.LastError == nil {
		t.Fatalf("last_error = nil")
	}
	if updated.LastError.Code != invalidWorkspaceAuthErrorCode {
		t.Fatalf("last_error.code = %q, want %q", updated.LastError.Code, invalidWorkspaceAuthErrorCode)
	}
	if updated.LastError.Message != upstreamMessage {
		t.Fatalf("last_error.message = %q, want %q", updated.LastError.Message, upstreamMessage)
	}
	if updated.LastError.HTTPStatus != http.StatusForbidden {
		t.Fatalf("last_error.http_status = %d, want %d", updated.LastError.HTTPStatus, http.StatusForbidden)
	}
}

func TestManager_MarkResultOrdinary403DoesNotMarkInvalidWorkspaceAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "ordinary-403-auth",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5",
		Success:  false,
		Error: &Error{
			Code:       "forbidden",
			Message:    "access denied",
			HTTPStatus: http.StatusForbidden,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if string(updated.Status) == invalidWorkspaceAuthStatus {
		t.Fatalf("ordinary 403 was marked as %q", invalidWorkspaceAuthStatus)
	}
	if updated.StatusMessage == invalidWorkspaceAuthReason {
		t.Fatalf("ordinary 403 got invalid workspace reason")
	}
}

func TestManager_MarkResultTokenInvalidAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "token-invalid-auth",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5",
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusUnauthorized,
			Message:    `{"error":{"message":"` + tokenInvalidAuthMessage + `","type":"` + tokenInvalidAuthErrorType + `","code":"` + tokenInvalidAuthErrorCode + `"}}`,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if string(updated.Status) != tokenInvalidAuthStatus {
		t.Fatalf("status = %q, want %q", updated.Status, tokenInvalidAuthStatus)
	}
	if updated.StatusMessage != tokenInvalidAuthReason {
		t.Fatalf("status_message = %q, want %q", updated.StatusMessage, tokenInvalidAuthReason)
	}
	if updated.LastError == nil {
		t.Fatalf("last_error = nil")
	}
	if updated.LastError.Code != tokenInvalidAuthErrorCode {
		t.Fatalf("last_error.code = %q, want %q", updated.LastError.Code, tokenInvalidAuthErrorCode)
	}
	if updated.LastError.Message != tokenInvalidAuthMessage {
		t.Fatalf("last_error.message = %q, want %q", updated.LastError.Message, tokenInvalidAuthMessage)
	}
	if updated.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("last_error.http_status = %d, want %d", updated.LastError.HTTPStatus, http.StatusUnauthorized)
	}
}

func TestManager_MarkResultOrdinary401DoesNotMarkTokenInvalidAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "ordinary-401-auth",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5",
		Success:  false,
		Error: &Error{
			Code:       tokenInvalidAuthErrorCode,
			Message:    tokenInvalidAuthMessage,
			HTTPStatus: http.StatusUnauthorized,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if string(updated.Status) == tokenInvalidAuthStatus {
		t.Fatalf("ordinary 401 was marked as %q", tokenInvalidAuthStatus)
	}
	if updated.StatusMessage == tokenInvalidAuthReason {
		t.Fatalf("ordinary 401 got token invalid reason")
	}
}
