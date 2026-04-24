package admin

import "testing"

func TestCodexTextCollectorFallsBackToOutputItemDoneMessage(t *testing.T) {
	collector := newCodexTextCollector()

	if got := collector.ConsumeEvent([]byte(`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`)); got != "" {
		t.Fatalf("ConsumeEvent() = %q, want empty", got)
	}

	got := collector.Complete([]byte(`{"type":"response.completed","response":{"output":[]}}`))
	if got != "ok" {
		t.Fatalf("Complete() = %q, want %q", got, "ok")
	}
	if !collector.HasContent() {
		t.Fatal("expected collector to report content after output_item.done fallback")
	}
}

func TestCodexTextCollectorFallsBackToCompletedOutput(t *testing.T) {
	collector := newCodexTextCollector()

	got := collector.Complete([]byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`))
	if got != "done" {
		t.Fatalf("Complete() = %q, want %q", got, "done")
	}
	if !collector.HasContent() {
		t.Fatal("expected collector to report content after response.completed fallback")
	}
}

func TestCodexTextCollectorFallsBackToOutputTextDone(t *testing.T) {
	collector := newCodexTextCollector()

	if got := collector.ConsumeEvent([]byte(`{"type":"response.output_text.done","text":"done"}`)); got != "" {
		t.Fatalf("ConsumeEvent() = %q, want empty", got)
	}

	got := collector.Complete([]byte(`{"type":"response.completed","response":{"output":[]}}`))
	if got != "done" {
		t.Fatalf("Complete() = %q, want %q", got, "done")
	}
}

func TestCodexTextCollectorDoesNotDuplicateAfterDelta(t *testing.T) {
	collector := newCodexTextCollector()

	if got := collector.ConsumeEvent([]byte(`{"type":"response.output_text.delta","delta":"hello"}`)); got != "hello" {
		t.Fatalf("ConsumeEvent() = %q, want %q", got, "hello")
	}
	if got := collector.ConsumeEvent([]byte(`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`)); got != "" {
		t.Fatalf("ConsumeEvent() = %q, want empty", got)
	}

	got := collector.Complete([]byte(`{"type":"response.completed","response":{"output":[]}}`))
	if got != "" {
		t.Fatalf("Complete() = %q, want empty", got)
	}
	if !collector.HasContent() {
		t.Fatal("expected collector to report content after delta")
	}
}

func TestCodexResponseFailedMessageUsesNestedResponseError(t *testing.T) {
	got := codexResponseFailedMessage([]byte(`{"type":"response.failed","response":{"error":{"code":"model_not_found","message":"The model ` + "`gpt-5.5`" + ` does not exist or you do not have access to it."}}}`))
	want := "The model `gpt-5.5` does not exist or you do not have access to it."
	if got != want {
		t.Fatalf("codexResponseFailedMessage() = %q, want %q", got, want)
	}
}

func TestCodexResponseFailedMessagePrefersStatusDetails(t *testing.T) {
	got := codexResponseFailedMessage([]byte(`{"type":"response.failed","response":{"status_details":{"error":{"message":"status details"}},"error":{"message":"nested response"}},"error":{"message":"root"}}`))
	if got != "status details" {
		t.Fatalf("codexResponseFailedMessage() = %q, want %q", got, "status details")
	}
}

func TestCodexResponseFailedMessageFallsBackToRootErrorAndDefault(t *testing.T) {
	got := codexResponseFailedMessage([]byte(`{"type":"response.failed","response":{"error":{"message":" "}},"error":{"message":"root"}}`))
	if got != "root" {
		t.Fatalf("codexResponseFailedMessage() = %q, want %q", got, "root")
	}

	got = codexResponseFailedMessage([]byte(`{"type":"response.failed","response":{"error":{"message":" "}},"error":{"message":" "}}`))
	if got != "上游返回 response.failed" {
		t.Fatalf("codexResponseFailedMessage() = %q, want default", got)
	}
}
