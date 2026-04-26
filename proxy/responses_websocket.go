package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	responsesWebsocketRequestCreate  = "response.create"
	responsesWebsocketRequestAppend  = "response.append"
	responsesWebsocketEventError     = "error"
	responsesWebsocketEventCompleted = "response.completed"
	responsesWebsocketEventFailed    = "response.failed"
)

var responsesWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// ResponsesWebsocket handles downstream websocket clients for /v1/responses and
// /backend-api/codex/responses. It forwards upstream SSE data payloads as
// websocket text messages.
func (h *Handler) ResponsesWebsocket(c *gin.Context) {
	conn, err := responsesWebsocketUpgrader.Upgrade(c.Writer, c.Request, responsesWebsocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	defer conn.Close()

	var lastRequest []byte
	lastResponseOutput := []byte("[]")

	for {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				return
			}
			log.Printf("responses websocket read failed: %v", err)
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}

		normalized, nextRequest, err := normalizeResponsesWebsocketRequestMessage(payload, lastRequest, lastResponseOutput)
		if err != nil {
			if writeErr := writeResponsesWebsocketError(conn, http.StatusBadRequest, err.Error(), "invalid_request_error"); writeErr != nil {
				return
			}
			continue
		}

		output, err := h.forwardResponsesWebsocketRequest(c, conn, normalized)
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				return
			}
			log.Printf("responses websocket forward failed: %v", err)
			return
		}

		lastRequest = nextRequest
		lastResponseOutput = output
	}
}

func responsesWebsocketUpgradeHeaders(req *http.Request) http.Header {
	headers := http.Header{}
	if req == nil {
		return headers
	}
	if turnState := strings.TrimSpace(req.Header.Get("X-Codex-Turn-State")); turnState != "" {
		headers.Set("X-Codex-Turn-State", turnState)
	}
	return headers
}

func normalizeResponsesWebsocketRequestMessage(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte) ([]byte, []byte, error) {
	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 {
		return nil, lastRequest, fmt.Errorf("empty websocket request body")
	}
	if !json.Valid(rawJSON) {
		return nil, lastRequest, fmt.Errorf("invalid JSON websocket request body")
	}

	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	switch requestType {
	case "":
		return normalizeResponsesWebsocketInitialRequest(rawJSON)
	case responsesWebsocketRequestCreate:
		if len(lastRequest) == 0 {
			return normalizeResponsesWebsocketInitialRequest(rawJSON)
		}
		return normalizeResponsesWebsocketSubsequentRequest(rawJSON, lastRequest, lastResponseOutput)
	case responsesWebsocketRequestAppend:
		return normalizeResponsesWebsocketSubsequentRequest(rawJSON, lastRequest, lastResponseOutput)
	default:
		return nil, lastRequest, fmt.Errorf("unsupported websocket request type: %s", requestType)
	}
}

func normalizeResponsesWebsocketInitialRequest(rawJSON []byte) ([]byte, []byte, error) {
	normalized, err := sjson.DeleteBytes(rawJSON, "type")
	if err != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	if !gjson.GetBytes(normalized, "input").Exists() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte("[]"))
	}

	if strings.TrimSpace(gjson.GetBytes(normalized, "model").String()) == "" {
		return nil, nil, fmt.Errorf("missing model in websocket request")
	}
	return normalized, bytes.Clone(normalized), nil
}

func normalizeResponsesWebsocketSubsequentRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte) ([]byte, []byte, error) {
	if len(lastRequest) == 0 {
		return nil, lastRequest, fmt.Errorf("websocket request received before response.create")
	}

	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() || !nextInput.IsArray() {
		return nil, lastRequest, fmt.Errorf("websocket request requires array field: input")
	}

	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) != "" {
		normalized, err := sjson.DeleteBytes(rawJSON, "type")
		if err != nil {
			normalized = bytes.Clone(rawJSON)
		}
		if !gjson.GetBytes(normalized, "model").Exists() {
			if model := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String()); model != "" {
				normalized, _ = sjson.SetBytes(normalized, "model", model)
			}
		}
		if !gjson.GetBytes(normalized, "instructions").Exists() {
			if instructions := gjson.GetBytes(lastRequest, "instructions"); instructions.Exists() {
				normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
			}
		}
		normalized, _ = sjson.SetBytes(normalized, "stream", true)
		if strings.TrimSpace(gjson.GetBytes(normalized, "model").String()) == "" {
			return nil, lastRequest, fmt.Errorf("missing model in websocket request")
		}
		return normalized, bytes.Clone(normalized), nil
	}

	mergedInput, err := mergeResponsesWebsocketJSONArrayRaw(gjson.GetBytes(lastRequest, "input").Raw, normalizeResponsesWebsocketJSONArrayRaw(lastResponseOutput))
	if err != nil {
		return nil, lastRequest, fmt.Errorf("invalid previous response output: %w", err)
	}
	mergedInput, err = mergeResponsesWebsocketJSONArrayRaw(mergedInput, nextInput.Raw)
	if err != nil {
		return nil, lastRequest, fmt.Errorf("invalid request input: %w", err)
	}

	normalized, err := sjson.DeleteBytes(rawJSON, "type")
	if err != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	normalized, err = sjson.SetRawBytes(normalized, "input", []byte(mergedInput))
	if err != nil {
		return nil, lastRequest, fmt.Errorf("failed to merge websocket input: %w", err)
	}
	if !gjson.GetBytes(normalized, "model").Exists() {
		if model := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String()); model != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", model)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		if instructions := gjson.GetBytes(lastRequest, "instructions"); instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	if strings.TrimSpace(gjson.GetBytes(normalized, "model").String()) == "" {
		return nil, lastRequest, fmt.Errorf("missing model in websocket request")
	}
	return normalized, bytes.Clone(normalized), nil
}

func mergeResponsesWebsocketJSONArrayRaw(existingRaw string, appendRaw string) (string, error) {
	existingRaw = strings.TrimSpace(existingRaw)
	appendRaw = strings.TrimSpace(appendRaw)
	if existingRaw == "" {
		existingRaw = "[]"
	}
	if appendRaw == "" {
		appendRaw = "[]"
	}

	var existing []json.RawMessage
	if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
		return "", err
	}
	var appendItems []json.RawMessage
	if err := json.Unmarshal([]byte(appendRaw), &appendItems); err != nil {
		return "", err
	}

	merged := append(existing, appendItems...)
	out, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func normalizeResponsesWebsocketJSONArrayRaw(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "[]"
	}
	result := gjson.Parse(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return trimmed
	}
	return "[]"
}

func prepareResponsesWebsocketCodexBody(rawBody []byte, model string) (body []byte, expandedInputRaw string, reasoningEffort string, serviceTier string, err error) {
	body = normalizeServiceTierField(bytes.Clone(rawBody))
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.SetBytes(body, "store", false)
	if !gjson.GetBytes(body, "include").Exists() {
		body, _ = sjson.SetBytes(body, "include", []string{"reasoning.encrypted_content"})
	}

	inputResult := gjson.GetBytes(body, "input")
	if inputResult.Exists() && inputResult.Type == gjson.String {
		body, _ = sjson.SetBytes(body, "input", []map[string]string{
			{"role": "user", "content": inputResult.String()},
		})
	}
	body = normalizeCodexResponsesBody(body)

	if re := gjson.GetBytes(body, "reasoning_effort"); re.Exists() && !gjson.GetBytes(body, "reasoning.effort").Exists() {
		body, _ = sjson.SetBytes(body, "reasoning.effort", re.String())
	}
	body = clampReasoningEffort(body, model)
	reasoningEffort = gjson.GetBytes(body, "reasoning.effort").String()
	serviceTier = extractServiceTier(body)
	body = sanitizeServiceTierForUpstream(body)
	body = ensureToolDescriptions(body)
	body = sanitizeToolSchemas(body)
	body = normalizeCodexResponsesBody(body)

	body, expandedInputRaw = expandPreviousResponse(body)

	unsupportedFields := []string{
		"max_output_tokens", "max_tokens", "max_completion_tokens",
		"temperature", "top_p", "frequency_penalty", "presence_penalty",
		"logprobs", "top_logprobs", "n", "seed", "stop", "user",
		"logit_bias", "response_format", "serviceTier",
		"stream_options", "reasoning_effort", "truncation", "context_management",
		"disable_response_storage", "verbosity",
	}
	for _, field := range unsupportedFields {
		body, _ = sjson.DeleteBytes(body, field)
	}
	return body, expandedInputRaw, reasoningEffort, serviceTier, nil
}

func (h *Handler) forwardResponsesWebsocketRequest(c *gin.Context, conn *websocket.Conn, rawBody []byte) ([]byte, error) {
	if len(rawBody) > security.MaxRequestBodySize {
		return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusRequestEntityTooLarge, "请求体过大", "invalid_request_error")
	}
	if h == nil || h.store == nil {
		return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusServiceUnavailable, "无可用账号，请稍后重试", "server_error")
	}

	model := strings.TrimSpace(gjson.GetBytes(rawBody, "model").String())
	if model == "" {
		return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadRequest, "missing required field: model", "invalid_request_error")
	}
	if err := security.ValidateModelName(model); err != nil {
		return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadRequest, "model 参数无效", "invalid_request_error")
	}
	if IsImageOnlyModel(model) {
		return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadRequest, fmt.Sprintf("model '%s' is only supported on /v1/images/generations and /v1/images/edits", model), "invalid_request_error")
	}
	c.Set("x-model", model)

	codexBody, expandedInputRaw, reasoningEffort, serviceTier, err := prepareResponsesWebsocketCodexBody(rawBody, model)
	if err != nil {
		return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadRequest, err.Error(), "invalid_request_error")
	}
	if serviceTier != "" {
		c.Set("x-service-tier", serviceTier)
	}

	requestStartedAt := requestStartTime(c)
	logRequestLifecycleStart(c, "/v1/responses", model, true, reasoningEffort)
	sessionID := ResolveSessionID(c.GetHeader("Authorization"), rawBody)
	maxRetries := h.getMaxRetries()
	excludeAccounts := make(map[int64]bool)
	var lastErr error
	var lastStatusCode int
	var lastBody []byte

	for attempt := 0; attempt <= maxRetries; attempt++ {
		acquireStartedAt := time.Now()
		account := h.acquireAccountForRequest(c, excludeAccounts)
		attemptAcquireMs := int(time.Since(acquireStartedAt).Milliseconds())
		if account == nil {
			if lastStatusCode != 0 && len(lastBody) > 0 {
				status, message, errType := responsesWebsocketUpstreamError(lastStatusCode, lastBody)
				return []byte("[]"), writeResponsesWebsocketError(conn, status, message, errType)
			}
			if lastErr != nil {
				return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadGateway, "上游请求失败: "+lastErr.Error(), "upstream_error")
			}
			return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusServiceUnavailable, "无可用账号，请稍后重试", "server_error")
		}

		start := time.Now()
		proxyURL := h.store.NextProxy()
		logRequestDispatch(c, "/v1/responses", attempt+1, account, proxyURL, model, reasoningEffort, attemptAcquireMs)

		apiKey := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		deviceCfg := h.deviceCfg
		if deviceCfg == nil {
			deviceCfg = DefaultDeviceProfileConfig()
		}
		downstreamHeaders := c.Request.Header.Clone()
		attemptBody := ensureImageGenerationTool(codexBody, model, account.GetPlanType())

		resp, attemptTrace, reqErr := ExecuteRequestTraced(c.Request.Context(), account, attemptBody, sessionID, proxyURL, apiKey, deviceCfg, downstreamHeaders, true)
		durationMs := int(time.Since(start).Milliseconds())
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		if attemptTrace != nil && !attemptTrace.HeaderAt.IsZero() {
			logUpstreamAttemptHeaders(c, "/v1/responses", attempt+1, account, statusCode, attemptTrace, requestStartedAt)
		}

		if reqErr != nil {
			if kind := classifyTransportFailure(reqErr); kind != "" {
				h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
			}
			logUpstreamAttemptResult(c, "/v1/responses", attempt+1, account, proxyURL, 0, durationMs, requestStartedAt, reqErr.Error())
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastErr = reqErr
			if !IsRetryableError(reqErr) && classifyTransportFailure(reqErr) == "" {
				return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadGateway, reqErr.Error(), "upstream_error")
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			h.persistUsageAndSettleFromResponse(account, resp)
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if kind := classifyHTTPFailure(resp.StatusCode, errBody); kind != "" {
				h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
			}
			logUpstreamAttemptResult(c, "/v1/responses", attempt+1, account, proxyURL, resp.StatusCode, durationMs, requestStartedAt, fmt.Sprintf("http_%d", resp.StatusCode))
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastStatusCode = resp.StatusCode
			lastBody = errBody
			if resp.StatusCode == http.StatusUnauthorized && isPermanentUnauthorizedError(errBody) {
				h.forceDeleteAccount(account.ID(), "auto_clean_no_organization")
			}
			if isRetryableStatus(resp.StatusCode, errBody) && attempt < maxRetries {
				continue
			}
			status, message, errType := responsesWebsocketUpstreamError(resp.StatusCode, errBody)
			return []byte("[]"), writeResponsesWebsocketError(conn, status, message, errType)
		}

		output, err := h.forwardResponsesWebsocketStream(c, conn, account, resp, expandedInputRaw, model, reasoningEffort, serviceTier, requestStartedAt, start, attempt+1)
		h.store.Release(account)
		return output, err
	}

	if lastErr != nil {
		return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadGateway, "上游请求失败: "+lastErr.Error(), "upstream_error")
	}
	if lastStatusCode != 0 {
		status, message, errType := responsesWebsocketUpstreamError(lastStatusCode, lastBody)
		return []byte("[]"), writeResponsesWebsocketError(conn, status, message, errType)
	}
	return []byte("[]"), writeResponsesWebsocketError(conn, http.StatusBadGateway, "上游请求失败", "upstream_error")
}

func (h *Handler) forwardResponsesWebsocketStream(
	c *gin.Context,
	conn *websocket.Conn,
	account *auth.Account,
	resp *http.Response,
	expandedInputRaw string,
	model string,
	reasoningEffort string,
	serviceTier string,
	requestStartedAt time.Time,
	start time.Time,
	attempt int,
) ([]byte, error) {
	defer resp.Body.Close()
	defer h.persistUsageAndSettleFromResponse(account, resp)

	var usage *UsageInfo
	completedOutput := []byte("[]")
	gotTerminal := false
	var writeErr error
	firstFrameRecorded := false
	firstTokenMs := 0
	attemptFirstTokenMs := 0
	actualServiceTier := ""

	readErr := ReadSSEStream(resp.Body, func(data []byte) bool {
		eventType := gjson.GetBytes(data, "type").String()
		if !firstFrameRecorded {
			firstFrameRecorded = true
			logUpstreamFirstFrame(c, "/v1/responses", attempt, eventType, requestStartedAt, start)
		}
		if firstTokenMs == 0 && eventType == "response.output_text.delta" {
			attemptFirstTokenMs = int(time.Since(start).Milliseconds())
			firstTokenMs = int(time.Since(requestStartedAt).Milliseconds())
			logUpstreamFirstVisible(c, "/v1/responses", attempt, eventType, requestStartedAt, start)
			h.store.ReportFirstTokenLatency(account, time.Duration(attemptFirstTokenMs)*time.Millisecond)
		}

		if eventType == responsesWebsocketEventCompleted {
			usage = extractUsage(data)
			if output := gjson.GetBytes(data, "response.output"); output.Exists() && output.IsArray() {
				completedOutput = bytes.Clone([]byte(output.Raw))
			}
			if tier := gjson.GetBytes(data, "response.service_tier").String(); tier != "" {
				actualServiceTier = tier
			}
			cacheCompletedResponse([]byte(expandedInputRaw), data)
			gotTerminal = true
		}
		if eventType == responsesWebsocketEventFailed || eventType == responsesWebsocketEventError {
			gotTerminal = true
		}

		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			writeErr = err
			return false
		}
		return !isResponsesWebsocketTerminalEvent(eventType)
	})

	attemptDuration := int(time.Since(start).Milliseconds())
	totalDuration := int(time.Since(requestStartedAt).Milliseconds())
	outcome := classifyStreamOutcome(c.Request.Context().Err(), readErr, writeErr, gotTerminal)
	if outcome.penalize {
		recyclePooledClientForAccount(account)
		h.store.ReportRequestFailure(account, outcome.failureKind, time.Duration(attemptDuration)*time.Millisecond)
	} else if outcome.logStatusCode == http.StatusOK {
		h.store.ReportRequestSuccess(account, schedulerLatency(attemptDuration, attemptFirstTokenMs))
	}
	logUpstreamAttemptResult(c, "/v1/responses", attempt, account, "", outcome.logStatusCode, attemptDuration, requestStartedAt, "")

	resolvedServiceTier := resolveServiceTier(actualServiceTier, serviceTier)
	h.logResponsesWebsocketUsage(account, model, outcome.logStatusCode, totalDuration, firstTokenMs, reasoningEffort, resolvedServiceTier, usage)

	if writeErr != nil {
		return completedOutput, writeErr
	}
	if readErr != nil {
		return completedOutput, readErr
	}
	if !gotTerminal {
		return completedOutput, writeResponsesWebsocketError(conn, http.StatusRequestTimeout, "stream closed before response.completed", "upstream_error")
	}
	return completedOutput, nil
}

func isResponsesWebsocketTerminalEvent(eventType string) bool {
	return eventType == responsesWebsocketEventCompleted || eventType == responsesWebsocketEventFailed || eventType == responsesWebsocketEventError
}

func (h *Handler) logResponsesWebsocketUsage(account *auth.Account, model string, statusCode int, durationMs int, firstTokenMs int, reasoningEffort string, serviceTier string, usage *UsageInfo) {
	if h == nil || account == nil {
		return
	}
	input := &database.UsageLogInput{
		AccountID:        account.ID(),
		Endpoint:         "/v1/responses",
		Model:            model,
		StatusCode:       statusCode,
		DurationMs:       durationMs,
		FirstTokenMs:     firstTokenMs,
		ReasoningEffort:  reasoningEffort,
		InboundEndpoint:  "/v1/responses",
		UpstreamEndpoint: "/v1/responses",
		Stream:           true,
		ServiceTier:      serviceTier,
	}
	if usage != nil {
		input.PromptTokens = usage.PromptTokens
		input.CompletionTokens = usage.CompletionTokens
		input.TotalTokens = usage.TotalTokens
		input.InputTokens = usage.InputTokens
		input.OutputTokens = usage.OutputTokens
		input.ReasoningTokens = usage.ReasoningTokens
		input.CachedTokens = usage.CachedTokens
	}
	h.logUsage(input)
}

func responsesWebsocketUpstreamError(statusCode int, body []byte) (int, string, string) {
	status := normalizeUpstreamStatusCode(statusCode, body)
	if status <= 0 {
		status = http.StatusBadGateway
	}
	message := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if errType == "" {
		if status >= 500 {
			errType = "upstream_error"
		} else {
			errType = "invalid_request_error"
		}
	}
	return status, message, errType
}

func writeResponsesWebsocketError(conn *websocket.Conn, status int, message string, errType string) error {
	if conn == nil {
		return nil
	}
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(status)
	}
	errType = strings.TrimSpace(errType)
	if errType == "" {
		errType = "server_error"
	}

	payload := []byte(`{"type":"error","status":0,"error":{"message":"","type":""}}`)
	payload, _ = sjson.SetBytes(payload, "status", status)
	payload, _ = sjson.SetBytes(payload, "error.message", message)
	payload, _ = sjson.SetBytes(payload, "error.type", errType)
	return conn.WriteMessage(websocket.TextMessage, payload)
}
