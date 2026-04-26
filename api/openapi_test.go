package api

import (
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func loadOpenAPISpec(t *testing.T) map[string]any {
	t.Helper()

	raw, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	return spec
}

func getMap(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()

	current := value
	for _, key := range path {
		next, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s is %T, want map", strings.Join(path, "."), current)
		}
		current, ok = next[key]
		if !ok {
			t.Fatalf("missing %s", strings.Join(path, "."))
		}
	}
	got, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want map", strings.Join(path, "."), current)
	}
	return got
}

func getSlice(t *testing.T, value any, path ...string) []any {
	t.Helper()

	current := value
	for _, key := range path {
		next, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s is %T, want map", strings.Join(path, "."), current)
		}
		current, ok = next[key]
		if !ok {
			t.Fatalf("missing %s", strings.Join(path, "."))
		}
	}
	got, ok := current.([]any)
	if !ok {
		t.Fatalf("%s is %T, want slice", strings.Join(path, "."), current)
	}
	return got
}

func hasString(values []any, want string) bool {
	for _, value := range values {
		if got, ok := value.(string); ok && got == want {
			return true
		}
	}
	return false
}

func TestOpenAPIAuthenticationAndCorePaths(t *testing.T) {
	spec := loadOpenAPISpec(t)

	auth := getMap(t, spec, "components", "securitySchemes", "BearerAuth")
	if auth["type"] != "http" || auth["scheme"] != "bearer" {
		t.Fatalf("BearerAuth = %#v, want HTTP bearer scheme", auth)
	}
	if desc, _ := auth["description"].(string); !strings.Contains(desc, "Bearer") || !strings.Contains(desc, "not configured") {
		t.Fatalf("BearerAuth description should mention Bearer and unconfigured-key skip behavior, got %q", desc)
	}

	paths := getMap(t, spec, "paths")
	for _, path := range []string{
		"/chat/completions",
		"/responses",
		"/backend-api/codex/responses",
		"/responses/compact",
		"/backend-api/codex/responses/compact",
		"/images/generations",
		"/images/edits",
		"/health",
	} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("missing path %s", path)
		}
	}

	healthGet := getMap(t, spec, "paths", "/health", "get")
	security, ok := healthGet["security"].([]any)
	if !ok || len(security) != 0 {
		t.Fatalf("GET /health security = %#v, want explicit empty security", healthGet["security"])
	}

	responsePostParameters := getSlice(t, spec, "paths", "/responses", "post", "parameters")
	if len(responsePostParameters) == 0 {
		t.Fatal("POST /responses should document the optional Version header parameter")
	}
}

func TestOpenAPISchemasCoverCurrentCompatibilitySurface(t *testing.T) {
	spec := loadOpenAPISpec(t)

	roleEnum := getSlice(t, spec, "components", "schemas", "ChatCompletionRequest", "properties", "messages", "items", "properties", "role", "enum")
	if !hasString(roleEnum, "developer") {
		t.Fatalf("ChatCompletionRequest role enum = %#v, want developer role", roleEnum)
	}

	imageEventTypes := getSlice(t, spec, "components", "schemas", "ImageStreamEvent", "properties", "type", "enum")
	for _, want := range []string{
		"image_generation.partial_image",
		"image_generation.completed",
		"image_edit.partial_image",
		"image_edit.completed",
		"error",
	} {
		if !hasString(imageEventTypes, want) {
			t.Fatalf("ImageStreamEvent type enum missing %q: %#v", want, imageEventTypes)
		}
	}

	streamSchema := getMap(t, spec, "paths", "/images/generations", "post", "responses", "200", "content", "text/event-stream", "schema")
	if streamSchema["$ref"] != "#/components/schemas/ImageStreamEvent" {
		t.Fatalf("image generation SSE schema = %#v, want ImageStreamEvent ref", streamSchema)
	}
}
