package wa

import (
	"strings"
	"testing"
)

func TestRenderQR(t *testing.T) {
	t.Run("renders a scannable block", func(t *testing.T) {
		out, err := RenderQR("2@abc,def,ghi")
		if err != nil {
			t.Fatalf("RenderQR: %v", err)
		}
		if out == "" {
			t.Fatal("want non-empty output")
		}
		if !strings.Contains(out, "\n") {
			t.Fatal("want multi-line output")
		}
		// Output hygiene: no ANSI color escape sequences.
		if strings.Contains(out, "\033[") {
			t.Error("output contains ANSI escape sequences")
		}
	})

	t.Run("deterministic for identical input", func(t *testing.T) {
		a, err := RenderQR("same-payload")
		if err != nil {
			t.Fatalf("RenderQR: %v", err)
		}
		b, err := RenderQR("same-payload")
		if err != nil {
			t.Fatalf("RenderQR: %v", err)
		}
		if a != b {
			t.Error("RenderQR not deterministic for identical input")
		}
	})

	t.Run("rejects empty code", func(t *testing.T) {
		if _, err := RenderQR(""); err == nil {
			t.Error("want error for empty code")
		}
	})
}
