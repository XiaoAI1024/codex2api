package proxy

import "testing"

func TestModelCatalog_BasicLookups(t *testing.T) {
	if !IsModelAllowed("gpt-5.5") {
		t.Fatal("gpt-5.5 should be allowed")
	}
	if !IsModelAllowed("gpt-5.4") {
		t.Fatal("gpt-5.4 should be allowed")
	}
	if IsModelAllowed("gpt-5") {
		t.Fatal("gpt-5 should not be allowed")
	}
	if !SupportsImageRequests("gpt-image-2") {
		t.Fatal("gpt-image-2 should support image requests")
	}
	if !IsImageOnlyModel("gpt-image-2") {
		t.Fatal("gpt-image-2 should be image-only")
	}
}

func TestModelCatalog_ListPublicModels(t *testing.T) {
	models := ListPublicModels()
	if len(models) == 0 {
		t.Fatal("public model list should not be empty")
	}

	hasImage := false
	has55 := false
	for _, model := range models {
		if model == "gpt-image-2" {
			hasImage = true
		}
		if model == "gpt-5.5" {
			has55 = true
		}
		if model == "gpt-5" {
			t.Fatal("gpt-5 must not appear in public models")
		}
	}
	if !hasImage {
		t.Fatal("expected gpt-image-2 in public models")
	}
	if !has55 {
		t.Fatal("expected gpt-5.5 in public models")
	}
}

func TestClampReasoningEffortForModel(t *testing.T) {
	effort, keep := ClampReasoningEffortForModel("gpt-5.4-mini", "xhigh")
	if !keep {
		t.Fatal("gpt-5.4-mini should keep reasoning field")
	}
	if effort != "high" {
		t.Fatalf("expected high, got %q", effort)
	}

	effort, keep = ClampReasoningEffortForModel("gpt-5.1-codex-mini", "high")
	if !keep {
		t.Fatal("gpt-5.1-codex-mini should keep reasoning field")
	}
	if effort != "medium" {
		t.Fatalf("expected medium, got %q", effort)
	}

	effort, keep = ClampReasoningEffortForModel("gpt-image-2", "high")
	if keep {
		t.Fatal("gpt-image-2 should drop reasoning field")
	}
	if effort != "" {
		t.Fatalf("expected empty effort, got %q", effort)
	}
}

func TestClampReasoningEffortForModel_UnknownAndNormalization(t *testing.T) {
	effort, keep := ClampReasoningEffortForModel("unknown-model", "high")
	if keep {
		t.Fatal("unknown model should drop reasoning field")
	}
	if effort != "" {
		t.Fatalf("unknown model effort should be empty, got %q", effort)
	}

	effort, keep = ClampReasoningEffortForModel(" GPT-5.4-MINI ", " HIGH ")
	if !keep {
		t.Fatal("known model should keep reasoning field")
	}
	if effort != "high" {
		t.Fatalf("expected normalized effort high, got %q", effort)
	}

	effort, keep = ClampReasoningEffortForModel("gpt-5.4-mini", "")
	if !keep {
		t.Fatal("empty effort on known model should still keep reasoning field")
	}
	if effort != "" {
		t.Fatalf("empty effort should remain empty, got %q", effort)
	}
}
