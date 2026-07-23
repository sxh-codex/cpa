package executor

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyCodexHeadersOAuthKeepsBearerAndDropsHopByHopHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	auth := &cliproxyauth.Auth{Provider: "codex"}

	if err := applyCodexHeaders(req, auth, "oauth-token", true, nil); err != nil {
		t.Fatalf("applyCodexHeaders: %v", err)
	}

	if got := req.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want Bearer oauth-token", got)
	}
	assertCodexHopByHopHeadersEmpty(t, req.Header)
}

func TestApplyCodexHeadersDropsCustomHopByHopHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"header:Connection":        "Keep-Alive",
			"header:Keep-Alive":        "timeout=30",
			"header:Proxy-Connection":  "Keep-Alive",
			"header:Transfer-Encoding": "chunked",
			"header:Upgrade":           "websocket",
		},
	}

	if err := applyCodexHeaders(req, auth, "oauth-token", false, nil); err != nil {
		t.Fatalf("applyCodexHeaders: %v", err)
	}

	assertCodexHopByHopHeadersEmpty(t, req.Header)
}

func TestApplyCodexHeadersAgentIdentityUsesAgentAssertion(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	encodedPrivateKey := testCodexHTTPAgentIdentityPrivateKey(t)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"auth_mode":         "agentIdentity",
			"agent_runtime_id":  "runtime-test",
			"agent_private_key": encodedPrivateKey,
			"task_id":           "task-test",
		},
	}

	if err := applyCodexHeaders(req, auth, "bearer-token-should-not-be-used", true, nil); err != nil {
		t.Fatalf("applyCodexHeaders: %v", err)
	}

	authorization := req.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "AgentAssertion ") {
		t.Fatal("Authorization prefix mismatch")
	}
	if authorization == "Bearer bearer-token-should-not-be-used" {
		t.Fatal("Authorization used Bearer token for Agent Identity")
	}
	if strings.Contains(authorization, encodedPrivateKey) {
		t.Fatal("Authorization contains private key")
	}
	assertCodexHopByHopHeadersEmpty(t, req.Header)
}

func TestApplyCodexHeadersAgentIdentityMissingCredentialReturnsError(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"auth_mode":        "agentIdentity",
			"agent_runtime_id": "runtime-test",
			"task_id":          "task-test",
		},
	}

	if err := applyCodexHeaders(req, auth, "bearer-token-should-not-be-used", true, nil); err == nil {
		t.Fatal("expected error")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty on error", got)
	}
}

func assertCodexHopByHopHeadersEmpty(t *testing.T, headers http.Header) {
	t.Helper()
	for _, name := range []string{"Connection", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding", "Upgrade"} {
		if got := headers.Get(name); got != "" {
			t.Fatalf("%s = %q, want empty", name, got)
		}
	}
}

func testCodexHTTPAgentIdentityPrivateKey(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	return base64.StdEncoding.EncodeToString(der)
}
