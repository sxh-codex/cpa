package helps

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

func newTestAgentIdentityAuth(t *testing.T) (*cliproxyauth.Auth, ed25519.PrivateKey, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(der)
	return &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{
		"auth_mode":         OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":  "runtime-test",
		"agent_private_key": encoded,
		"task_id":           "task-test",
	}}, privateKey, encoded
}

func TestBuildAgentAssertionUsesRFC3339TimestampAndRuntimeTaskPayload(t *testing.T) {
	auth, privateKey, encodedKey := newTestAgentIdentityAuth(t)
	key, err := AgentIdentityKeyFromAuth(auth)
	if err != nil {
		t.Fatalf("AgentIdentityKeyFromAuth: %v", err)
	}
	now := time.Date(2026, 7, 23, 9, 10, 11, 0, time.FixedZone("UTC+8", 8*60*60))
	assertion, err := BuildAgentAssertion(key, now)
	if err != nil {
		t.Fatalf("BuildAgentAssertion: %v", err)
	}
	if !strings.HasPrefix(assertion, "AgentAssertion ") {
		t.Fatalf("assertion prefix = %q", assertion)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(assertion, "AgentAssertion "))
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	if strings.Contains(string(decoded), encodedKey) {
		t.Fatal("assertion leaked private key")
	}
	var envelope struct {
		RuntimeID string `json:"agent_runtime_id"`
		TaskID    string `json:"task_id"`
		Timestamp string `json:"timestamp"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if envelope.Timestamp != "2026-07-23T01:10:11Z" {
		t.Fatalf("timestamp = %q", envelope.Timestamp)
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil {
		t.Fatalf("signature decode: %v", err)
	}
	if !ed25519.Verify(privateKey.Public().(ed25519.PublicKey), []byte("runtime-test:task-test:2026-07-23T01:10:11Z"), signature) {
		t.Fatal("signature did not verify runtimeID:taskID:timestamp payload")
	}
}

func TestSignAgentTaskRegistrationUsesRuntimeTimestampPayload(t *testing.T) {
	auth, privateKey, _ := newTestAgentIdentityAuth(t)
	key, err := AgentIdentityKeyFromAuth(auth)
	if err != nil {
		t.Fatalf("AgentIdentityKeyFromAuth: %v", err)
	}
	timestamp, signatureRaw, err := SignAgentTaskRegistration(key, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SignAgentTaskRegistration: %v", err)
	}
	signature, err := base64.StdEncoding.DecodeString(signatureRaw)
	if err != nil {
		t.Fatalf("signature decode: %v", err)
	}
	if !ed25519.Verify(privateKey.Public().(ed25519.PublicKey), []byte("runtime-test:"+timestamp), signature) {
		t.Fatal("registration signature did not verify runtimeID:timestamp payload")
	}
}

func TestRegisterAgentIdentityTaskAcceptsPlainAndEncryptedTaskID(t *testing.T) {
	auth, privateKey, _ := newTestAgentIdentityAuth(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/agent/runtime-test/task/register" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Fatalf("headers = %#v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "timestamp") || !strings.Contains(string(body), "signature") {
			t.Fatalf("body = %s", body)
		}
		if requests == 1 {
			_, _ = w.Write([]byte(`{"task_id":"task-plain"}`))
			return
		}
		digest := sha512.Sum512(privateKey.Seed())
		var curvePrivate [32]byte
		copy(curvePrivate[:], digest[:32])
		curvePrivate[0] &= 248
		curvePrivate[31] &= 127
		curvePrivate[31] |= 64
		curvePublicBytes, err := curve25519.X25519(curvePrivate[:], curve25519.Basepoint)
		if err != nil {
			t.Fatalf("X25519: %v", err)
		}
		var curvePublic [32]byte
		copy(curvePublic[:], curvePublicBytes)
		ciphertext, err := box.SealAnonymous(nil, []byte("task-encrypted"), &curvePublic, rand.Reader)
		if err != nil {
			t.Fatalf("SealAnonymous: %v", err)
		}
		_, _ = w.Write([]byte(`{"encryptedTaskId":"` + base64.StdEncoding.EncodeToString(ciphertext) + `"}`))
	}))
	defer server.Close()

	got, err := RegisterAgentIdentityTask(context.Background(), server.Client(), server.URL, auth, time.Now())
	if err != nil || got != "task-plain" {
		t.Fatalf("plain task = %q, err=%v", got, err)
	}
	got, err = RegisterAgentIdentityTask(context.Background(), server.Client(), server.URL, auth, time.Now())
	if err != nil || got != "task-encrypted" {
		t.Fatalf("encrypted task = %q, err=%v", got, err)
	}
}

func TestIsAgentIdentityTaskInvalidResponseIsNarrow(t *testing.T) {
	valid := [][]byte{
		[]byte(`{"error":{"code":"invalid_task_id"}}`),
		[]byte(`{"error":"invalid_task_id"}`),
		[]byte(`invalid task_id`),
		[]byte(`task_id is invalid`),
		[]byte(`unknown task_id`),
	}
	for _, body := range valid {
		if !IsAgentIdentityTaskInvalidResponse(http.StatusUnauthorized, body) {
			t.Fatalf("expected invalid task match for %s", body)
		}
	}
	if IsAgentIdentityTaskInvalidResponse(http.StatusUnauthorized, []byte(`{"error":{"code":"auth_unavailable"}}`)) {
		t.Fatal("ordinary 401 matched invalid task")
	}
	if IsAgentIdentityTaskInvalidResponse(http.StatusForbidden, []byte(`{"error":{"code":"invalid_task_id"}}`)) {
		t.Fatal("non-401 matched invalid task")
	}
}
