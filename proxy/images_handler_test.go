package proxy

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildImagesRequest_Generation(t *testing.T) {
	raw := []byte(`{"size":"1024x1024","response_format":"b64_json","n":2}`)
	body := buildImagesRequest(raw, "draw a cat", nil, "gpt-image-2", "generate")

	if got := gjson.GetBytes(body, "model").String(); got != defaultImagesMainModel {
		t.Fatalf("model mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "instructions").String(); got != "" {
		t.Fatalf("instructions mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "stream").Bool(); !got {
		t.Fatalf("stream mismatch: %v", got)
	}
	if got := gjson.GetBytes(body, "store").Bool(); got {
		t.Fatalf("store mismatch: %v", got)
	}
	if got := gjson.GetBytes(body, "parallel_tool_calls").Bool(); !got {
		t.Fatalf("parallel_tool_calls mismatch: %v", got)
	}
	if got := gjson.GetBytes(body, "include.0").String(); got != "reasoning.encrypted_content" {
		t.Fatalf("include mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "medium" {
		t.Fatalf("reasoning.effort mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "reasoning.summary").String(); got != "auto" {
		t.Fatalf("reasoning.summary mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "tool_choice.type").String(); got != "image_generation" {
		t.Fatalf("tool_choice.type mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tool type mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "tools.0.action").String(); got != "generate" {
		t.Fatalf("tool action mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "tools.0.model").String(); got != "gpt-image-2" {
		t.Fatalf("tool model mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "input.0.content.0.type").String(); got != "input_text" {
		t.Fatalf("content type mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "input.0.content.0.text").String(); got != "draw a cat" {
		t.Fatalf("prompt mismatch: %q", got)
	}
}

func TestBuildImagesRequest_Edits(t *testing.T) {
	raw := []byte(`{"quality":"high","input_fidelity":"high","mask":{"image_url":"data:image/png;base64,MASK"}}`)
	images := []string{
		"https://example.com/a.png",
		"data:image/png;base64,AAA",
	}
	body := buildImagesRequest(raw, "make it blue", images, "gpt-image-2", "edit")

	if got := gjson.GetBytes(body, "tools.0.action").String(); got != "edit" {
		t.Fatalf("tool action mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "input.0.content.#").Int(); got != 3 {
		t.Fatalf("content items mismatch: %d", got)
	}
	if got := gjson.GetBytes(body, "input.0.content.1.type").String(); got != "input_image" {
		t.Fatalf("first image item type mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "input.0.content.1.image_url").String(); got != images[0] {
		t.Fatalf("first image mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "tools.0.input_fidelity").String(); got != "high" {
		t.Fatalf("input_fidelity mismatch: %q", got)
	}
	if got := gjson.GetBytes(body, "tools.0.input_image_mask.image_url").String(); got != "data:image/png;base64,MASK" {
		t.Fatalf("mask image mismatch: %q", got)
	}
}

func TestParseMultipartImagesEditRequest(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("prompt", "edit this")
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("response_format", "url")
	_ = writer.WriteField("input_fidelity", "high")
	imagePart, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatalf("create image part: %v", err)
	}
	_, _ = imagePart.Write([]byte("fake-png-image"))
	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	if err != nil {
		t.Fatalf("create mask part: %v", err)
	}
	_, _ = maskPart.Write([]byte("fake-png-mask"))
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	parsed, err := parseMultipartImagesEditRequest(req)
	if err != nil {
		t.Fatalf("parse multipart edit request: %v", err)
	}

	if parsed.prompt != "edit this" {
		t.Fatalf("prompt = %q", parsed.prompt)
	}
	if parsed.imageModel != "gpt-image-2" {
		t.Fatalf("model = %q", parsed.imageModel)
	}
	if parsed.responseFormat != "url" {
		t.Fatalf("response_format = %q", parsed.responseFormat)
	}
	if len(parsed.images) != 1 || !strings.HasPrefix(parsed.images[0], "data:image/png;base64,") {
		t.Fatalf("images = %#v", parsed.images)
	}
	if !strings.HasPrefix(parsed.mask, "data:image/png;base64,") {
		t.Fatalf("mask = %q", parsed.mask)
	}
	if got := gjson.GetBytes(parsed.rawBody, "input_fidelity").String(); got != "high" {
		t.Fatalf("raw input_fidelity = %q", got)
	}
}

func TestBuildImageStreamPayloads(t *testing.T) {
	partial := buildImageStreamPayload("image_generation.partial_image", "b64_json", imageCallResult{
		Result:       "PART",
		OutputFormat: "png",
	}, 2, nil)
	if got := gjson.GetBytes(partial, "type").String(); got != "image_generation.partial_image" {
		t.Fatalf("partial type = %q", got)
	}
	if got := gjson.GetBytes(partial, "partial_image_index").Int(); got != 2 {
		t.Fatalf("partial index = %d", got)
	}
	if got := gjson.GetBytes(partial, "b64_json").String(); got != "PART" {
		t.Fatalf("partial b64 = %q", got)
	}

	completed := buildImageStreamPayload("image_edit.completed", "url", imageCallResult{
		Result:       "DONE",
		OutputFormat: "webp",
	}, 0, []byte(`{"total_images":1}`))
	if got := gjson.GetBytes(completed, "url").String(); got != "data:image/webp;base64,DONE" {
		t.Fatalf("completed url = %q", got)
	}
	if got := gjson.GetBytes(completed, "usage.total_images").Int(); got != 1 {
		t.Fatalf("usage.total_images = %d", got)
	}
}

func TestExtractImagesFromResponsesCompleted(t *testing.T) {
	payload := []byte(`{
		"type":"response.completed",
		"response":{
			"created_at":1710000000,
			"output":[
				{"type":"image_generation_call","result":"AAAA","revised_prompt":"cat","output_format":"png"}
			],
			"tool_usage":{"image_gen":{"total_images":1}}
		}
	}`)
	results, createdAt, usageRaw, firstMeta, err := extractImagesFromResponsesCompleted(payload)
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if createdAt != 1710000000 {
		t.Fatalf("created_at mismatch: %d", createdAt)
	}
	if len(results) != 1 {
		t.Fatalf("results len mismatch: %d", len(results))
	}
	if results[0].Result != "AAAA" {
		t.Fatalf("result mismatch: %q", results[0].Result)
	}
	if firstMeta.RevisedPrompt != "cat" {
		t.Fatalf("first meta revised prompt mismatch: %q", firstMeta.RevisedPrompt)
	}
	if !strings.Contains(string(usageRaw), "total_images") {
		t.Fatalf("usage raw missing total_images: %s", string(usageRaw))
	}
}

func TestNormalizeImageInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "http url", in: "http://example.com/a.png", want: "http://example.com/a.png"},
		{name: "https url", in: "https://example.com/a.png", want: "https://example.com/a.png"},
		{name: "data uri", in: "data:image/jpeg;base64,AAA", want: "data:image/jpeg;base64,AAA"},
		{name: "raw base64", in: "AAA", want: "data:image/png;base64,AAA"},
		{name: "trim spaces", in: "  BBB  ", want: "data:image/png;base64,BBB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeImageInput(tc.in); got != tc.want {
				t.Fatalf("normalizeImageInput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCollectCompletedImageEvent_LateCompletedWins(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`data: {"type":"response.completed","response":{"output":[{"type":"image_generation_call","result":"OLD"}]}}`,
		"",
		`data: {"type":"response.completed","response":{"output":[{"type":"image_generation_call","result":"NEW"}],"tool_usage":{"image_gen":{"total_images":1}}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n"))

	completed, streamItems, failed, err := collectCompletedImageEvent(stream)
	if err != nil {
		t.Fatalf("collectCompletedImageEvent failed: %v", err)
	}
	if failed != "" {
		t.Fatalf("failed message should be empty, got %q", failed)
	}
	if len(streamItems) != 0 {
		t.Fatalf("streamItems should be empty when completed contains output, got %d", len(streamItems))
	}
	if got := gjson.GetBytes(completed, "response.output.0.result").String(); got != "NEW" {
		t.Fatalf("latest completed result = %q, want %q", got, "NEW")
	}
	if got := gjson.GetBytes(completed, "response.tool_usage.image_gen.total_images").Int(); got != 1 {
		t.Fatalf("tool_usage.total_images = %d, want 1", got)
	}
}

func TestCollectCompletedImageEvent_FailedMessage(t *testing.T) {
	stream := strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"boom\"}}}\n\n")

	completed, streamItems, failed, err := collectCompletedImageEvent(stream)
	if err != nil {
		t.Fatalf("collectCompletedImageEvent failed: %v", err)
	}
	if completed != nil {
		t.Fatalf("completed should be nil on failure, got %s", string(completed))
	}
	if len(streamItems) != 0 {
		t.Fatalf("streamItems should be empty on failure, got %d", len(streamItems))
	}
	if failed != "boom" {
		t.Fatalf("failed = %q, want %q", failed, "boom")
	}
}

func TestCollectCompletedImageEvent_OutputItemFallback(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"image_generation_call","result":"BBBB","output_format":"png"}}`,
		"",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","result":"AAAA","output_format":"webp"}}`,
		"",
		`data: {"type":"response.completed","response":{"output":[],"tool_usage":{"image_gen":{"total_images":2}}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n"))

	completed, streamItems, failed, err := collectCompletedImageEvent(stream)
	if err != nil {
		t.Fatalf("collectCompletedImageEvent failed: %v", err)
	}
	if failed != "" {
		t.Fatalf("failed message should be empty, got %q", failed)
	}
	if got := gjson.GetBytes(completed, "type").String(); got != "response.completed" {
		t.Fatalf("unexpected completed type: %q", got)
	}
	if len(streamItems) != 2 {
		t.Fatalf("streamItems len = %d, want 2", len(streamItems))
	}
	if streamItems[0].Result != "AAAA" || streamItems[1].Result != "BBBB" {
		t.Fatalf("fallback order mismatch: %+v", streamItems)
	}
}

func TestBuildImagesAPIResponse_URLAndUsage(t *testing.T) {
	results := []imageCallResult{
		{
			Result:        "AAAA",
			RevisedPrompt: "cat",
			OutputFormat:  "webp",
			Size:          "1024x1024",
			Background:    "transparent",
			Quality:       "high",
		},
	}
	payload, err := buildImagesAPIResponse(results, 1710000000, []byte(`{"total_images":1}`), results[0], "url")
	if err != nil {
		t.Fatalf("buildImagesAPIResponse failed: %v", err)
	}

	if got := gjson.GetBytes(payload, "created").Int(); got != 1710000000 {
		t.Fatalf("created = %d, want 1710000000", got)
	}
	if got := gjson.GetBytes(payload, "data.0.url").String(); got != "data:image/webp;base64,AAAA" {
		t.Fatalf("url = %q, want data:image/webp;base64,AAAA", got)
	}
	if got := gjson.GetBytes(payload, "data.0.revised_prompt").String(); got != "cat" {
		t.Fatalf("revised_prompt = %q, want %q", got, "cat")
	}
	if got := gjson.GetBytes(payload, "background").String(); got != "transparent" {
		t.Fatalf("background = %q, want %q", got, "transparent")
	}
	if got := gjson.GetBytes(payload, "usage.total_images").Int(); got != 1 {
		t.Fatalf("usage.total_images = %d, want 1", got)
	}
}

func TestBuildImagesAPIResponse_InvalidUsageIgnored(t *testing.T) {
	results := []imageCallResult{{Result: "AAAA"}}
	payload, err := buildImagesAPIResponse(results, 1710000000, []byte(`{"total_images":`), imageCallResult{}, "b64_json")
	if err != nil {
		t.Fatalf("buildImagesAPIResponse failed: %v", err)
	}

	if got := gjson.GetBytes(payload, "data.0.b64_json").String(); got != "AAAA" {
		t.Fatalf("b64_json = %q, want %q", got, "AAAA")
	}
	if gjson.GetBytes(payload, "usage").Exists() {
		t.Fatalf("usage should be omitted for invalid usage raw: %s", string(payload))
	}
}

func TestExtractImagesFromResponsesCompleted_ErrorPaths(t *testing.T) {
	if _, _, _, _, err := extractImagesFromResponsesCompleted([]byte(`{"type":"response.failed"}`)); err == nil {
		t.Fatal("unexpected event type should return error")
	}

	payloadNoImage := []byte(`{
		"type":"response.completed",
		"response":{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}
	}`)
	results, _, usageRaw, _, err := extractImagesFromResponsesCompleted(payloadNoImage)
	if err != nil {
		t.Fatalf("missing image_generation_call should not return error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results len = %d, want 0", len(results))
	}
	if len(usageRaw) != 0 {
		t.Fatalf("usageRaw should be empty when usage missing: %s", string(usageRaw))
	}
}

func TestMimeTypeFromOutputFormat(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "image/png"},
		{in: "jpeg", want: "image/jpeg"},
		{in: "image/webp", want: "image/webp"},
		{in: "unknown", want: "image/png"},
	}
	for _, tc := range tests {
		if got := mimeTypeFromOutputFormat(tc.in); got != tc.want {
			t.Fatalf("mimeTypeFromOutputFormat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
