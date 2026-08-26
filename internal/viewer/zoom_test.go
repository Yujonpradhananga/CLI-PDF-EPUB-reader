package viewer

import "testing"

// The zoom keys act on the view on screen only: switching views must bring back
// the scale that view was left at, not carry the current one over.
func TestZoomIsPerView(t *testing.T) {
	d := &DocumentViewer{}

	if got := d.zoom(); got != 1.0 {
		t.Fatalf("fresh viewer zoom = %v, want 1.0", got)
	}

	d.setZoom(1.4)
	d.dualPageMode = "half"
	if got := d.zoom(); got != 1.0 {
		t.Errorf("half-page zoom after zooming the single view = %v, want 1.0", got)
	}

	d.setZoom(0.7)
	d.dualPageMode = "horizontal"
	d.setZoom(1.9)

	for _, tc := range []struct {
		mode string
		want float64
	}{
		{"", 1.4},
		{"half", 0.7},
		{"horizontal", 1.9},
		{"vertical", 1.0},
	} {
		d.dualPageMode = tc.mode
		if got := d.zoom(); got != tc.want {
			t.Errorf("zoom in mode %q = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestSetZoomClamps(t *testing.T) {
	d := &DocumentViewer{}

	d.setZoom(5.0)
	if got := d.zoom(); got != maxZoom {
		t.Errorf("zoom above the limit = %v, want %v", got, maxZoom)
	}

	d.setZoom(0.01)
	if got := d.zoom(); got != minZoom {
		t.Errorf("zoom below the limit = %v, want %v", got, minZoom)
	}
}

// A config saved before zoom was per-view carries one scale; every view starts
// there rather than silently resetting the document to 100%.
func TestFillViewConfigSpreadsLegacyScale(t *testing.T) {
	cfg := fillViewConfig(ViewConfig{FitMode: "height", ScaleFactor: 1.3})

	for _, v := range viewKeys {
		if got := cfg.ScaleFactors[v]; got != 1.3 {
			t.Errorf("view %q scale = %v, want 1.3", v, got)
		}
	}

	d := &DocumentViewer{zoomByView: cfg.ScaleFactors}
	d.dualPageMode = "vertical"
	if got := d.zoom(); got != 1.3 {
		t.Errorf("vertical zoom from a migrated config = %v, want 1.3", got)
	}
}

// Per-view scales already on disk win over the legacy field.
func TestFillViewConfigKeepsPerViewScales(t *testing.T) {
	cfg := fillViewConfig(ViewConfig{
		ScaleFactor:  1.3,
		ScaleFactors: map[string]float64{"half": 0.6},
	})

	if got := cfg.ScaleFactors["half"]; got != 0.6 {
		t.Errorf("half-page scale = %v, want 0.6", got)
	}
	if _, ok := cfg.ScaleFactors[viewSingle]; ok {
		t.Errorf("single view seeded from the legacy scale, want left unset")
	}

	d := &DocumentViewer{zoomByView: cfg.ScaleFactors}
	if got := d.zoom(); got != 1.0 {
		t.Errorf("single-view zoom = %v, want 1.0", got)
	}
}
