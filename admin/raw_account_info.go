package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const whamAccountCheckURL = "https://chatgpt.com/backend-api/wham/accounts/check"

type cliproxyProfile struct {
	Email      string
	AccountID  string
	PlanType   string
	PlanSource string
}

// GetAccountRawInfo 获取账号一手信息：以 CPA/CLIProxyAPI 口径为主，附带上游 wham 原始响应。
// GET /api/admin/accounts/:id/raw-info
func (h *Handler) GetAccountRawInfo(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "无效的账号 ID")
		return
	}

	account := h.store.FindByID(id)
	if account == nil {
		writeError(c, http.StatusNotFound, "账号不在运行时池中")
		return
	}

	refreshFn := h.refreshAccount
	if refreshFn == nil {
		refreshFn = h.refreshSingleAccount
	}

	account.Mu().RLock()
	hasAccessToken := strings.TrimSpace(account.AccessToken) != ""
	hasRefreshToken := strings.TrimSpace(account.RefreshToken) != ""
	currentPlan := strings.TrimSpace(account.PlanType)
	account.Mu().RUnlock()
	needsRefresh := account.NeedsRefresh()

	// 对齐 CPA：优先保证 token 为最新状态
	if (!hasAccessToken || needsRefresh) && hasRefreshToken {
		refreshCtx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
		defer cancel()
		if err := refreshFn(refreshCtx, id); err != nil {
			writeError(c, http.StatusInternalServerError, "刷新 Access Token 失败: "+err.Error())
			return
		}
	} else if !hasAccessToken {
		writeError(c, http.StatusBadRequest, "账号没有可用的 Access Token，且缺少 Refresh Token")
		return
	}

	account.Mu().RLock()
	accessToken := strings.TrimSpace(account.AccessToken)
	accountID := strings.TrimSpace(account.AccountID)
	accountProxy := strings.TrimSpace(account.ProxyURL)
	account.Mu().RUnlock()

	if accessToken == "" {
		writeError(c, http.StatusBadRequest, "账号没有可用的 Access Token，请先刷新")
		return
	}

	proxyURL := strings.TrimSpace(h.store.NextProxy())
	if proxyURL == "" {
		proxyURL = accountProxy
	}

	rowCtx, rowCancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer rowCancel()
	row, err := h.db.GetAccountByID(rowCtx, id)
	if err != nil {
		writeInternalError(c, fmt.Errorf("读取账号凭据失败: %w", err))
		return
	}

	profile := resolveCliproxyProfile(row)
	if !hasCliproxyProfile(profile) {
		profile = resolveRuntimeProfile(account)
	}

	reqCtx, reqCancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer reqCancel()

	var (
		upstreamRawBody    []byte
		upstreamParsedBody any
		upstreamErr        error
	)

	upstreamRawBody, statusCode, reqErr := requestWhamAccountCheck(reqCtx, accessToken, accountID, proxyURL)
	if reqErr != nil {
		upstreamErr = reqErr
		if len(upstreamRawBody) > 0 {
			errCode := strings.TrimSpace(gjson.GetBytes(upstreamRawBody, "error.code").String())
			errMsg := strings.TrimSpace(gjson.GetBytes(upstreamRawBody, "error.message").String())
			if errMsg == "" {
				errMsg = strings.TrimSpace(gjson.GetBytes(upstreamRawBody, "message").String())
			}
			account.SetLastFailureDetail(statusCode, errCode, errMsg)
			switch statusCode {
			case http.StatusUnauthorized:
				h.store.MarkCooldown(account, 24*time.Hour, "unauthorized")
			case http.StatusTooManyRequests:
				if !h.store.MarkFullUsageCooldownFromSnapshot(account) {
					h.store.MarkCooldown(account, auth.RateLimitedProbeInterval, "rate_limited")
				}
			}
		}
	} else {
		if err := json.Unmarshal(upstreamRawBody, &upstreamParsedBody); err != nil {
			upstreamErr = fmt.Errorf("上游返回了非 JSON 数据")
		} else {
			account.ClearLastFailureDetail()
		}
	}

	upstreamFields, _ := extractCredentialUpdatesFromRawInfo(upstreamRawBody)
	refreshedFields, credentialUpdates := mergeCredentialRefresh(profile, upstreamFields, currentPlan)
	credentialUpdates["raw_info_refreshed_at"] = time.Now().UTC().Format(time.RFC3339)

	dbCtx, dbCancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer dbCancel()
	if err := h.db.UpdateCredentials(dbCtx, id, credentialUpdates); err != nil {
		writeInternalError(c, fmt.Errorf("写入账号原始信息失败: %w", err))
		return
	}

	applyRawInfoToRuntimeAccount(account, refreshedFields)
	h.db.InsertAccountEventAsync(id, "raw_info_refreshed", "manual")

	if upstreamErr != nil && !hasCliproxyProfile(profile) {
		writeError(c, http.StatusBadGateway, upstreamErr.Error())
		return
	}

	rawPayload := gin.H{
		"cliproxyapi_profile": buildCliproxyProfilePayload(profile),
	}
	if upstreamParsedBody != nil {
		rawPayload["upstream_wham_accounts_check"] = upstreamParsedBody
	}
	if upstreamErr != nil {
		rawPayload["upstream_wham_accounts_check_error"] = upstreamErr.Error()
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          "账号原始信息获取成功",
		"source":           "cliproxyapi",
		"fetched_at":       time.Now().UTC().Format(time.RFC3339),
		"refreshed_fields": refreshedFields,
		"raw":              rawPayload,
	})
}

func requestWhamAccountCheck(ctx context.Context, accessToken, accountID, proxyURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, whamAccountCheckURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("创建上游请求失败: %w", err)
	}

	profile := proxy.StableCodexClientProfile()
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Originator", proxy.Originator)
	req.Header.Set("X-Client-Request-Id", uuid.NewString())
	if strings.TrimSpace(profile.UserAgent) != "" {
		req.Header.Set("User-Agent", profile.UserAgent)
	}
	if strings.TrimSpace(profile.Version) != "" {
		req.Header.Set("Version", profile.Version)
	}
	if strings.TrimSpace(accountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", strings.TrimSpace(accountID))
	}

	client := newWhamClient(proxyURL)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求上游失败: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取上游响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return rawBody, resp.StatusCode, fmt.Errorf("上游返回 %d: %s", resp.StatusCode, truncate(string(rawBody), 500))
	}
	return rawBody, resp.StatusCode, nil
}

func newWhamClient(proxyURL string) *http.Client {
	transport := cloneHTTPTransport()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = true

	baseDialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport.DialContext = baseDialer.DialContext

	if strings.TrimSpace(proxyURL) != "" {
		if err := auth.ConfigureTransportProxy(transport, proxyURL, baseDialer); err != nil {
			log.Printf("配置账号原始信息请求代理失败，回退直连: %v", err)
			transport.Proxy = nil
			transport.DialContext = baseDialer.DialContext
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}

func cloneHTTPTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok && base != nil {
		return base.Clone()
	}
	return &http.Transport{}
}

func resolveCliproxyProfile(row *database.AccountRow) cliproxyProfile {
	if row == nil {
		return cliproxyProfile{}
	}

	result := cliproxyProfile{}
	credEmail := strings.TrimSpace(row.GetCredential("email"))
	credAccountID := strings.TrimSpace(row.GetCredential("account_id"))
	credPlan := auth.NormalizePlanType(strings.TrimSpace(row.GetCredential("plan_type")))

	if credEmail != "" {
		result.Email = credEmail
	}
	if credAccountID != "" {
		result.AccountID = credAccountID
	}

	plan := ""
	planSource := ""
	applyPlan := func(candidate string, source string) {
		candidate = auth.NormalizePlanType(candidate)
		if candidate == "" {
			return
		}
		next := auth.PreferPlanType(plan, candidate)
		if next != plan {
			plan = next
			planSource = source
		}
	}

	if credPlan != "" {
		applyPlan(credPlan, "credentials.plan_type")
	}

	if accessToken := strings.TrimSpace(row.GetCredential("access_token")); accessToken != "" {
		if info := auth.ParseAccessToken(accessToken); info != nil {
			if result.Email == "" && strings.TrimSpace(info.Email) != "" {
				result.Email = strings.TrimSpace(info.Email)
			}
			if result.AccountID == "" && strings.TrimSpace(info.ChatGPTAccountID) != "" {
				result.AccountID = strings.TrimSpace(info.ChatGPTAccountID)
			}
			applyPlan(info.PlanType, "access_token.chatgpt_plan_type")
		}
	}

	if idToken := strings.TrimSpace(row.GetCredential("id_token")); idToken != "" {
		if info := auth.ParseIDToken(idToken); info != nil {
			if strings.TrimSpace(info.Email) != "" {
				result.Email = strings.TrimSpace(info.Email)
			}
			if strings.TrimSpace(info.ChatGPTAccountID) != "" {
				result.AccountID = strings.TrimSpace(info.ChatGPTAccountID)
			}
			applyPlan(info.PlanType, "id_token.chatgpt_plan_type")
		}
	}

	result.PlanType = plan
	result.PlanSource = planSource
	return result
}

func resolveRuntimeProfile(account *auth.Account) cliproxyProfile {
	if account == nil {
		return cliproxyProfile{}
	}
	account.Mu().RLock()
	defer account.Mu().RUnlock()
	return cliproxyProfile{
		Email:      strings.TrimSpace(account.Email),
		AccountID:  strings.TrimSpace(account.AccountID),
		PlanType:   auth.NormalizePlanType(account.PlanType),
		PlanSource: "runtime.account",
	}
}

func hasCliproxyProfile(profile cliproxyProfile) bool {
	return strings.TrimSpace(profile.Email) != "" ||
		strings.TrimSpace(profile.AccountID) != "" ||
		strings.TrimSpace(profile.PlanType) != ""
}

func buildCliproxyProfilePayload(profile cliproxyProfile) gin.H {
	return gin.H{
		"email":       profile.Email,
		"account_id":  profile.AccountID,
		"plan_type":   profile.PlanType,
		"plan_source": profile.PlanSource,
	}
}

func mergeCredentialRefresh(profile cliproxyProfile, upstream map[string]string, currentPlan string) (map[string]string, map[string]interface{}) {
	refreshed := make(map[string]string, 3)
	updates := make(map[string]interface{}, 3)

	email := strings.TrimSpace(profile.Email)
	if email == "" {
		email = strings.TrimSpace(upstream["email"])
	}
	if email != "" {
		refreshed["email"] = email
		updates["email"] = email
	}

	accountID := strings.TrimSpace(profile.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(upstream["account_id"])
	}
	if accountID != "" {
		refreshed["account_id"] = accountID
		updates["account_id"] = accountID
	}

	selectedPlan := auth.NormalizePlanType(strings.TrimSpace(currentPlan))
	if candidate := strings.TrimSpace(profile.PlanType); candidate != "" {
		selectedPlan = auth.PreferPlanType(selectedPlan, candidate)
	}
	if candidate := strings.TrimSpace(upstream["plan_type"]); candidate != "" {
		selectedPlan = auth.PreferPlanType(selectedPlan, candidate)
	}
	if selectedPlan != "" {
		refreshed["plan_type"] = selectedPlan
		updates["plan_type"] = selectedPlan
	}

	return refreshed, updates
}

func extractCredentialUpdatesFromRawInfo(rawBody []byte) (map[string]string, map[string]interface{}) {
	if len(rawBody) == 0 {
		return map[string]string{}, map[string]interface{}{}
	}

	email := firstNonEmptyJSONValue(rawBody,
		"email",
		"user.email",
		"profile.email",
		"account.email",
		"data.email",
	)
	defaultAccountID := firstNonEmptyJSONValue(rawBody, "default_account_id")
	accountID := firstNonEmptyJSONValue(rawBody,
		"chatgpt_account_id",
		"account_id",
		"account.account_id",
		"account.chatgpt_account_id",
		"data.account_id",
	)
	if accountID == "" {
		accountID = defaultAccountID
	}
	if accountID == "" {
		accountID = firstNonEmptyJSONValue(rawBody, "accounts.0.id")
	}

	planTypeRaw := firstNonEmptyJSONValue(rawBody,
		"plan_type",
		"chatgpt_plan_type",
		"planType",
		"account.plan_type",
		"account.chatgpt_plan_type",
		"account.planType",
		"subscription.plan_type",
		"subscription.chatgpt_plan_type",
		"data.plan_type",
	)

	if planTypeRaw == "" && defaultAccountID != "" {
		escaped := escapeForGJSONLiteral(defaultAccountID)
		path := fmt.Sprintf(`accounts.#(id=="%s").plan_type`, escaped)
		planTypeRaw = strings.TrimSpace(gjson.GetBytes(rawBody, path).String())
	}
	if planTypeRaw == "" {
		planTypeRaw = firstNonEmptyJSONValue(rawBody, "accounts.0.plan_type")
	}

	refreshed := make(map[string]string, 3)
	updates := make(map[string]interface{}, 3)

	if strings.TrimSpace(email) != "" {
		refreshed["email"] = strings.TrimSpace(email)
		updates["email"] = strings.TrimSpace(email)
	}
	if strings.TrimSpace(accountID) != "" {
		refreshed["account_id"] = strings.TrimSpace(accountID)
		updates["account_id"] = strings.TrimSpace(accountID)
	}
	if strings.TrimSpace(planTypeRaw) != "" {
		normalizedPlan := auth.NormalizePlanType(planTypeRaw)
		refreshed["plan_type"] = normalizedPlan
		updates["plan_type"] = normalizedPlan
	}

	return refreshed, updates
}

func firstNonEmptyJSONValue(rawBody []byte, paths ...string) string {
	for _, path := range paths {
		value := strings.TrimSpace(gjson.GetBytes(rawBody, path).String())
		if value != "" {
			return value
		}
	}
	return ""
}

func escapeForGJSONLiteral(value string) string {
	replacer := strings.NewReplacer(`\\`, `\\\\`, `"`, `\\"`)
	return replacer.Replace(value)
}

func applyRawInfoToRuntimeAccount(account *auth.Account, refreshed map[string]string) {
	account.Mu().Lock()
	defer account.Mu().Unlock()

	if email := strings.TrimSpace(refreshed["email"]); email != "" {
		account.Email = email
	}
	if accountID := strings.TrimSpace(refreshed["account_id"]); accountID != "" {
		account.AccountID = accountID
	}
	if planType := strings.TrimSpace(refreshed["plan_type"]); planType != "" {
		account.PlanType = auth.NormalizePlanType(planType)
	}
}
