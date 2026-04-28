package wsrelay

import (
	"net/http"
	"testing"

	"github.com/codex2api/proxy"
	"github.com/tidwall/gjson"
)

func TestPrepareWebsocketHeadersUsesConfiguredDefaultsAndBetaFeatures(t *testing.T) {
	exec := NewExecutor()
	cfg := &proxy.DeviceProfileConfig{
		UserAgent:              "codex_cli_rs/0.120.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464",
		PackageVersion:         "0.120.0",
		RuntimeVersion:         "0.120.0",
		OS:                     "MacOS",
		Arch:                   "arm64",
		StabilizeDeviceProfile: true,
		BetaFeatures:           "multi_agent",
	}
	ginHeaders := http.Header{
		"Originator": []string{"custom-originator"},
		"Version":    []string{"9.9.9"},
	}

	headers := exec.prepareWebsocketHeaders("token-123", "42", "session-123", "api-key-1", cfg, ginHeaders)

	if got := headers.Get("Authorization"); got != "Bearer token-123" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("OpenAI-Beta"); got != responsesWebsocketBetaHeader {
		t.Fatalf("OpenAI-Beta = %q", got)
	}
	if got := headers.Get("X-Codex-Beta-Features"); got != "multi_agent" {
		t.Fatalf("X-Codex-Beta-Features = %q", got)
	}
	if got := headers.Get("User-Agent"); got != cfg.UserAgent {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := headers.Get("X-Stainless-Package-Version"); got != cfg.PackageVersion {
		t.Fatalf("X-Stainless-Package-Version = %q", got)
	}
	if got := headers.Get("X-Stainless-Runtime-Version"); got != cfg.RuntimeVersion {
		t.Fatalf("X-Stainless-Runtime-Version = %q", got)
	}
	if got := headers.Get("X-Stainless-Os"); got != cfg.OS {
		t.Fatalf("X-Stainless-Os = %q", got)
	}
	if got := headers.Get("X-Stainless-Arch"); got != cfg.Arch {
		t.Fatalf("X-Stainless-Arch = %q", got)
	}
	if got := headers.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := headers.Get("Originator"); got != "custom-originator" {
		t.Fatalf("Originator = %q", got)
	}
	if got := headers.Get("Chatgpt-Account-Id"); got != "42" {
		t.Fatalf("Chatgpt-Account-Id = %q", got)
	}
	if got := headers.Get("Conversation_id"); got != "session-123" {
		t.Fatalf("Conversation_id = %q", got)
	}
}

func TestPrepareWebsocketHeadersUsesAccountProfileByDefault(t *testing.T) {
	exec := NewExecutor()
	profile := proxy.ProfileForAccount(42)

	ginHeaders := http.Header{
		"Version": []string{"9.9.9"},
	}

	headers := exec.prepareWebsocketHeaders("token-123", "42", "session-123", "api-key-1", nil, ginHeaders)

	if got := headers.Get("User-Agent"); got != profile.UserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, profile.UserAgent)
	}
	if got := headers.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
}

func TestPrepareWebsocketBodyImageGenerationExplicitOnly(t *testing.T) {
	exec := NewExecutor()

	plain := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hello"}]}`)
	plainBody := exec.prepareWebsocketBody(plain, "session-123")
	if gjson.GetBytes(plainBody, `tools.#(type="image_generation")`).Exists() {
		t.Fatalf("plain websocket body should not add image_generation tool: %s", plainBody)
	}
	if got := gjson.GetBytes(plainBody, "type").String(); got != "response.create" {
		t.Fatalf("type = %q, want response.create; body=%s", got, plainBody)
	}
	if got := gjson.GetBytes(plainBody, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true; body=%s", got, plainBody)
	}

	explicit := []byte(`{
		"model":"gpt-5.4",
		"input":[{"role":"user","content":"draw a cat"}],
		"tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}],
		"tool_choice":{"type":"image_generation"}
	}`)
	explicitBody := exec.prepareWebsocketBody(explicit, "session-123")
	if got := gjson.GetBytes(explicitBody, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, explicitBody)
	}
	if got := gjson.GetBytes(explicitBody, "tools.0.model").String(); got != "gpt-image-2" {
		t.Fatalf("tools.0.model = %q, want gpt-image-2; body=%s", got, explicitBody)
	}
	if got := gjson.GetBytes(explicitBody, "tool_choice.type").String(); got != "image_generation" {
		t.Fatalf("tool_choice.type = %q, want image_generation; body=%s", got, explicitBody)
	}
}

func TestPrepareWebsocketBodyAfterResponsesPreparationImageGenerationExplicitOnly(t *testing.T) {
	exec := NewExecutor()

	preparedPlain, _ := proxy.PrepareResponsesBody([]byte(`{"model":"gpt-5.4","input":"hello"}`))
	plainBody := exec.prepareWebsocketBody(preparedPlain, "session-123")
	plainMessage := NewHTTPRequestMessage("req-plain", "session-123", plainBody)
	if gjson.GetBytes(plainMessage.Content, `tools.#(type="image_generation")`).Exists() {
		t.Fatalf("prepared plain websocket message should not add image_generation tool: %s", plainMessage.Content)
	}
	if got := gjson.GetBytes(plainMessage.Content, "type").String(); got != "response.create" {
		t.Fatalf("type = %q, want response.create; body=%s", got, plainMessage.Content)
	}

	preparedExplicit, _ := proxy.PrepareResponsesBody([]byte(`{
		"model":"gpt-5.4",
		"input":"draw a cat",
		"tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}],
		"tool_choice":{"type":"image_generation"}
	}`))
	explicitBody := exec.prepareWebsocketBody(preparedExplicit, "session-123")
	explicitMessage := NewHTTPRequestMessage("req-explicit", "session-123", explicitBody)
	if got := gjson.GetBytes(explicitMessage.Content, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, explicitMessage.Content)
	}
	if got := gjson.GetBytes(explicitMessage.Content, "tool_choice.type").String(); got != "image_generation" {
		t.Fatalf("tool_choice.type = %q, want image_generation; body=%s", got, explicitMessage.Content)
	}
	if instructions := gjson.GetBytes(explicitMessage.Content, "instructions").String(); instructions == "" {
		t.Fatalf("explicit image websocket message should include bridge instructions: %s", explicitMessage.Content)
	}
}
