package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

func TestBuildTestPayload_UsesCLIProxyStyleRequestShape(t *testing.T) {
	payload := buildTestPayload("gpt-5.5")

	if got := gjson.GetBytes(payload, "model").String(); got != "gpt-5.5" {
		t.Fatalf("model = %q, want gpt-5.5", got)
	}
	if !gjson.GetBytes(payload, "stream").Bool() {
		t.Fatalf("stream = false, want true")
	}
	if gjson.GetBytes(payload, "store").Bool() {
		t.Fatalf("store = true, want false")
	}
	if got := gjson.GetBytes(payload, "instructions").String(); got != "" {
		t.Fatalf("instructions = %q, want empty string", got)
	}
	if !gjson.GetBytes(payload, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls = false, want true")
	}
	if got := gjson.GetBytes(payload, "include.0").String(); got != "reasoning.encrypted_content" {
		t.Fatalf("include.0 = %q, want reasoning.encrypted_content", got)
	}
	if got := gjson.GetBytes(payload, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want message", got)
	}
	if got := gjson.GetBytes(payload, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want user", got)
	}
	if got := gjson.GetBytes(payload, "input.0.content.0.type").String(); got != "input_text" {
		t.Fatalf("input.0.content.0.type = %q, want input_text", got)
	}
	if got := gjson.GetBytes(payload, "input.0.content.0.text").String(); got != "Say hello in one sentence." {
		t.Fatalf("input.0.content.0.text = %q", got)
	}
	if got := gjson.GetBytes(payload, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation", got)
	}
	if got := gjson.GetBytes(payload, "tools.0.output_format").String(); got != "png" {
		t.Fatalf("tools.0.output_format = %q, want png", got)
	}
}

func TestReadCodexTextStream_UsesOutputItemFallback(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.created"}`,
		"",
		`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Hello!"}]}}`,
		"",
		`data: {"type":"response.completed","response":{"output":[]}}`,
		"",
	}, "\n")

	result, err := readCodexTextStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("readCodexTextStream returned error: %v", err)
	}
	if !result.Completed {
		t.Fatalf("Completed = false, want true")
	}
	if !result.HasContent {
		t.Fatalf("HasContent = false, want true")
	}
	if result.TerminalErr != "" {
		t.Fatalf("TerminalErr = %q, want empty", result.TerminalErr)
	}
	if result.FinalText != "Hello!" {
		t.Fatalf("FinalText = %q, want Hello!", result.FinalText)
	}
}

func TestParseSuccessfulGPT55ProbeResponse_ResponseFailedIsUnsupported(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.created"}`,
		"",
		`data: {"type":"response.failed","response":{"error":{"code":"model_not_found","message":"The model ` + "`gpt-5.5`" + ` does not exist or you do not have access to it."}}}`,
		"",
	}, "\n")

	result := parseSuccessfulGPT55ProbeResponse(&http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
	})
	if result.Outcome != auth.GPT55ProbeOutcomeUnsupported {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, auth.GPT55ProbeOutcomeUnsupported)
	}
	if !strings.Contains(result.LastError, "do not have access") {
		t.Fatalf("LastError = %q", result.LastError)
	}
}
