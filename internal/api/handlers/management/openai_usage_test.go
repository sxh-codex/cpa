package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/openaiusage"
)

type fakeOpenAIUsageStore struct {
	status   openaiusage.StatusResponse
	accounts []openaiusage.AccountStats
}

func (s fakeOpenAIUsageStore) Status() openaiusage.StatusResponse { return s.status }

func (s fakeOpenAIUsageStore) Accounts() []openaiusage.AccountStats {
	return append([]openaiusage.AccountStats(nil), s.accounts...)
}

func (s fakeOpenAIUsageStore) Account(authIndex string) (openaiusage.AccountStats, bool) {
	for _, account := range s.accounts {
		if account.AuthIndex == authIndex {
			return account, true
		}
	}
	return openaiusage.AccountStats{}, false
}

func TestGetOpenAIUsageStatus(t *testing.T) {
	h := &Handler{openAIUsageStore: fakeOpenAIUsageStore{
		status: openaiusage.StatusResponse{Enabled: true, Path: `E:\CLIProxyAPI\data\openai-usage.json`, QueueLimit: openaiusage.QueueLimit, PricingMissingCount: 4},
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-usage/status", nil)
	h.GetOpenAIUsageStatus(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload openaiusage.StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !payload.Enabled || payload.QueueLimit != openaiusage.QueueLimit || payload.PricingMissingCount != 4 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestGetOpenAIUsageAccounts(t *testing.T) {
	h := &Handler{openAIUsageStore: fakeOpenAIUsageStore{
		status: openaiusage.StatusResponse{DroppedEvents: 2, PricingMissingCount: 5},
		accounts: []openaiusage.AccountStats{{
			AuthIndex:           "idx",
			AuthFileName:        "idx.json",
			RequestCount:        3,
			PricingMissingCount: 5,
		}},
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-usage/accounts", nil)
	h.GetOpenAIUsageAccounts(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload openaiusage.AccountsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Total != 1 || payload.DroppedEvents != 2 || payload.PricingMissingCount != 5 || payload.Accounts[0].AuthIndex != "idx" || payload.Accounts[0].PricingMissingCount != 5 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestGetOpenAIUsageAccount(t *testing.T) {
	h := &Handler{openAIUsageStore: fakeOpenAIUsageStore{
		accounts: []openaiusage.AccountStats{{AuthIndex: "idx", AuthFileName: "idx.json"}},
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "auth_index", Value: "idx"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-usage/accounts/idx", nil)
	h.GetOpenAIUsageAccount(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetOpenAIUsageAccountNotFound(t *testing.T) {
	h := &Handler{openAIUsageStore: fakeOpenAIUsageStore{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "auth_index", Value: "missing"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-usage/accounts/missing", nil)
	h.GetOpenAIUsageAccount(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}
