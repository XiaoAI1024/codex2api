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
