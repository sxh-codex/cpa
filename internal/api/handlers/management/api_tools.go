package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/openaiusage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const defaultAPICallTimeout = 60 * time.Second

const (
	antigravityOAuthClientIDEnv     = "ANTIGRAVITY_OAUTH_CLIENT_ID"
	antigravityOAuthClientSecretEnv = "ANTIGRAVITY_OAUTH_CLIENT_SECRET"
)

var antigravityOAuthTokenURL = "https://oauth2.googleapis.com/token"

type apiCallRequest struct {
	AuthIndexSnake  *string           `json:"auth_index"`
	AuthIndexCamel  *string           `json:"authIndex"`
	AuthIndexPascal *string           `json:"AuthIndex"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Header          map[string]string `json:"header"`
	Data            string            `json:"data"`
}

type apiCallResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
}

// APICall makes a generic HTTP request on behalf of the management API caller.
// It is protected by the management middleware.
//
// Endpoint:
//
//	POST /v0/management/api-call
//
// Authentication:
//
//	Same as other management APIs (requires a management key and remote-management rules).
//	You can provide the key via:
//	- Authorization: Bearer <key>
//	- X-Management-Key: <key>
//
// Request JSON:
//   - auth_index / authIndex / AuthIndex (optional):
//     The credential "auth_index" from GET /v0/management/auth-files (or other endpoints returning it).
//     If omitted or not found, credential-specific proxy/token substitution is skipped.
//   - method (required): HTTP method, e.g. GET, POST, PUT, PATCH, DELETE.
//   - url (required): Absolute URL including scheme and host, e.g. "https://api.example.com/v1/ping".
//   - header (optional): Request headers map.
//     Supports magic variable "$TOKEN$" which is replaced using the selected credential:
//     1) metadata.access_token
//     2) attributes.api_key
//     3) metadata.token / metadata.id_token / metadata.cookie
//     Example: {"Authorization":"Bearer $TOKEN$"}.
//     Note: if you need to override the HTTP Host header, set header["Host"].
//   - data (optional): Raw request body as string (useful for POST/PUT/PATCH).
//
// Proxy selection (highest priority first):
//  1. Selected credential proxy_url
//  2. Global config proxy-url
//  3. Direct connect (environment proxies are not used)
//
// Response JSON (returned with HTTP 200 when the APICall itself succeeds):
//   - status_code: Upstream HTTP status code.
//   - header: Upstream response headers.
//   - body: Upstream response body as string.
//
// Example:
//
//	curl -sS -X POST "http://127.0.0.1:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"GET","url":"https://api.example.com/v1/ping","header":{"Authorization":"Bearer $TOKEN$"}}'
//
//	curl -sS -X POST "http://127.0.0.1:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer 831227" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"POST","url":"https://api.example.com/v1/fetchAvailableModels","header":{"Authorization":"Bearer $TOKEN$","Content-Type":"application/json","User-Agent":"cliproxyapi"},"data":"{}"}'
func (h *Handler) APICall(c *gin.Context) {
	var body apiCallRequest
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	method := strings.ToUpper(strings.TrimSpace(body.Method))
	if method == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing method"})
		return
	}

	urlStr := strings.TrimSpace(body.URL)
	if urlStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing url"})
		return
	}
	parsedURL, errParseURL := url.Parse(urlStr)
	if errParseURL != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}

	authIndex := firstNonEmptyString(body.AuthIndexSnake, body.AuthIndexCamel, body.AuthIndexPascal)
	auth := h.authByIndex(authIndex)

	reqHeaders := body.Header
	if reqHeaders == nil {
		reqHeaders = map[string]string{}
	}

	var hostOverride string
	var token string
	var tokenResolved bool
	var tokenErr error
	for key, value := range reqHeaders {
		if !strings.Contains(value, "$TOKEN$") {
			continue
		}
		if !tokenResolved {
			token, tokenErr = h.resolveTokenForAuth(c.Request.Context(), auth)
			tokenResolved = true
		}
		if auth != nil && token == "" {
			if tokenErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth token refresh failed"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth token not found"})
			return
		}
		if token == "" {
			continue
		}
		reqHeaders[key] = strings.ReplaceAll(value, "$TOKEN$", token)
	}

	var requestBody io.Reader
	if body.Data != "" {
		requestBody = strings.NewReader(body.Data)
	}

	req, errNewRequest := http.NewRequestWithContext(c.Request.Context(), method, urlStr, requestBody)
	if errNewRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to build request"})
		return
	}

	for key, value := range reqHeaders {
		if strings.EqualFold(key, "host") {
			hostOverride = strings.TrimSpace(value)
			continue
		}
		req.Header.Set(key, value)
	}
	if hostOverride != "" {
		req.Host = hostOverride
	}

	httpClient := &http.Client{
		Timeout: defaultAPICallTimeout,
	}
	httpClient.Transport = h.apiCallTransport(auth)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("management APICall request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	respBody, errReadAll := io.ReadAll(resp.Body)
	if errReadAll != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}

	h.markInvalidAuthFromAPICall(c.Request.Context(), auth, resp.StatusCode, respBody)
	h.recordOpenAIQuotaSampleFromAPICall(c.Request.Context(), auth, parsedURL, resp.StatusCode, respBody)

	c.JSON(http.StatusOK, apiCallResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       string(respBody),
	})
}

func (h *Handler) markInvalidAuthFromAPICall(ctx context.Context, auth *coreauth.Auth, statusCode int, respBody []byte) {
	if h == nil || h.authManager == nil || auth == nil {
		return
	}
	var status coreauth.Status
	statusMessage := ""
	lastError := &coreauth.Error{HTTPStatus: statusCode}
	switch {
	case matchesInvalidAuthResponse(statusCode, respBody):
		status = coreauth.Status(invalidAuthStatus)
		statusMessage = invalidAuthReason
		lastError.Code = invalidAuthErrorCode
		lastError.Message = gjson.GetBytes(respBody, "error.message").String()
	case matchesTokenInvalidAuthResponse(statusCode, respBody):
		status = coreauth.Status(tokenInvalidAuthStatus)
		statusMessage = tokenInvalidAuthReason
		lastError.Code = tokenInvalidAuthErrorCode
		lastError.Message = gjson.GetBytes(respBody, "error.message").String()
	default:
		return
	}

	updated := auth.Clone()
	updated.Status = status
	updated.StatusMessage = statusMessage
	updated.LastError = lastError
	updated.UpdatedAt = time.Now()
	if _, errUpdate := h.authManager.Update(ctx, updated); errUpdate != nil {
		log.WithError(errUpdate).Debug("failed to mark invalid auth from management APICall")
	}
}

func (h *Handler) recordOpenAIQuotaSampleFromAPICall(ctx context.Context, auth *coreauth.Auth, requestedURL *url.URL, statusCode int, respBody []byte) {
	if h == nil || auth == nil || requestedURL == nil || statusCode < 200 || statusCode >= 300 {
		return
	}
	if !strings.EqualFold(requestedURL.Hostname(), "chatgpt.com") || requestedURL.EscapedPath() != "/backend-api/wham/usage" {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider != "codex" && provider != "openai" {
		return
	}
	authType := auth.AuthKind()
	if authType != coreauth.AuthKindOAuth {
		return
	}
	authIndex := strings.TrimSpace(auth.EnsureIndex())
	if authIndex == "" {
		return
	}
	fileName := openAIUsageAuthFileName(auth)
	if fileName == "" {
		return
	}
	windowID, usedPercent, ok := quotaSampleWindowFromUsageResponse(respBody)
	if !ok {
		return
	}
	store := h.openAIUsageStoreSnapshot()
	recorder, ok := store.(openaiusage.QuotaSampleRecorder)
	if !ok || recorder == nil {
		return
	}
	if err := recorder.RecordQuotaSample(ctx, openaiusage.QuotaSample{
		AuthIndex:    authIndex,
		AuthFileName: fileName,
		DisplayName:  fileName,
		Provider:     provider,
		AuthType:     authType,
		AccountEmail: authEmail(auth),
		WindowID:     windowID,
		UsedPercent:  usedPercent,
		SampledAt:    time.Now().UTC(),
	}); err != nil {
		log.WithError(err).Warn("failed to record OpenAI quota sample from management APICall")
	}
}

func quotaSampleWindowFromUsageResponse(respBody []byte) (string, float64, bool) {
	rateLimit := gjson.GetBytes(respBody, "rate_limit")
	if !rateLimit.Exists() {
		rateLimit = gjson.GetBytes(respBody, "rateLimit")
	}
	if !rateLimit.Exists() {
		return "", 0, false
	}

	candidates := []quotaSampleWindowCandidate{
		{window: rateLimit.Get("primary_window")},
		{window: rateLimit.Get("primaryWindow")},
		{window: rateLimit.Get("secondary_window"), secondary: true},
		{window: rateLimit.Get("secondaryWindow"), secondary: true},
	}
	var weekly *quotaSampleWindowCandidate
	var monthly *quotaSampleWindowCandidate
	sawWindowSeconds := false
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.window.Exists() {
			continue
		}
		secondsValue := quotaSampleWindowSeconds(candidate.window)
		if !secondsValue.Exists() {
			continue
		}
		sawWindowSeconds = true
		seconds := secondsValue.Int()
		switch {
		case seconds == 604800:
			if weekly == nil {
				weekly = candidate
			}
		case seconds >= 28*24*60*60 && seconds <= 31*24*60*60:
			if monthly == nil {
				monthly = candidate
			}
		}
	}
	if weekly != nil {
		if usedPercent, ok := quotaSampleUsedPercent(weekly.window); ok {
			return "weekly", usedPercent, true
		}
		return "", 0, false
	}
	if monthly != nil {
		if usedPercent, ok := quotaSampleUsedPercent(monthly.window); ok {
			return "monthly", usedPercent, true
		}
		return "", 0, false
	}
	if sawWindowSeconds {
		return "", 0, false
	}
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.secondary || !candidate.window.Exists() {
			continue
		}
		if usedPercent, ok := quotaSampleUsedPercent(candidate.window); ok {
			return "weekly", usedPercent, true
		}
	}
	return "", 0, false
}

type quotaSampleWindowCandidate struct {
	window    gjson.Result
	secondary bool
}

func quotaSampleWindowSeconds(window gjson.Result) gjson.Result {
	seconds := window.Get("limit_window_seconds")
	if seconds.Exists() {
		return seconds
	}
	return window.Get("limitWindowSeconds")
}

func quotaSampleUsedPercent(window gjson.Result) (float64, bool) {
	usedPercent := window.Get("used_percent")
	if !usedPercent.Exists() {
		usedPercent = window.Get("usedPercent")
	}
	if !usedPercent.Exists() {
		return 0, false
	}
	var value float64
	switch usedPercent.Type {
	case gjson.Number:
		value = usedPercent.Float()
	case gjson.String:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(usedPercent.String()), 64)
		if err != nil {
			return 0, false
		}
		value = parsed
	default:
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func matchesInvalidAuthResponse(statusCode int, respBody []byte) bool {
	if statusCode != http.StatusForbidden {
		return false
	}
	code := strings.TrimSpace(gjson.GetBytes(respBody, "error.code").String())
	if code != invalidAuthErrorCode {
		return false
	}
	message := gjson.GetBytes(respBody, "error.message").String()
	return strings.Contains(message, invalidAuthMessageSubstring)
}

func matchesTokenInvalidAuthResponse(statusCode int, respBody []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	code := strings.TrimSpace(gjson.GetBytes(respBody, "error.code").String())
	if code != tokenInvalidAuthErrorCode {
		return false
	}
	errorType := strings.TrimSpace(gjson.GetBytes(respBody, "error.type").String())
	if errorType != tokenInvalidAuthErrorType {
		return false
	}
	message := strings.TrimSpace(gjson.GetBytes(respBody, "error.message").String())
	return message == tokenInvalidAuthMessage
}

func firstNonEmptyString(values ...*string) string {
	for _, v := range values {
		if v == nil {
			continue
		}
		if out := strings.TrimSpace(*v); out != "" {
			return out
		}
	}
	return ""
}

func tokenValueForAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if v := tokenValueFromMetadata(auth.Metadata); v != "" {
		return v
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	return ""
}

func (h *Handler) resolveTokenForAuth(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}

	if strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		token, errToken := h.refreshAntigravityOAuthAccessToken(ctx, auth)
		return token, errToken
	}

	return tokenValueForAuth(auth), nil
}

func (h *Handler) refreshAntigravityOAuthAccessToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return "", nil
	}

	metadata := auth.Metadata
	if len(metadata) == 0 {
		return "", fmt.Errorf("antigravity oauth metadata missing")
	}

	current := strings.TrimSpace(tokenValueFromMetadata(metadata))
	if current != "" && !antigravityTokenNeedsRefresh(metadata) {
		return current, nil
	}

	refreshToken := stringValue(metadata, "refresh_token")
	if refreshToken == "" {
		return "", fmt.Errorf("antigravity refresh token missing")
	}

	tokenURL := strings.TrimSpace(antigravityOAuthTokenURL)
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	clientID, clientSecret, errCredentials := antigravityOAuthClientCredentials(metadata)
	if errCredentials != nil {
		return "", errCredentials
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if errReq != nil {
		return "", errReq
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: h.apiCallTransport(auth),
	}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", errRead
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("antigravity oauth token refresh failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return "", errUnmarshal
	}

	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", fmt.Errorf("antigravity oauth token refresh returned empty access_token")
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	now := time.Now()
	auth.Metadata["access_token"] = strings.TrimSpace(tokenResp.AccessToken)
	if strings.TrimSpace(tokenResp.RefreshToken) != "" {
		auth.Metadata["refresh_token"] = strings.TrimSpace(tokenResp.RefreshToken)
	}
	if tokenResp.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = tokenResp.ExpiresIn
		auth.Metadata["timestamp"] = now.UnixMilli()
		auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	auth.Metadata["type"] = "antigravity"

	if h != nil && h.authManager != nil {
		auth.LastRefreshedAt = now
		auth.UpdatedAt = now
		_, _ = h.authManager.Update(ctx, auth)
	}

	return strings.TrimSpace(tokenResp.AccessToken), nil
}

func antigravityTokenNeedsRefresh(metadata map[string]any) bool {
	// Refresh a bit early to avoid requests racing token expiry.
	const skew = 30 * time.Second

	if metadata == nil {
		return true
	}
	if expStr, ok := metadata["expired"].(string); ok {
		if ts, errParse := time.Parse(time.RFC3339, strings.TrimSpace(expStr)); errParse == nil {
			return !ts.After(time.Now().Add(skew))
		}
	}
	expiresIn := int64Value(metadata["expires_in"])
	timestampMs := int64Value(metadata["timestamp"])
	if expiresIn > 0 && timestampMs > 0 {
		exp := time.UnixMilli(timestampMs).Add(time.Duration(expiresIn) * time.Second)
		return !exp.After(time.Now().Add(skew))
	}
	return true
}

func int64Value(raw any) int64 {
	switch typed := raw.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0
		}
		return int64(typed)
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		if i, errParse := typed.Int64(); errParse == nil {
			return i
		}
	case string:
		if s := strings.TrimSpace(typed); s != "" {
			if i, errParse := json.Number(s).Int64(); errParse == nil {
				return i
			}
		}
	}
	return 0
}

func stringValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 || key == "" {
		return ""
	}
	if v, ok := metadata[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func antigravityOAuthClientCredentials(metadata map[string]any) (string, string, error) {
	clientID := strings.TrimSpace(stringValue(metadata, "client_id"))
	if clientID == "" {
		clientID = strings.TrimSpace(stringValue(metadata, "clientId"))
	}
	if clientID == "" {
		clientID = strings.TrimSpace(stringValue(metadata, "oauth_client_id"))
	}
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv(antigravityOAuthClientIDEnv))
	}

	clientSecret := strings.TrimSpace(stringValue(metadata, "client_secret"))
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(stringValue(metadata, "clientSecret"))
	}
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(stringValue(metadata, "oauth_client_secret"))
	}
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(os.Getenv(antigravityOAuthClientSecretEnv))
	}

	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("antigravity oauth client credentials missing: set metadata client_id/client_secret or %s/%s", antigravityOAuthClientIDEnv, antigravityOAuthClientSecretEnv)
	}
	return clientID, clientSecret, nil
}

func tokenValueFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if v, ok := metadata["accessToken"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if tokenRaw, ok := metadata["token"]; ok && tokenRaw != nil {
		switch typed := tokenRaw.(type) {
		case string:
			if v := strings.TrimSpace(typed); v != "" {
				return v
			}
		case map[string]any:
			if v, ok := typed["access_token"].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
			if v, ok := typed["accessToken"].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case map[string]string:
			if v := strings.TrimSpace(typed["access_token"]); v != "" {
				return v
			}
			if v := strings.TrimSpace(typed["accessToken"]); v != "" {
				return v
			}
		}
	}
	if v, ok := metadata["token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["id_token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["cookie"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func (h *Handler) authByIndex(authIndex string) *coreauth.Auth {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || h == nil || h.authManager == nil {
		return nil
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		if auth.Index == authIndex {
			return auth
		}
	}
	return nil
}

func (h *Handler) apiCallTransport(auth *coreauth.Auth) http.RoundTripper {
	var proxyCandidates []string
	if auth != nil {
		if proxyStr := strings.TrimSpace(auth.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
		if h != nil && h.cfg != nil {
			if proxyStr := strings.TrimSpace(proxyURLFromAPIKeyConfig(h.cfg, auth)); proxyStr != "" {
				proxyCandidates = append(proxyCandidates, proxyStr)
			}
		}
	}
	if h != nil && h.cfg != nil {
		if proxyStr := strings.TrimSpace(h.cfg.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
	}

	for _, proxyStr := range proxyCandidates {
		if transport := buildProxyTransport(proxyStr); transport != nil {
			return transport
		}
	}

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		return &http.Transport{Proxy: nil}
	}
	clone := transport.Clone()
	clone.Proxy = nil
	return clone
}

type apiKeyConfigEntry interface {
	GetAPIKey() string
	GetBaseURL() string
}

func resolveAPIKeyConfig[T apiKeyConfigEntry](entries []T, auth *coreauth.Auth) *T {
	if auth == nil || len(entries) == 0 {
		return nil
	}
	attrKey, attrBase := "", ""
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range entries {
		entry := &entries[i]
		cfgKey := strings.TrimSpace((*entry).GetAPIKey())
		cfgBase := strings.TrimSpace((*entry).GetBaseURL())
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range entries {
			entry := &entries[i]
			if strings.EqualFold(strings.TrimSpace((*entry).GetAPIKey()), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func proxyURLFromAPIKeyConfig(cfg *config.Config, auth *coreauth.Auth) string {
	if cfg == nil || auth == nil {
		return ""
	}
	authKind, authAccount := auth.AccountInfo()
	if !strings.EqualFold(strings.TrimSpace(authKind), "api_key") {
		return ""
	}

	attrs := auth.Attributes
	compatName := ""
	providerKey := ""
	if len(attrs) > 0 {
		compatName = strings.TrimSpace(attrs["compat_name"])
		providerKey = strings.TrimSpace(attrs["provider_key"])
	}
	if compatName != "" || strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return resolveOpenAICompatAPIKeyProxyURL(cfg, auth, strings.TrimSpace(authAccount), providerKey, compatName)
	}

	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "gemini":
		if entry := resolveAPIKeyConfig(cfg.GeminiKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	case "gemini-interactions":
		if entry := resolveAPIKeyConfig(cfg.InteractionsKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	case "claude":
		if entry := resolveAPIKeyConfig(cfg.ClaudeKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	case "codex":
		if entry := resolveAPIKeyConfig(cfg.CodexKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	case "xai":
		if entry := resolveAPIKeyConfig(cfg.XAIKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	}
	return ""
}

func resolveOpenAICompatAPIKeyProxyURL(cfg *config.Config, auth *coreauth.Auth, apiKey, providerKey, compatName string) string {
	if cfg == nil || auth == nil {
		return ""
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	candidates := make([]string, 0, 3)
	if v := strings.TrimSpace(compatName); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(providerKey); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}

	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				for j := range compat.APIKeyEntries {
					entry := &compat.APIKeyEntries[j]
					if strings.EqualFold(strings.TrimSpace(entry.APIKey), apiKey) {
						return strings.TrimSpace(entry.ProxyURL)
					}
				}
				return ""
			}
		}
	}
	return ""
}

func buildProxyTransport(proxyStr string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyStr)
	if errBuild != nil {
		log.WithError(errBuild).Debug("build proxy transport failed")
		return nil
	}
	return transport
}
