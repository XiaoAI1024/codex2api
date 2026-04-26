package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
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

var imageStreamFirstPacketGrace = 2 * time.Second

type imageCallResult struct {
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
}

type imagesEditRequest struct {
	rawBody        []byte
	prompt         string
	imageModel     string
	responseFormat string
	images         []string
	mask           string
	stream         bool
}

type imageStreamForwardResult struct {
	wroteDownstream bool
	sawRetryBarrier bool
	writeErr        error
}

func (r imageStreamForwardResult) canTransparentRetry(c *gin.Context) bool {
	return c.Request.Context().Err() == nil && !r.wroteDownstream && !r.sawRetryBarrier && r.writeErr == nil && !c.Writer.Written()
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
	h.executeImageEndpoint(c, "/v1/images/generations", imageModel, responseFormat, codexBody, gjson.GetBytes(rawBody, "stream").Bool(), "image_generation")
}

// ImagesEdits 处理 /v1/images/edits。
func (h *Handler) ImagesEdits(c *gin.Context) {
	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(security.MaxRequestBodySize))
		parsed, err := parseMultipartImagesEditRequest(c.Request)
		if err != nil {
			api.SendError(c, api.NewAPIError(api.ErrCodeInvalidRequest, "Invalid multipart request: "+err.Error(), api.ErrorTypeInvalidRequest))
			return
		}
		h.handleParsedImagesEdit(c, parsed)
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

	parsed, err := parseJSONImagesEditRequest(rawBody)
	if err != nil {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidRequest, err.Error(), api.ErrorTypeInvalidRequest))
		return
	}
	h.handleParsedImagesEdit(c, parsed)
}

func (h *Handler) handleParsedImagesEdit(c *gin.Context, parsed imagesEditRequest) {
	if parsed.prompt == "" {
		api.SendMissingFieldError(c, "prompt")
		return
	}
	if !SupportsImageRequests(parsed.imageModel) {
		api.SendError(c, api.NewAPIError(
			api.ErrCodeUnsupportedModel,
			fmt.Sprintf("model '%s' is not supported on image endpoints", parsed.imageModel),
			api.ErrorTypeInvalidRequest,
		))
		return
	}
	if parsed.responseFormat != "b64_json" && parsed.responseFormat != "url" {
		api.SendError(c, api.NewAPIError(api.ErrCodeInvalidParameter, "response_format must be one of: b64_json, url", api.ErrorTypeInvalidRequest))
		return
	}
	if len(parsed.images) == 0 {
		api.SendMissingFieldError(c, "image")
		return
	}
	rawBody := parsed.rawBody
	if gjson.GetBytes(rawBody, "n").Exists() {
		log.Printf("[images/edits] ignore unsupported n parameter")
		rawBody, _ = sjson.DeleteBytes(rawBody, "n")
	}
	codexBody := buildImagesRequest(rawBody, parsed.prompt, parsed.images, parsed.imageModel, "edit")
	h.executeImageEndpoint(c, "/v1/images/edits", parsed.imageModel, parsed.responseFormat, codexBody, parsed.stream, "image_edit")
}

func parseJSONImagesEditRequest(rawBody []byte) (imagesEditRequest, error) {
	prompt := strings.TrimSpace(gjson.GetBytes(rawBody, "prompt").String())
	imageModel := strings.TrimSpace(gjson.GetBytes(rawBody, "model").String())
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	responseFormat := strings.ToLower(strings.TrimSpace(gjson.GetBytes(rawBody, "response_format").String()))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}

	if gjson.GetBytes(rawBody, "mask.file_id").Exists() {
		return imagesEditRequest{}, fmt.Errorf("mask.file_id is not supported, use mask.image_url instead")
	}

	var images []string
	collectImage := func(item gjson.Result) error {
		value, ok, err := parseJSONImageInputValue(item, "image")
		if err != nil {
			return err
		}
		if ok {
			images = append(images, value)
		}
		return nil
	}
	for _, field := range []string{"image", "images"} {
		v := gjson.GetBytes(rawBody, field)
		if !v.Exists() {
			continue
		}
		if v.IsArray() {
			for _, item := range v.Array() {
				if err := collectImage(item); err != nil {
					return imagesEditRequest{}, err
				}
			}
			continue
		}
		if err := collectImage(v); err != nil {
			return imagesEditRequest{}, err
		}
	}

	var mask string
	if maskValue := gjson.GetBytes(rawBody, "mask"); maskValue.Exists() {
		parsedMask, ok, err := parseJSONImageInputValue(maskValue, "mask")
		if err != nil {
			return imagesEditRequest{}, err
		}
		if ok {
			mask = parsedMask
			rawBody, _ = sjson.SetBytes(rawBody, "mask.image_url", parsedMask)
		}
	}

	return imagesEditRequest{
		rawBody:        rawBody,
		prompt:         prompt,
		imageModel:     imageModel,
		responseFormat: responseFormat,
		images:         images,
		mask:           mask,
		stream:         gjson.GetBytes(rawBody, "stream").Bool(),
	}, nil
}

func parseJSONImageInputValue(item gjson.Result, fieldName string) (string, bool, error) {
	if !item.Exists() {
		return "", false, nil
	}
	if item.Type == gjson.String {
		value := strings.TrimSpace(item.String())
		if value == "" {
			return "", false, nil
		}
		return normalizeImageInput(value), true, nil
	}
	if !item.IsObject() {
		return "", false, fmt.Errorf("%s must be a string or an object", fieldName)
	}

	for _, path := range []string{"image_url.url", "image_url", "url"} {
		candidate := item.Get(path)
		if !candidate.Exists() || candidate.Type != gjson.String {
			continue
		}
		value := strings.TrimSpace(candidate.String())
		if value == "" {
			continue
		}
		return normalizeImageInput(value), true, nil
	}

	return "", false, fmt.Errorf("%s object must contain image_url.url, image_url, or url", fieldName)
}

func parseMultipartImagesEditRequest(r *http.Request) (imagesEditRequest, error) {
	if err := r.ParseMultipartForm(int64(security.MaxRequestBodySize)); err != nil {
		return imagesEditRequest{}, err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	imageModel := strings.TrimSpace(r.FormValue("model"))
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	responseFormat := strings.ToLower(strings.TrimSpace(r.FormValue("response_format")))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}

	var images []string
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		files := r.MultipartForm.File["image[]"]
		if len(files) == 0 {
			files = r.MultipartForm.File["image"]
		}
		for _, file := range files {
			dataURL, err := multipartFileToDataURL(file)
			if err != nil {
				return imagesEditRequest{}, err
			}
			images = append(images, dataURL)
		}
	}

	body := []byte(`{}`)
	for _, field := range []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"} {
		if value := strings.TrimSpace(r.FormValue(field)); value != "" {
			body, _ = sjson.SetBytes(body, field, value)
		}
	}
	for _, field := range []string{"output_compression", "partial_images"} {
		if value := strings.TrimSpace(r.FormValue(field)); value != "" {
			body, _ = sjson.SetBytes(body, field, parseIntField(value, 0))
		}
	}
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		if masks := r.MultipartForm.File["mask"]; len(masks) > 0 && masks[0] != nil {
			dataURL, err := multipartFileToDataURL(masks[0])
			if err != nil {
				return imagesEditRequest{}, err
			}
			body, _ = sjson.SetBytes(body, "mask.image_url", dataURL)
		}
	}

	return imagesEditRequest{
		rawBody:        body,
		prompt:         prompt,
		imageModel:     imageModel,
		responseFormat: responseFormat,
		images:         images,
		mask:           gjson.GetBytes(body, "mask.image_url").String(),
		stream:         parseBoolField(r.FormValue("stream"), false),
	}, nil
}

func multipartFileToDataURL(file *multipart.FileHeader) (string, error) {
	if file == nil {
		return "", fmt.Errorf("missing file")
	}
	f, err := file.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	contentType := strings.TrimSpace(file.Header.Get("Content-Type"))
	if contentType == "" || contentType == "application/octet-stream" {
		if ext := strings.TrimSpace(filepath.Ext(file.Filename)); ext != "" {
			if byExt := strings.TrimSpace(mime.TypeByExtension(ext)); byExt != "" {
				contentType = byExt
			}
		}
	}
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func parseIntField(value string, fallback int64) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseBoolField(value string, fallback bool) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
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
	if action == "edit" {
		if v := gjson.GetBytes(rawBody, "input_fidelity"); v.Exists() {
			tool, _ = sjson.SetRawBytes(tool, "input_fidelity", []byte(v.Raw))
		}
		if mask := strings.TrimSpace(gjson.GetBytes(rawBody, "mask.image_url").String()); mask != "" {
			tool, _ = sjson.SetBytes(tool, "input_image_mask.image_url", mask)
		}
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

func (h *Handler) executeImageEndpoint(c *gin.Context, endpoint, imageModel, responseFormat string, codexBody []byte, stream bool, streamPrefix string) {
	requestStartedAt := requestStartTime(c)
	logRequestLifecycleStart(c, endpoint, imageModel, stream, "")

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
			h.persistUsageAndSettleFromResponse(account, resp)
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

		if stream {
			streamResult, streamErr := h.forwardImageStream(c, resp.Body, responseFormat, streamPrefix, account, endpoint, imageModel, requestStartedAt, start)
			resp.Body.Close()
			h.persistUsageAndSettleFromResponse(account, resp)
			h.store.Release(account)
			if streamErr != nil {
				if streamResult.canTransparentRetry(c) && attempt < maxRetries {
					excludeAccounts[account.ID()] = true
					lastErr = streamErr
					continue
				}
				if !streamResult.wroteDownstream && !c.Writer.Written() {
					c.JSON(http.StatusBadGateway, gin.H{
						"error": gin.H{"message": "上游请求失败: " + streamErr.Error(), "type": "upstream_error"},
					})
				}
				return
			}
			return
		}

		completed, streamItems, failedMsg, readErr := collectCompletedImageEvent(resp.Body)
		resp.Body.Close()
		h.persistUsageAndSettleFromResponse(account, resp)
		if readErr != nil {
			h.finishImageNonStreamUpstreamFailure(c, account, endpoint, imageModel, requestStartedAt, start, readErr)
			return
		}
		if failedMsg != "" {
			h.finishImageNonStreamUpstreamFailure(c, account, endpoint, imageModel, requestStartedAt, start, fmt.Errorf("upstream image request failed: %s", failedMsg))
			return
		}

		results, createdAt, usageRaw, firstMeta, parseErr := extractImagesFromResponsesCompleted(completed)
		if parseErr != nil {
			h.finishImageNonStreamUpstreamFailure(c, account, endpoint, imageModel, requestStartedAt, start, parseErr)
			return
		}
		if len(results) == 0 && len(streamItems) > 0 {
			results = streamItems
			firstMeta = streamItems[0]
		}
		if len(results) == 0 {
			h.finishImageNonStreamUpstreamFailure(c, account, endpoint, imageModel, requestStartedAt, start, fmt.Errorf("upstream did not return image output"))
			return
		}

		payload, buildErr := buildImagesAPIResponse(results, createdAt, usageRaw, firstMeta, responseFormat)
		if buildErr != nil {
			h.finishImageNonStreamUpstreamFailure(c, account, endpoint, imageModel, requestStartedAt, start, buildErr)
			return
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

func (h *Handler) finishImageNonStreamUpstreamFailure(c *gin.Context, account *auth.Account, endpoint string, imageModel string, requestStartedAt time.Time, start time.Time, err error) {
	attemptDuration := int(time.Since(start).Milliseconds())
	totalDuration := int(time.Since(requestStartedAt).Milliseconds())
	if h != nil && h.store != nil {
		h.store.ReportRequestFailure(account, "server", schedulerLatency(attemptDuration, 0))
	}
	h.logUsage(&database.UsageLogInput{
		AccountID:        account.ID(),
		Endpoint:         endpoint,
		Model:            imageModel,
		StatusCode:       http.StatusBadGateway,
		DurationMs:       totalDuration,
		InboundEndpoint:  endpoint,
		UpstreamEndpoint: "/v1/responses",
		Stream:           false,
	})
	if h != nil && h.store != nil {
		h.store.Release(account)
	}
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{"message": "上游请求失败: " + err.Error(), "type": "upstream_error"},
	})
}

func collectCompletedImageEvent(body io.Reader) ([]byte, []imageCallResult, string, error) {
	var completedEvent []byte
	var fallbackCompletedEvent []byte
	var latestUsageEvent []byte
	var failedMessage string
	itemsByIndex := make(map[int64]imageCallResult)
	fallbackItems := make([]imageCallResult, 0, 2)

	err := ReadSSEStream(body, func(data []byte) bool {
		eventType := gjson.GetBytes(data, "type").String()
		switch eventType {
		case "response.completed":
			eventData := append([]byte(nil), data...)
			if completedEventHasUsage(eventData) {
				latestUsageEvent = eventData
			}
			if completedEventHasImages(eventData) {
				completedEvent = append(completedEvent[:0], eventData...)
			} else {
				fallbackCompletedEvent = append(fallbackCompletedEvent[:0], eventData...)
			}
			if len(completedEvent) > 0 && len(latestUsageEvent) > 0 {
				completedEvent = mergeCompletedUsage(completedEvent, latestUsageEvent)
			}
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
	if len(completedEvent) == 0 && len(fallbackCompletedEvent) > 0 {
		completedEvent = append(completedEvent[:0], fallbackCompletedEvent...)
		if len(latestUsageEvent) > 0 {
			completedEvent = mergeCompletedUsage(completedEvent, latestUsageEvent)
		}
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

func completedEventHasImages(payload []byte) bool {
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() {
		return false
	}
	for _, item := range output.Array() {
		if _, ok := parseImageCallResultFromItem(item); ok {
			return true
		}
	}
	return false
}

func completedEventHasUsage(payload []byte) bool {
	for _, path := range []string{"response.tool_usage.image_gen", "response.usage", "usage"} {
		if usage := gjson.GetBytes(payload, path); usage.Exists() && usage.IsObject() {
			return true
		}
	}
	return false
}

func mergeCompletedUsage(base []byte, usageEvent []byte) []byte {
	if len(base) == 0 {
		return append([]byte(nil), usageEvent...)
	}
	merged := append([]byte(nil), base...)
	for _, path := range []string{"response.tool_usage.image_gen", "response.usage", "usage"} {
		if usage := gjson.GetBytes(usageEvent, path); usage.Exists() && usage.IsObject() {
			merged, _ = sjson.SetRawBytes(merged, path, []byte(usage.Raw))
		}
	}
	return merged
}

func (h *Handler) forwardImageStream(c *gin.Context, body io.Reader, responseFormat string, streamPrefix string, account *auth.Account, endpoint string, imageModel string, requestStartedAt time.Time, start time.Time) (imageStreamForwardResult, error) {
	result := imageStreamForwardResult{}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"message": "streaming not supported", "type": "server_error"},
		})
		result.wroteDownstream = true
		return result, fmt.Errorf("streaming not supported")
	}

	headersPrepared := false
	prepareHeaders := func() {
		if headersPrepared {
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		headersPrepared = true
	}

	writeEvent := func(eventName string, payload []byte) error {
		prepareHeaders()
		if eventName != "" {
			if _, err := fmt.Fprintf(c.Writer, "event: %s\n", eventName); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	writeKeepAlive := func() error {
		prepareHeaders()
		if _, err := fmt.Fprint(c.Writer, ": keep-alive\n\n"); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	var usage *UsageInfo
	var writeErr error
	var readErr error
	gotTerminal := false
	wroteAny := false
	streamFailureMessage := ""
	firstFrameRecorded := false
	itemsByIndex := make(map[int64]imageCallResult)
	fallbackItems := make([]imageCallResult, 0, 2)

	writeStreamFailure := func(message string) bool {
		streamFailureMessage = message
		if err := writeImageStreamError(writeEvent, http.StatusBadGateway, message); err != nil {
			writeErr = err
			result.writeErr = err
			return false
		}
		wroteAny = true
		result.wroteDownstream = true
		return false
	}

	type imageStreamReadEvent struct {
		data []byte
		err  error
	}
	readEvents := make(chan imageStreamReadEvent, 1)
	doneReading := make(chan struct{})
	defer close(doneReading)
	go func() {
		err := ReadSSEStream(body, func(data []byte) bool {
			dataCopy := append([]byte(nil), data...)
			select {
			case readEvents <- imageStreamReadEvent{data: dataCopy}:
				return true
			case <-doneReading:
				return false
			}
		})
		select {
		case readEvents <- imageStreamReadEvent{err: err}:
		case <-doneReading:
		}
	}()

	keepAliveTimer := time.NewTimer(imageStreamFirstPacketGrace)
	defer keepAliveTimer.Stop()
	keepAliveC := keepAliveTimer.C
	keepReading := true

	for keepReading {
		select {
		case event := <-readEvents:
			if event.data == nil {
				readErr = event.err
				keepReading = false
				break
			}
			data := event.data
			eventType := gjson.GetBytes(data, "type").String()
			if !firstFrameRecorded {
				firstFrameRecorded = true
				logUpstreamFirstFrame(c, endpoint, 1, eventType, requestStartedAt, start)
			}
			result.sawRetryBarrier = true
			if isImageStreamRetryBarrierEvent(eventType, data) {
				result.sawRetryBarrier = true
			}

			switch eventType {
			case "response.image_generation_call.partial_image":
				b64 := strings.TrimSpace(gjson.GetBytes(data, "partial_image_b64").String())
				if b64 == "" {
					continue
				}
				eventName := streamPrefix + ".partial_image"
				payload := buildImageStreamPayload(eventName, responseFormat, imageCallResult{
					Result:       b64,
					OutputFormat: strings.TrimSpace(gjson.GetBytes(data, "output_format").String()),
				}, gjson.GetBytes(data, "partial_image_index").Int(), nil)
				writeErr = writeEvent(eventName, payload)
				result.writeErr = writeErr
				wroteAny = true
				result.wroteDownstream = true
				if writeErr != nil {
					keepReading = false
				}
			case "response.output_item.done":
				item := gjson.GetBytes(data, "item")
				if parsed, ok := parseImageCallResultFromItem(item); ok {
					if idx := gjson.GetBytes(data, "output_index"); idx.Exists() {
						itemsByIndex[idx.Int()] = parsed
					} else {
						fallbackItems = append(fallbackItems, parsed)
					}
				}
			case "response.completed":
				gotTerminal = true
				result.sawRetryBarrier = true
				usage = extractUsage(data)
				results, _, usageRaw, _, err := extractImagesFromResponsesCompleted(data)
				if err != nil {
					keepReading = writeStreamFailure(err.Error())
					break
				}
				if len(results) == 0 {
					results = sortedImageStreamItems(itemsByIndex, fallbackItems)
				}
				if len(results) == 0 {
					keepReading = writeStreamFailure("upstream did not return image output")
					break
				}
				eventName := streamPrefix + ".completed"
				for _, img := range results {
					payload := buildImageStreamPayload(eventName, responseFormat, img, 0, usageRaw)
					if err := writeEvent(eventName, payload); err != nil {
						writeErr = err
						result.writeErr = err
						keepReading = false
						break
					}
					wroteAny = true
					result.wroteDownstream = true
				}
				keepReading = false
			case "response.failed":
				gotTerminal = true
				result.sawRetryBarrier = true
				msg := strings.TrimSpace(gjson.GetBytes(data, "response.error.message").String())
				if msg == "" {
					msg = "Codex upstream error"
				}
				keepReading = writeStreamFailure(msg)
			case "error":
				result.sawRetryBarrier = true
				msg := strings.TrimSpace(gjson.GetBytes(data, "error.message").String())
				if msg == "" {
					msg = strings.TrimSpace(gjson.GetBytes(data, "message").String())
				}
				if msg == "" {
					msg = "Codex upstream error"
				}
				keepReading = writeStreamFailure(msg)
			}
		case <-keepAliveC:
			keepAliveC = nil
			if !wroteAny && !c.Writer.Written() {
				if err := writeKeepAlive(); err != nil {
					writeErr = err
					result.writeErr = err
					keepReading = false
					break
				}
				wroteAny = true
				result.wroteDownstream = true
			}
		case <-c.Request.Context().Done():
			readErr = c.Request.Context().Err()
			keepReading = false
			if closer, ok := body.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}

	attemptDuration := int(time.Since(start).Milliseconds())
	totalDuration := int(time.Since(requestStartedAt).Milliseconds())
	outcome := classifyStreamOutcome(c.Request.Context().Err(), readErr, writeErr, gotTerminal)
	if streamFailureMessage != "" {
		outcome = streamOutcome{
			logStatusCode:  http.StatusBadGateway,
			failureKind:    "server",
			failureMessage: "上游图片请求失败: " + streamFailureMessage,
			penalize:       true,
		}
	}
	if outcome.penalize {
		recyclePooledClientForAccount(account)
		h.store.ReportRequestFailure(account, outcome.failureKind, time.Duration(attemptDuration)*time.Millisecond)
	} else if outcome.logStatusCode == http.StatusOK {
		h.store.ReportRequestSuccess(account, schedulerLatency(attemptDuration, 0))
	}
	logInput := &database.UsageLogInput{
		AccountID:        account.ID(),
		Endpoint:         endpoint,
		Model:            imageModel,
		StatusCode:       outcome.logStatusCode,
		DurationMs:       totalDuration,
		InboundEndpoint:  endpoint,
		UpstreamEndpoint: "/v1/responses",
		Stream:           true,
	}
	if usage != nil {
		logInput.PromptTokens = usage.PromptTokens
		logInput.CompletionTokens = usage.CompletionTokens
		logInput.TotalTokens = usage.TotalTokens
		logInput.InputTokens = usage.InputTokens
		logInput.OutputTokens = usage.OutputTokens
		logInput.ReasoningTokens = usage.ReasoningTokens
		logInput.CachedTokens = usage.CachedTokens
	}
	h.logUsage(logInput)
	logUpstreamAttemptResult(c, endpoint, 1, account, "", outcome.logStatusCode, attemptDuration, requestStartedAt, "")

	if writeErr != nil {
		return result, writeErr
	}
	if streamFailureMessage != "" {
		return result, fmt.Errorf("upstream image request failed: %s", streamFailureMessage)
	}
	if readErr != nil {
		if wroteAny {
			_ = writeImageStreamError(writeEvent, http.StatusBadGateway, "upstream stream read failed: "+readErr.Error())
			result.wroteDownstream = true
			return result, readErr
		}
		return result, readErr
	}
	if !gotTerminal {
		err := fmt.Errorf("upstream stream closed before response.completed")
		if wroteAny {
			_ = writeImageStreamError(writeEvent, http.StatusBadGateway, err.Error())
			result.wroteDownstream = true
		}
		return result, err
	}
	return result, nil
}

func isImageStreamRetryBarrierEvent(eventType string, data []byte) bool {
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	if eventType == "" {
		return false
	}
	if eventType == "response.completed" || eventType == "response.failed" || eventType == "error" {
		return true
	}
	if strings.Contains(eventType, "image_generation_call") || strings.Contains(eventType, "partial_image") || strings.Contains(eventType, "error") {
		return true
	}
	if item := gjson.GetBytes(data, "item"); item.Exists() && item.Get("type").String() == "image_generation_call" {
		return true
	}
	if output := gjson.GetBytes(data, "response.output"); output.IsArray() {
		for _, item := range output.Array() {
			if item.Get("type").String() == "image_generation_call" {
				return true
			}
		}
	}
	return false
}

func writeImageStreamError(writeEvent func(string, []byte) error, status int, message string) error {
	payload := []byte(`{"error":{"message":"","type":"upstream_error"}}`)
	payload, _ = sjson.SetBytes(payload, "error.message", message)
	payload, _ = sjson.SetBytes(payload, "error.code", status)
	return writeEvent("error", payload)
}

func sortedImageStreamItems(itemsByIndex map[int64]imageCallResult, fallbackItems []imageCallResult) []imageCallResult {
	items := make([]imageCallResult, 0, len(itemsByIndex)+len(fallbackItems))
	if len(itemsByIndex) > 0 {
		indexes := make([]int64, 0, len(itemsByIndex))
		for idx := range itemsByIndex {
			indexes = append(indexes, idx)
		}
		sort.Slice(indexes, func(i, j int) bool {
			return indexes[i] < indexes[j]
		})
		for _, idx := range indexes {
			items = append(items, itemsByIndex[idx])
		}
	}
	items = append(items, fallbackItems...)
	return items
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
