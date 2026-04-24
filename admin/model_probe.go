package admin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

type probeModelSupportRequest struct {
	Model       string  `json:"model"`
	AccountIDs  []int64 `json:"account_ids"`
	Concurrency int     `json:"concurrency"`
	Force       *bool   `json:"force"`
}

type probeModelSupportAccountResult struct {
	AccountID int64  `json:"account_id"`
	Email     string `json:"email,omitempty"`
	Supported bool   `json:"supported"`
	Outcome   string `json:"outcome"`
	CheckedAt string `json:"checked_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type probeModelSupportResponse struct {
	Model       string                           `json:"model"`
	Total       int                              `json:"total"`
	Supported   int                              `json:"supported"`
	Unsupported int                              `json:"unsupported"`
	Failed      int                              `json:"failed"`
	Results     []probeModelSupportAccountResult `json:"results,omitempty"`
}

func normalizeProbeModel(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))
	if model == "" {
		return "gpt-5.5"
	}
	return model
}

func isGPT55UnsupportedResponse(statusCode int, errCode, errMsg string) bool {
	if statusCode >= 500 {
		return false
	}
	combined := strings.ToLower(strings.TrimSpace(errCode + " " + errMsg))
	if combined == "" {
		return false
	}
	return strings.Contains(combined, "does not exist") ||
		strings.Contains(combined, "do not have access") ||
		strings.Contains(combined, "model_not_found") ||
		strings.Contains(combined, "unsupported_model") ||
		strings.Contains(combined, "unknown model")
}

// ProbeGPT55Capability 发送最小请求探测账号是否支持 gpt-5.5。
func (h *Handler) ProbeGPT55Capability(ctx context.Context, account *auth.Account) auth.GPT55ProbeResult {
	if h == nil || h.store == nil {
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: "handler/store 未初始化"}
	}
	if account == nil {
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: "账号不存在"}
	}

	payload := buildTestPayload("gpt-5.5")

	proxyURL := h.store.NextProxy()
	resp, err := proxy.ExecuteRequest(ctx, account, payload, "", proxyURL, "", nil, nil)
	if err != nil {
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: err.Error()}
	}
	defer resp.Body.Close()

	if usagePct, ok := proxy.ParseCodexUsageHeaders(resp, account); ok {
		h.store.PersistUsageSnapshot(account, usagePct)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		streamResult := parseSuccessfulGPT55ProbeResponse(resp)
		if streamResult.Outcome != auth.GPT55ProbeOutcomeSupported {
			return streamResult
		}
		account.ClearLastFailureDetail()
		if _, cooldownReason, active := account.GetCooldownSnapshot(); active && cooldownReason == "full_usage" {
			if !h.store.MarkFullUsageCooldownFromSnapshot(account) {
				h.store.ClearCooldown(account)
			}
		} else {
			h.store.ClearCooldown(account)
		}
		h.store.ReportRequestSuccess(account, 0)
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeSupported}
	}

	body, _ := io.ReadAll(resp.Body)
	displayStatus := proxy.NormalizeUpstreamStatusCode(resp.StatusCode, body)
	errCode, errMsg := proxy.ParseUpstreamErrorBrief(body)

	switch {
	case proxy.IsUnauthorizedLikeStatus(resp.StatusCode, body):
		if errCode == "" {
			errCode = "unauthorized"
		}
		if errMsg == "" {
			errMsg = "Unauthorized"
		}
		account.SetLastFailureDetail(displayStatus, errCode, errMsg)
		h.store.ReportRequestFailure(account, "unauthorized", 0)
		h.store.MarkCooldown(account, 24*time.Hour, "unauthorized")
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: errMsg}
	case resp.StatusCode == http.StatusTooManyRequests:
		if errCode == "" {
			errCode = "rate_limited"
		}
		if errMsg == "" {
			errMsg = "Rate limited"
		}
		account.SetLastFailureDetail(displayStatus, errCode, errMsg)
		if !h.store.MarkFullUsageCooldownFromSnapshot(account) {
			h.store.MarkCooldown(account, auth.RateLimitedProbeInterval, "rate_limited")
		}
		h.store.ReportRequestFailure(account, "rate_limited", 0)
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: errMsg}
	case isGPT55UnsupportedResponse(displayStatus, errCode, errMsg):
		if errMsg == "" {
			errMsg = http.StatusText(displayStatus)
		}
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeUnsupported, LastError: errMsg}
	default:
		if errMsg == "" {
			errMsg = http.StatusText(displayStatus)
		}
		if errMsg == "" && len(body) > 0 {
			errMsg = truncate(string(body), 300)
		}
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: errMsg}
	}
}

// ProbeModelSupport 手动批量探测账号模型能力（当前仅支持 gpt-5.5）。
func (h *Handler) ProbeModelSupport(c *gin.Context) {
	if h == nil || h.store == nil {
		writeError(c, http.StatusInternalServerError, "账号池未初始化")
		return
	}

	var req probeModelSupportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求体无效: "+err.Error())
		return
	}

	model := normalizeProbeModel(req.Model)
	if model != "gpt-5.5" {
		writeError(c, http.StatusBadRequest, "当前仅支持探测 gpt-5.5")
		return
	}

	probeFn := h.probeGPT55
	if probeFn == nil {
		writeError(c, http.StatusInternalServerError, "gpt-5.5 探测器未初始化")
		return
	}

	force := true
	if req.Force != nil {
		force = *req.Force
	}

	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = h.store.GetTestConcurrency()
	}
	if concurrency <= 0 {
		concurrency = 10
	}
	if concurrency > 20 {
		concurrency = 20
	}

	accounts := h.store.Accounts()
	if len(req.AccountIDs) > 0 {
		allowed := make(map[int64]struct{}, len(req.AccountIDs))
		for _, id := range req.AccountIDs {
			allowed[id] = struct{}{}
		}
		filtered := make([]*auth.Account, 0, len(accounts))
		for _, account := range accounts {
			if _, ok := allowed[account.ID()]; ok {
				filtered = append(filtered, account)
			}
		}
		accounts = filtered
	}

	if len(accounts) == 0 {
		c.JSON(http.StatusOK, probeModelSupportResponse{Model: model})
		return
	}

	results := make([]probeModelSupportAccountResult, len(accounts))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	resp := probeModelSupportResponse{
		Model: model,
		Total: len(accounts),
	}

	for idx, account := range accounts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, acc *auth.Account) {
			defer wg.Done()
			defer func() { <-sem }()

			probeCtx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
			defer cancel()

			result := probeFn(probeCtx, acc, force)
			supported, checkedAt, lastError := acc.GPT55CapabilitySnapshot()
			acc.Mu().RLock()
			email := acc.Email
			acc.Mu().RUnlock()
			item := probeModelSupportAccountResult{
				AccountID: acc.ID(),
				Email:     email,
				Supported: supported,
				Outcome:   string(result.Outcome),
				LastError: strings.TrimSpace(lastError),
			}
			if checkedAt.IsZero() {
				item.LastError = strings.TrimSpace(result.LastError)
			} else {
				item.CheckedAt = checkedAt.UTC().Format(time.RFC3339)
			}

			mu.Lock()
			results[i] = item
			switch result.Outcome {
			case auth.GPT55ProbeOutcomeSupported:
				resp.Supported++
			case auth.GPT55ProbeOutcomeUnsupported:
				resp.Unsupported++
			default:
				resp.Failed++
			}
			mu.Unlock()
		}(idx, account)
	}

	wg.Wait()
	resp.Results = results
	c.JSON(http.StatusOK, resp)
}
