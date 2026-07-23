package helps

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

const (
	OpenAIAuthModeAgentIdentity = "agentIdentity"
	AgentIdentityAuthAPIBaseURL = "https://auth.openai.com/api/accounts"
)

type AgentIdentityTaskRegistrationResponse struct {
	TaskID               string `json:"task_id"`
	TaskIDCamel          string `json:"taskId"`
	EncryptedTaskID      string `json:"encrypted_task_id"`
	EncryptedTaskIDCamel string `json:"encryptedTaskId"`
}

type AgentIdentityKey struct {
	RuntimeID  string
	PrivateKey ed25519.PrivateKey
	TaskID     string
}

func IsAgentIdentityAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(AuthMetadataString(auth, "auth_mode"), OpenAIAuthModeAgentIdentity)
}

func AuthMetadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if value, ok := auth.Metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func AgentIdentityKeyFromAuth(auth *cliproxyauth.Auth) (AgentIdentityKey, error) {
	if auth == nil {
		return AgentIdentityKey{}, errors.New("agent identity auth is nil")
	}
	raw := AuthMetadataString(auth, "agent_private_key")
	if raw == "" {
		return AgentIdentityKey{}, errors.New("agent identity private key is missing")
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return AgentIdentityKey{}, errors.New("agent identity private key is not valid base64")
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return AgentIdentityKey{}, errors.New("agent identity private key is not valid PKCS#8")
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return AgentIdentityKey{}, errors.New("agent identity private key is not Ed25519")
	}
	runtimeID := AuthMetadataString(auth, "agent_runtime_id")
	if runtimeID == "" {
		return AgentIdentityKey{}, errors.New("agent identity runtime id is missing")
	}
	return AgentIdentityKey{RuntimeID: runtimeID, PrivateKey: privateKey, TaskID: AuthMetadataString(auth, "task_id")}, nil
}

func BuildAgentAssertion(key AgentIdentityKey, now time.Time) (string, error) {
	if strings.TrimSpace(key.RuntimeID) == "" || strings.TrimSpace(key.TaskID) == "" {
		return "", errors.New("agent identity runtime or task id is missing")
	}
	timestamp := now.UTC().Format(time.RFC3339)
	payload := []byte(key.RuntimeID + ":" + key.TaskID + ":" + timestamp)
	signature := ed25519.Sign(key.PrivateKey, payload)
	envelope := map[string]string{
		"agent_runtime_id": key.RuntimeID,
		"task_id":          key.TaskID,
		"timestamp":        timestamp,
		"signature":        base64.StdEncoding.EncodeToString(signature),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", errors.New("failed to serialize agent assertion")
	}
	return "AgentAssertion " + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func SignAgentTaskRegistration(key AgentIdentityKey, now time.Time) (string, string, error) {
	if strings.TrimSpace(key.RuntimeID) == "" {
		return "", "", errors.New("agent identity runtime id is missing")
	}
	timestamp := now.UTC().Format(time.RFC3339)
	signature := ed25519.Sign(key.PrivateKey, []byte(key.RuntimeID+":"+timestamp))
	return timestamp, base64.StdEncoding.EncodeToString(signature), nil
}

func DecryptAgentTaskID(key AgentIdentityKey, encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", errors.New("encrypted agent task id is not valid base64")
	}
	digest := sha512.Sum512(key.PrivateKey.Seed())
	var curvePrivate [32]byte
	copy(curvePrivate[:], digest[:32])
	curvePrivate[0] &= 248
	curvePrivate[31] &= 127
	curvePrivate[31] |= 64
	curvePublicBytes, err := curve25519.X25519(curvePrivate[:], curve25519.Basepoint)
	if err != nil {
		return "", errors.New("failed to derive agent identity decryption key")
	}
	var curvePublic [32]byte
	copy(curvePublic[:], curvePublicBytes)
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &curvePublic, &curvePrivate)
	if !ok {
		return "", errors.New("failed to decrypt encrypted agent task id")
	}
	taskID := strings.TrimSpace(string(plaintext))
	if taskID == "" {
		return "", errors.New("decrypted agent task id is empty")
	}
	return taskID, nil
}

func RegisterAgentIdentityTask(ctx context.Context, client *http.Client, baseURL string, auth *cliproxyauth.Auth, now time.Time) (string, error) {
	key, err := AgentIdentityKeyFromAuth(auth)
	if err != nil {
		return "", err
	}
	timestamp, signature, err := SignAgentTaskRegistration(key, now)
	if err != nil {
		return "", err
	}
	if client == nil {
		client = http.DefaultClient
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = AgentIdentityAuthAPIBaseURL
	}
	body, err := json.Marshal(map[string]string{"timestamp": timestamp, "signature": signature})
	if err != nil {
		return "", errors.New("failed to serialize agent task registration")
	}
	url := baseURL + "/v1/agent/" + key.RuntimeID + "/task/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("failed to build agent task registration request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("agent task registration request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("agent task registration returned status %d", resp.StatusCode)
	}
	var result AgentIdentityTaskRegistrationResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return "", errors.New("agent task registration response is invalid")
	}
	if taskID := strings.TrimSpace(result.TaskID); taskID != "" {
		return taskID, nil
	}
	if taskID := strings.TrimSpace(result.TaskIDCamel); taskID != "" {
		return taskID, nil
	}
	encrypted := strings.TrimSpace(result.EncryptedTaskID)
	if encrypted == "" {
		encrypted = strings.TrimSpace(result.EncryptedTaskIDCamel)
	}
	if encrypted == "" {
		return "", errors.New("agent task registration response omitted task id")
	}
	return DecryptAgentTaskID(key, encrypted)
}

func IsAgentIdentityTaskInvalidResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	lower := strings.ToLower(string(body))
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(lower)
	for _, marker := range []string{`"code":"invalid_task_id"`, `"error":"invalid_task_id"`} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	for _, marker := range []string{"invalid task_id", "task_id is invalid", "unknown task_id"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func RedactAgentIdentitySecrets(auth *cliproxyauth.Auth, body []byte) []byte {
	if !IsAgentIdentityAuth(auth) || len(body) == 0 {
		return body
	}
	redacted := string(body)
	for _, key := range []string{"agent_private_key", "id_token", "task_id", "access_token", "refresh_token", "api_key"} {
		if value := AuthMetadataString(auth, key); value != "" {
			redacted = strings.ReplaceAll(redacted, value, "[redacted]")
		}
	}
	const prefix = "AgentAssertion "
	for offset := 0; offset < len(redacted); {
		idx := strings.Index(redacted[offset:], prefix)
		if idx < 0 {
			break
		}
		start := offset + idx + len(prefix)
		end := start
		for end < len(redacted) && !strings.ContainsRune(" \t\r\n\"',}", rune(redacted[end])) {
			end++
		}
		redacted = redacted[:start] + "[redacted]" + redacted[end:]
		offset = start + len("[redacted]")
	}
	return []byte(redacted)
}
