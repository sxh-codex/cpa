package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/openaiusage"
)

func (h *Handler) GetOpenAIUsageStatus(c *gin.Context) {
	store := h.openAIUsageStoreSnapshot()
	if store == nil {
		c.JSON(http.StatusOK, openaiusage.StatusResponse{
			Enabled:        false,
			QueueLimit:     openaiusage.QueueLimit,
			BatchSize:      openaiusage.MaxBatchSize,
			PricingSource:  "OpenAI API pricing page https://developers.openai.com/api/docs/pricing checked 2026-07-14",
			PricingVersion: openaiusage.PricingVersion,
		})
		return
	}
	c.JSON(http.StatusOK, store.Status())
}

func (h *Handler) GetOpenAIUsageAccounts(c *gin.Context) {
	store := h.openAIUsageStoreSnapshot()
	if store == nil {
		c.JSON(http.StatusOK, openaiusage.AccountsResponse{Accounts: []openaiusage.AccountStats{}})
		return
	}
	accounts := store.Accounts()
	status := store.Status()
	c.JSON(http.StatusOK, openaiusage.AccountsResponse{
		Accounts:            accounts,
		Total:               len(accounts),
		DroppedEvents:       status.DroppedEvents,
		PricingMissingCount: status.PricingMissingCount,
	})
}

func (h *Handler) GetOpenAIUsageAccount(c *gin.Context) {
	store := h.openAIUsageStoreSnapshot()
	if store == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "openai usage not available"})
		return
	}
	authIndex := strings.TrimSpace(c.Param("auth_index"))
	account, ok := store.Account(authIndex)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "openai usage account not found"})
		return
	}
	c.JSON(http.StatusOK, account)
}

func (h *Handler) openAIUsageStoreSnapshot() openaiusage.Store {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.openAIUsageStore
}
