package api

import (
	"os"
	"strings"
	"testing"
)

func TestAPIReadmeGenerationStreamExampleDoesNotMixEditEvents(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(raw)

	start := strings.Index(readme, "Streaming image example:")
	if start < 0 {
		t.Fatal("missing Streaming image example section")
	}
	end := strings.Index(readme[start:], "### Responses WebSocket")
	if end < 0 {
		t.Fatal("missing Responses WebSocket section after image streaming example")
	}
	generationExample := readme[start : start+end]

	if strings.Contains(generationExample, "image_edit.completed") {
		t.Fatalf("generation streaming example should not include image edit events:\n%s", generationExample)
	}
	if !strings.Contains(generationExample, "image_generation.completed") {
		t.Fatalf("generation streaming example should include image_generation.completed:\n%s", generationExample)
	}
}

func TestAPIDocsDescribeAuthForAliasesAndHealth(t *testing.T) {
	raw, err := os.ReadFile("../docs/API.md")
	if err != nil {
		t.Fatalf("read docs/API.md: %v", err)
	}
	docs := string(raw)

	for _, want := range []string{
		"配置了 API Key 时",
		"`/v1/*`",
		"根路径兼容端点",
		"`/backend-api/codex/*`",
		"未配置任何 API Key",
		"`GET /health`",
		"不需要认证",
	} {
		if !strings.Contains(docs, want) {
			t.Fatalf("docs/API.md should describe auth detail %q", want)
		}
	}
}
