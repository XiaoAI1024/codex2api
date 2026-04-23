package proxy

import (
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
	raw := []byte(`{"quality":"high"}`)
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
