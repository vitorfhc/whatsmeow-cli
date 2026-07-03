package wa

import (
	"bytes"
	"fmt"

	"github.com/mdp/qrterminal/v3"
)

// RenderQR renders a QR payload as a scannable half-block Unicode string. The
// output uses only block glyphs and whitespace (no ANSI color), so it can be
// carried in a JSON field and printed to a terminal or chat for scanning.
func RenderQR(code string) (string, error) {
	if code == "" {
		return "", fmt.Errorf("render qr: empty code")
	}
	var buf bytes.Buffer
	qrterminal.GenerateHalfBlock(code, qrterminal.L, &buf)
	return buf.String(), nil
}
