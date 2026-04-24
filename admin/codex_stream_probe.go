package admin

import (
	"io"
	"net/http"
	"strings"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var adminImageGenerationToolJSON = []byte(`{"type":"image_generation","output_format":"png"}`)
var adminImageGenerationToolArrayJSON = []byte(`[{"type":"image_generation","output_format":"png"}]`)

type codexTextStreamResult struct {
	Completed   bool
	HasContent  bool
	TerminalErr string
	FinalText   string
}

func ensureCLIProxyStyleImageGenerationTool(payload []byte, model string) []byte {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "spark") {
		return payload
	}

	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		payload, _ = sjson.SetRawBytes(payload, "tools", adminImageGenerationToolArrayJSON)
		return payload
	}
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == "image_generation" {
			return payload
		}
	}
	payload, _ = sjson.SetRawBytes(payload, "tools.-1", adminImageGenerationToolJSON)
	return payload
}

func readCodexTextStream(body io.Reader, onDelta func(string)) (codexTextStreamResult, error) {
	result := codexTextStreamResult{}
	collector := newCodexTextCollector()

	err := proxy.ReadSSEStream(body, func(data []byte) bool {
		switch gjson.GetBytes(data, "type").String() {
		case "response.output_text.delta":
			delta := collector.ConsumeEvent(data)
			if delta != "" && onDelta != nil {
				onDelta(delta)
			}
		case "response.output_text.done", "response.output_item.done":
			collector.ConsumeEvent(data)
		case "response.completed":
			result.Completed = true
			result.FinalText = collector.Complete(data)
			result.HasContent = collector.HasContent()
			return false
		case "response.failed":
			result.TerminalErr = codexResponseFailedMessage(data)
			return false
		}
		return true
	})
	if !result.HasContent {
		result.HasContent = collector.HasContent()
	}
	return result, err
}

func parseSuccessfulGPT55ProbeResponse(resp *http.Response) auth.GPT55ProbeResult {
	if resp == nil || resp.Body == nil {
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: "响应为空"}
	}

	streamResult, err := readCodexTextStream(resp.Body, nil)
	if err != nil {
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: err.Error()}
	}
	if streamResult.TerminalErr != "" {
		if isGPT55UnsupportedResponse(resp.StatusCode, "", streamResult.TerminalErr) {
			return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeUnsupported, LastError: streamResult.TerminalErr}
		}
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: streamResult.TerminalErr}
	}
	if !streamResult.Completed {
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: "测试未完成（未收到 response.completed）"}
	}
	if !streamResult.HasContent {
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: "未收到模型输出"}
	}
	return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeSupported}
}
