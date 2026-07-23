package management

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func testAgentIdentityPrivateKey(t *testing.T) string {
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

func TestSub2APIAgentIdentityWrapperImportCreatesDistinctAuthFiles(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	privateKey := testAgentIdentityPrivateKey(t)
	payload := []byte(`{
  "type":"sub2api-export",
  "accounts":[
    {"name":"same","platform":"openai","type":"oauth","credentials":{"auth_mode":"agentIdentity","agent_private_key":"` + privateKey + `","agent_runtime_id":"runtime-a","workspace_id":"workspace-a","email":"same@example.com","account_id":"acct-a","chatgpt_user_id":"user-a","plan_type":"plus","chatgpt_account_is_fedramp":false}},
    {"name":"same","platform":"openai","type":"oauth","credentials":{"auth_mode":"agentIdentity","agent_private_key":"` + privateKey + `","agent_runtime_id":"runtime-b","workspace_id":"workspace-b","email":"same@example.com","account_id":"acct-b","chatgpt_user_id":"user-b","plan_type":"team"}}
  ]
}`)

	result, err := h.writeUploadedAuthData(context.Background(), "sub2api.json", payload)
	if err != nil {
		t.Fatalf("writeUploadedAuthData: %v", err)
	}
	if !result.IsWrapper || result.Uploaded != 2 || result.Skipped != 0 || len(result.Files) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Files[0] == result.Files[1] {
		t.Fatalf("expected distinct files, got %q", result.Files[0])
	}
	for _, file := range result.Files {
		data, errRead := os.ReadFile(filepath.Join(authDir, file))
		if errRead != nil {
			t.Fatalf("read generated file: %v", errRead)
		}
		var meta map[string]any
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("generated json: %v", err)
		}
		if meta["type"] != "codex" || meta["auth_mode"] != helps.OpenAIAuthModeAgentIdentity {
			t.Fatalf("generated metadata = %#v", meta)
		}
		if meta["agent_private_key"] != privateKey {
			t.Fatal("private key was not preserved in generated credential file")
		}
		if !managerHasFileName(manager, file) {
			t.Fatalf("manager missing auth %q", file)
		}
	}
}

func managerHasFileName(manager *coreauth.Manager, fileName string) bool {
	for _, auth := range manager.List() {
		if auth.FileName == fileName {
			return true
		}
	}
	return false
}

func TestSub2APIAgentIdentityWrapperImportValidation(t *testing.T) {
	privateKey := testAgentIdentityPrivateKey(t)
	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{name: "missing private key", payload: `{"type":"oauth","credentials":{"auth_mode":"agentIdentity","agent_runtime_id":"runtime"}}`, wantErr: "agent_private_key is required"},
		{name: "missing runtime", payload: `{"type":"oauth","credentials":{"auth_mode":"agentIdentity","agent_private_key":"` + privateKey + `"}}`, wantErr: "agent_runtime_id is required"},
		{name: "wrong type", payload: `{"type":"api_key","credentials":{"auth_mode":"agentIdentity","agent_private_key":"` + privateKey + `","agent_runtime_id":"runtime"}}`, wantErr: "account type is not oauth"},
		{name: "missing task allowed", payload: `{"type":"oauth","credentials":{"auth_mode":"agentIdentity","agent_private_key":"` + privateKey + `","agent_runtime_id":"runtime"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var account sub2APIExportAccount
			if err := json.Unmarshal([]byte(tt.payload), &account); err != nil {
				t.Fatalf("unmarshal account: %v", err)
			}
			_, data, err := buildSub2APIAgentIdentityAuthFile("import.json", 0, account)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !json.Valid(data) {
				t.Fatalf("invalid json: %s", data)
			}
			if got := string(data); !containsJSONField(got, `"task_id": ""`) {
				t.Fatalf("task_id missing or non-empty: %s", data)
			}
		})
	}
}

func TestUploadRawOAuthAndAPIKeyJSONRemainSingleFile(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "codex-oauth.json", body: []byte(`{"type":"codex","email":"user@example.com","access_token":"token"}`)},
		{name: "codex-key.json", body: []byte(`{"type":"codex","auth_kind":"api_key","api_key":"key"}`)},
	} {
		result, err := h.writeUploadedAuthData(context.Background(), tc.name, tc.body)
		if err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
		if result.IsWrapper || result.Uploaded != 1 || len(result.Files) != 1 || result.Files[0] != tc.name {
			t.Fatalf("result for %s = %+v", tc.name, result)
		}
		if _, errStat := os.Stat(filepath.Join(authDir, tc.name)); errStat != nil {
			t.Fatalf("expected %s to exist: %v", tc.name, errStat)
		}
	}
}

func containsJSONField(body string, field string) bool {
	return len(body) >= len(field) && json.Valid([]byte(body)) && strings.Contains(body, field)
}
