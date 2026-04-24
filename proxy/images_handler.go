package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	defaultImagesMainModel = "gpt-5.4-mini"
	defaultImagesToolModel = "gpt-image-2"
)

type imageCallResult struct {
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
}

// ImagesGenerations 处理 /v1/images/generations。
func (h *Handler) ImagesGenerations(c *gin.Context) {
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidRequest, "Failed to read request body", api.ErrorTypeInvalidRequest))
		return
	}
	if len(rawBody) > security.MaxRequestBodySize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": gin.H{"message": "请求体过大", "type": "invalid_request_error"},
		})
		return
	}
	if !json.Valid(rawBody) {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidRequest, "Request body must be valid JSON", api.ErrorTypeInvalidRequest))
		return
	}

	prompt := strings.TrimSpace(gjson.GetBytes(rawBody, "prompt").String())
	if prompt == "" {
		api.SendMissingFieldError(c, "prompt")
		return
	}

	imageModel := strings.TrimSpace(gjson.GetBytes(rawBody, "model").String())
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	if !SupportsImageRequests(imageModel) {
		api.SendError(c, api.NewAPIError(
			api.ErrCodeUnsupportedModel,
			fmt.Sprintf("model '%s' is not supported on image endpoints", imageModel),
			api.ErrorTypeInvalidRequest,
		))
		return
	}

	responseFormat := strings.ToLower(strings.TrimSpace(gjson.GetBytes(rawBody, "response_format").String()))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	if responseFormat != "b64_json" && responseFormat != "url" {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidParameter, "response_format must be one of: b64_json, url", api.ErrorTypeInvalidRequest))
		return
	}

	if gjson.GetBytes(rawBody, "n").Exists() {
		log.Printf("[images/generations] ignore unsupported n parameter")
		rawBody, _ = sjson.DeleteBytes(rawBody, "n")
	}

	codexBody := buildImagesRequest(rawBody, prompt, nil, imageModel, "generate")
	h.executeImageEndpoint(c, "/v1/images/generations", imageModel, responseFormat, codexBody)
}

// ImagesEdits 处理 /v1/images/edits（当前支持 JSON 请求）。
func (h *Handler) ImagesEdits(c *gin.Context) {
	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		api.SendError(c, api.NewAPIError(
			api.ErrCodeInvalidRequest,
			"multipart/form-data is not supported yet for /v1/images/edits, please send application/json",
			api.ErrorTypeInvalidRequest,
		))
		return
	}
	if contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		api.SendError(c, api.NewAPIError(
			api.ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported Content-Type %q, only application/json is supported currently", contentType),
			api.ErrorTypeInvalidRequest,
		))
		return
	}

	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidRequest, "Failed to read request body", api.ErrorTypeInvalidRequest))
		return
	}
	if len(rawBody) > security.MaxRequestBodySize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": gin.H{"message": "请求体过大", "type": "invalid_request_error"},
		})
		return
	}
	if !json.Valid(rawBody) {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidRequest, "Request body must be valid JSON", api.ErrorTypeInvalidRequest))
		return
	}

	prompt := strings.TrimSpace(gjson.GetBytes(rawBody, "prompt").String())
	if prompt == "" {
		api.SendMissingFieldError(c, "prompt")
		return
	}

	imageModel := strings.TrimSpace(gjson.GetBytes(rawBody, "model").String())
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	if !SupportsImageRequests(imageModel) {
		api.SendError(c, api.NewAPIError(
			api.ErrCodeUnsupportedModel,
			fmt.Sprintf("model '%s' is not supported on image endpoints", imageModel),
			api.ErrorTypeInvalidRequest,
		))
		return
	}

	responseFormat := strings.ToLower(strings.TrimSpace(gjson.GetBytes(rawBody, "response_format").String()))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	if responseFormat != "b64_json" && responseFormat != "url" {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidParameter, "response_format must be one of: b64_json, url", api.ErrorTypeInvalidRequest))
		return
	}

	var images []string
	collectImage := func(item gjson.Result) {
		switch {
		case item.Type == gjson.String:
			value := strings.TrimSpace(item.String())
			if value != "" {
				images = append(images, normalizeImageInput(value))
			}
		case item.IsObject():
			for _, path := range []string{"image_url", "url", "image_url.url"} {
				value := strings.TrimSpace(item.Get(path).String())
				if value != "" {
					images = append(images, normalizeImageInput(value))
					return
				}
			}
		}
	}
	for _, field := range []string{"image", "images"} {
		v := gjson.GetBytes(rawBody, field)
		if !v.Exists() {
			continue
		}
		if v.IsArray() {
			for _, item := range v.Array() {
				collectImage(item)
			}
			continue
		}
		collectImage(v)
	}
	if len(images) == 0 {
		api.SendMissingFieldError(c, "image")
		return
	}

	if gjson.GetBytes(rawBody, "n").Exists() {
		log.Printf("[images/edits] ignore unsupported n parameter")
		rawBody, _ = sjson.DeleteBytes(rawBody, "n")
	}

	codexBody := buildImagesRequest(rawBody, prompt, images, imageModel, "edit")
	h.executeImageEndpoint(c, "/v1/images/edits", imageModel, responseFormat, codexBody)
}

func normalizeImageInput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "data:") {
		return raw
	}
	return "data:image/png;base64," + raw
}

func buildImagesRequest(rawBody []byte, prompt string, images []string, imageModel string, action string) []byte {
	body := []byte(`{}`)
	body, _ = sjson.SetBytes(body, "model", defaultImagesMainModel)
	body, _ = sjson.SetBytes(body, "instructions", "")
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.SetBytes(body, "store", false)
	body, _ = sjson.SetBytes(body, "parallel_tool_calls", true)
	body, _ = sjson.SetBytes(body, "include", []string{"reasoning.encrypted_content"})
	body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
	body, _ = sjson.SetBytes(body, "reasoning.summary", "auto")
	body, _ = sjson.SetBytes(body, "tool_choice.type", "image_generation")

	tool := []byte(`{"type":"image_generation"}`)
	tool, _ = sjson.SetBytes(tool, "action", action)
	tool, _ = sjson.SetBytes(tool, "model", imageModel)
	for _, field := range []string{"size", "quality", "background", "output_format", "output_compression", "moderation", "partial_images"} {
		v := gjson.GetBytes(rawBody, field)
		if !v.Exists() {
			continue
		}
		tool, _ = sjson.SetRawBytes(tool, field, []byte(v.Raw))
	}
	body, _ = sjson.SetRawBytes(body, "tools.0", tool)

	content := []byte(`[]`)
	textPart := []byte(`{"type":"input_text","text":""}`)
	textPart, _ = sjson.SetBytes(textPart, "text", prompt)
	content, _ = sjson.SetRawBytes(content, "0", textPart)
	contentIndex := 1
	for _, img := range images {
		imagePart := []byte(`{"type":"input_image","image_url":""}`)
		imagePart, _ = sjson.SetBytes(imagePart, "image_url", img)
		content, _ = sjson.SetRawBytes(content, fmt.Sprintf("%d", contentIndex), imagePart)
		contentIndex++
	}
	body, _ = sjson.SetBytes(body, "input.0.type", "message")
	body, _ = sjson.SetBytes(body, "input.0.role", "user")
	body, _ = sjson.SetRawBytes(body, "input.0.content", content)
	return body
}

func (h *Handler) executeImageEndpoint(c *gin.Context, endpoint, imageModel, responseFormat string, codexBody []byte) {
	requestStartedAt := requestStartTime(c)
	logRequestLifecycleStart(c, endpoint, imageModel, false, "")

	maxRetries := h.getMaxRetries()
	var lastErr error
	var lastStatusCode int
	var lastBody []byte
	excludeAccounts := make(map[int64]bool)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		acquireStartedAt := time.Now()
		account := h.acquireAccountForRequest(c, excludeAccounts)
		attemptAcquireMs := int(time.Since(acquireStartedAt).Milliseconds())
		if account == nil {
			if lastStatusCode != 0 && len(lastBody) > 0 {
				h.sendFinalUpstreamError(c, lastStatusCode, lastBody)
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{"message": "无可用账号，请稍后重试", "type": "server_error"},
			})
			return
		}

		start := time.Now()
		proxyURL := h.store.NextProxy()
		logRequestDispatch(c, endpoint, attempt+1, account, proxyURL, imageModel, "", attemptAcquireMs)

		apiKey := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		deviceCfg := h.deviceCfg
		if deviceCfg == nil {
			deviceCfg = DefaultDeviceProfileConfig()
		}
		downstreamHeaders := c.Request.Header.Clone()
		sessionID := ResolveSessionID(c.GetHeader("Authorization"), codexBody)

		resp, _, reqErr := ExecuteRequestTraced(c.Request.Context(), account, codexBody, sessionID, proxyURL, apiKey, deviceCfg, downstreamHeaders, false)
		durationMs := int(time.Since(start).Milliseconds())

		if reqErr != nil {
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastErr = reqErr
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			h.applyCooldown(account, resp.StatusCode, errBody, resp)
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastStatusCode = resp.StatusCode
			lastBody = errBody
			if isRetryableStatus(resp.StatusCode, errBody) && attempt < maxRetries {
				continue
			}
			h.sendFinalUpstreamError(c, resp.StatusCode, errBody)
			return
		}

		completed, streamItems, failedMsg, readErr := collectCompletedImageEvent(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastErr = readErr
			continue
		}
		if failedMsg != "" {
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastErr = fmt.Errorf("upstream image request failed: %s", failedMsg)
			continue
		}

		results, createdAt, usageRaw, firstMeta, parseErr := extractImagesFromResponsesCompleted(completed)
		if parseErr != nil {
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastErr = parseErr
			continue
		}
		if len(results) == 0 && len(streamItems) > 0 {
			results = streamItems
			firstMeta = streamItems[0]
		}
		if len(results) == 0 {
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastErr = fmt.Errorf("upstream did not return image output")
			continue
		}

		payload, buildErr := buildImagesAPIResponse(results, createdAt, usageRaw, firstMeta, responseFormat)
		if buildErr != nil {
			h.store.Release(account)
			excludeAccounts[account.ID()] = true
			lastErr = buildErr
			continue
		}

		logInput := &database.UsageLogInput{
			AccountID:        account.ID(),
			Endpoint:         endpoint,
			Model:            imageModel,
			StatusCode:       http.StatusOK,
			DurationMs:       int(time.Since(requestStartedAt).Milliseconds()),
			InboundEndpoint:  endpoint,
			UpstreamEndpoint: "/v1/responses",
			Stream:           false,
		}
		if usage := extractUsage(completed); usage != nil {
			logInput.PromptTokens = usage.PromptTokens
			logInput.CompletionTokens = usage.CompletionTokens
			logInput.TotalTokens = usage.TotalTokens
			logInput.InputTokens = usage.InputTokens
			logInput.OutputTokens = usage.OutputTokens
			logInput.ReasoningTokens = usage.ReasoningTokens
			logInput.CachedTokens = usage.CachedTokens
		}
		h.logUsage(logInput)
		h.store.ReportRequestSuccess(account, schedulerLatency(durationMs, 0))
		h.store.Release(account)

		c.Data(http.StatusOK, "application/json", payload)
		return
	}

	if lastStatusCode != 0 && len(lastBody) > 0 {
		h.sendFinalUpstreamError(c, lastStatusCode, lastBody)
		return
	}
	if lastErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"message": "上游请求失败: " + lastErr.Error(), "type": "upstream_error"},
		})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{"message": "图片请求失败", "type": "upstream_error"},
	})
}

func collectCompletedImageEvent(body io.Reader) ([]byte, []imageCallResult, string, error) {
	var completedEvent []byte
	var failedMessage string
	itemsByIndex := make(map[int64]imageCallResult)
	fallbackItems := make([]imageCallResult, 0, 2)

	err := ReadSSEStream(body, func(data []byte) bool {
		eventType := gjson.GetBytes(data, "type").String()
		switch eventType {
		case "response.completed":
			// 取最后一个 completed，兼容 usage 晚到。
			completedEvent = append(completedEvent[:0], data...)
		case "response.output_item.done":
			item := gjson.GetBytes(data, "item")
			if parsed, ok := parseImageCallResultFromItem(item); ok {
				if idx := gjson.GetBytes(data, "output_index"); idx.Exists() {
					itemsByIndex[idx.Int()] = parsed
				} else {
					fallbackItems = append(fallbackItems, parsed)
				}
			}
		case "response.failed":
			failedMessage = strings.TrimSpace(gjson.GetBytes(data, "response.error.message").String())
			if failedMessage == "" {
				failedMessage = "Codex upstream error"
			}
		}
		return true
	})
	if err != nil {
		return nil, nil, "", err
	}
	if failedMessage != "" {
		return nil, nil, failedMessage, nil
	}
	if len(completedEvent) == 0 {
		return nil, nil, "", fmt.Errorf("upstream did not return response.completed")
	}
	streamItems := make([]imageCallResult, 0, len(itemsByIndex)+len(fallbackItems))
	if len(itemsByIndex) > 0 {
		indexes := make([]int64, 0, len(itemsByIndex))
		for idx := range itemsByIndex {
			indexes = append(indexes, idx)
		}
		sort.Slice(indexes, func(i, j int) bool {
			return indexes[i] < indexes[j]
		})
		for _, idx := range indexes {
			streamItems = append(streamItems, itemsByIndex[idx])
		}
	}
	streamItems = append(streamItems, fallbackItems...)
	return completedEvent, streamItems, "", nil
}

func parseImageCallResultFromItem(item gjson.Result) (imageCallResult, bool) {
	if !item.Exists() || !item.IsObject() {
		return imageCallResult{}, false
	}
	if item.Get("type").String() != "image_generation_call" {
		return imageCallResult{}, false
	}
	result := strings.TrimSpace(item.Get("result").String())
	if result == "" {
		return imageCallResult{}, false
	}
	return imageCallResult{
		Result:        result,
		RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
		OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
		Size:          strings.TrimSpace(item.Get("size").String()),
		Background:    strings.TrimSpace(item.Get("background").String()),
		Quality:       strings.TrimSpace(item.Get("quality").String()),
	}, true
}

func extractImagesFromResponsesCompleted(payload []byte) (results []imageCallResult, createdAt int64, usageRaw []byte, firstMeta imageCallResult, err error) {
	if gjson.GetBytes(payload, "type").String() != "response.completed" {
		return nil, 0, nil, imageCallResult{}, fmt.Errorf("unexpected event type")
	}

	createdAt = gjson.GetBytes(payload, "response.created_at").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}

	output := gjson.GetBytes(payload, "response.output")
	if output.IsArray() {
		for _, item := range output.Array() {
			entry, ok := parseImageCallResultFromItem(item)
			if !ok {
				continue
			}
			if len(results) == 0 {
				firstMeta = entry
			}
			results = append(results, entry)
		}
	}
	if usage := gjson.GetBytes(payload, "response.tool_usage.image_gen"); usage.Exists() && usage.IsObject() {
		usageRaw = []byte(usage.Raw)
	}
	return results, createdAt, usageRaw, firstMeta, nil
}

func buildImagesAPIResponse(results []imageCallResult, createdAt int64, usageRaw []byte, firstMeta imageCallResult, responseFormat string) ([]byte, error) {
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", createdAt)

	for _, img := range results {
		item := []byte(`{}`)
		if responseFormat == "url" {
			item, _ = sjson.SetBytes(item, "url", "data:"+mimeTypeFromOutputFormat(img.OutputFormat)+";base64,"+img.Result)
		} else {
			item, _ = sjson.SetBytes(item, "b64_json", img.Result)
		}
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}

	if firstMeta.Background != "" {
		out, _ = sjson.SetBytes(out, "background", firstMeta.Background)
	}
	if firstMeta.OutputFormat != "" {
		out, _ = sjson.SetBytes(out, "output_format", firstMeta.OutputFormat)
	}
	if firstMeta.Quality != "" {
		out, _ = sjson.SetBytes(out, "quality", firstMeta.Quality)
	}
	if firstMeta.Size != "" {
		out, _ = sjson.SetBytes(out, "size", firstMeta.Size)
	}
	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", usageRaw)
	}

	return out, nil
}

func mimeTypeFromOutputFormat(outputFormat string) string {
	if outputFormat == "" {
		return "image/png"
	}
	if strings.Contains(outputFormat, "/") {
		return outputFormat
	}
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
