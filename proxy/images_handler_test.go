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
