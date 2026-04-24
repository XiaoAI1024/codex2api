package admin

import (
	"strings"

	"github.com/tidwall/gjson"
)

type codexTextCollector struct {
	hasContent    bool
	fallbackTexts []string
}

func newCodexTextCollector() *codexTextCollector {
	return &codexTextCollector{}
}

func (c *codexTextCollector) ConsumeEvent(data []byte) string {
	switch gjson.GetBytes(data, "type").String() {
	case "response.output_text.delta":
		delta := gjson.GetBytes(data, "delta").String()
		if strings.TrimSpace(delta) == "" {
			return ""
		}
		c.hasContent = true
		return delta
	case "response.output_text.done":
		c.appendFallback(gjson.GetBytes(data, "text").String())
	case "response.output_item.done":
		c.appendFallback(extractCodexOutputItemDoneText(data))
	}
	return ""
}

func (c *codexTextCollector) Complete(data []byte) string {
	if c.hasContent {
		return ""
	}

	if text := extractCodexCompletedOutputText(data); strings.TrimSpace(text) != "" {
		c.hasContent = true
		return text
	}

	if len(c.fallbackTexts) == 0 {
		return ""
	}

	text := strings.Join(c.fallbackTexts, "")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	c.hasContent = true
	return text
}

func (c *codexTextCollector) HasContent() bool {
	return c != nil && c.hasContent
}

func (c *codexTextCollector) appendFallback(text string) {
	if c == nil || strings.TrimSpace(text) == "" {
		return
	}
	for _, existing := range c.fallbackTexts {
		if existing == text {
			return
		}
	}
	c.fallbackTexts = append(c.fallbackTexts, text)
}

func extractCodexOutputItemDoneText(data []byte) string {
	return extractCodexMessageOutputText(gjson.GetBytes(data, "item"))
}

func extractCodexCompletedOutputText(data []byte) string {
	output := gjson.GetBytes(data, "response.output")
	if !output.Exists() || !output.IsArray() {
		return ""
	}

	var parts []string
	for _, item := range output.Array() {
		text := extractCodexMessageOutputText(item)
		if strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "")
}

func extractCodexMessageOutputText(item gjson.Result) string {
	if !item.Exists() || item.Type != gjson.JSON || item.Get("type").String() != "message" {
		return ""
	}

	content := item.Get("content")
	if !content.Exists() || !content.IsArray() {
		return ""
	}

	var parts []string
	for _, part := range content.Array() {
		if part.Get("type").String() != "output_text" {
			continue
		}
		text := part.Get("text").String()
		if strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "")
}

func codexResponseFailedMessage(data []byte) string {
	errMsg := strings.TrimSpace(gjson.GetBytes(data, "response.status_details.error.message").String())
	if errMsg != "" {
		return errMsg
	}
	errMsg = strings.TrimSpace(gjson.GetBytes(data, "response.error.message").String())
	if errMsg != "" {
		return errMsg
	}
	errMsg = strings.TrimSpace(gjson.GetBytes(data, "error.message").String())
	if errMsg != "" {
		return errMsg
	}
	return "上游返回 response.failed"
}
