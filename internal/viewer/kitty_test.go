package viewer

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestClearKittyGraphicsDeletesOwnedPlacements(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()

	saved := os.Stdout
	os.Stdout = w
	d := &DocumentViewer{lastKittyImageID: 41, flashRuleID: 42}
	d.clearKittyGraphics()
	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	for _, id := range []string{"i=41", "i=42"} {
		if !strings.Contains(string(out), id) {
			t.Errorf("cleanup output %q does not delete %s", out, id)
		}
	}
	if d.lastKittyImageID != 0 || d.flashRuleID != 0 {
		t.Errorf("ids after cleanup: page=%d rule=%d, want both zero",
			d.lastKittyImageID, d.flashRuleID)
	}
}
