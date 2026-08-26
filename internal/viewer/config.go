package viewer

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type ViewConfig struct {
	FitMode       string             `json:"fit_mode"`
	DarkMode      string             `json:"dark_mode"`
	DualPageMode  string             `json:"dual_page_mode"`
	ForceMode     string             `json:"force_mode"`
	ScaleFactor   float64            `json:"scale_factor"`            // zoom of the single-page view; the only zoom a config written before it was per-view carries
	ScaleFactors  map[string]float64 `json:"scale_factors,omitempty"` // zoom of each view, keyed by viewKeys
	HTMLPageWidth int                `json:"html_page_width,omitempty"`
	CropTop       float64            `json:"crop_top,omitempty"`
	CropBottom    float64            `json:"crop_bottom,omitempty"`
	CropLeft      float64            `json:"crop_left,omitempty"`
	CropRight     float64            `json:"crop_right,omitempty"`
}

// The view names zoom is stored under: the single-page view plus the three
// dualPageMode values (see viewKey).
const (
	viewSingle = "single"
)

var viewKeys = []string{viewSingle, "vertical", "horizontal", "half"}

// ConfigStore maps absolute file paths to their saved view config.
type ConfigStore map[string]ViewConfig

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "docviewer", "config.json")
}

func loadConfigStore() ConfigStore {
	store := make(ConfigStore)
	data, err := os.ReadFile(configPath())
	if err != nil {
		return store
	}
	json.Unmarshal(data, &store)
	return store
}

func saveConfigStore(store ConfigStore) {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0644)
}

func defaultViewConfig() ViewConfig {
	return ViewConfig{
		FitMode:       "height",
		ScaleFactor:   1.0,
		HTMLPageWidth: 1000,
	}
}

// loadDocConfig returns the saved config for the given file, or defaults.
func loadDocConfig(absPath string) ViewConfig {
	store := loadConfigStore()
	if cfg, ok := store[absPath]; ok {
		return fillViewConfig(cfg)
	}
	return defaultViewConfig()
}

// fillViewConfig repairs a config read from disk: absent values take their
// default, and a config written before zoom was per-view has its one scale
// spread over every view, so a document reopens where it was left.
func fillViewConfig(cfg ViewConfig) ViewConfig {
	if cfg.ScaleFactor <= 0 {
		cfg.ScaleFactor = 1.0
	}
	if cfg.HTMLPageWidth <= 0 {
		cfg.HTMLPageWidth = 1000
	}
	if len(cfg.ScaleFactors) == 0 {
		cfg.ScaleFactors = make(map[string]float64, len(viewKeys))
		for _, v := range viewKeys {
			cfg.ScaleFactors[v] = cfg.ScaleFactor
		}
	}
	return cfg
}

// saveConfig persists the viewer's current view options for this document.
func (d *DocumentViewer) saveConfig() {
	absPath, err := filepath.Abs(d.path)
	if err != nil {
		return
	}
	store := loadConfigStore()
	store[absPath] = ViewConfig{
		FitMode:       d.fitMode,
		DarkMode:      d.darkMode,
		DualPageMode:  d.dualPageMode,
		ForceMode:     d.forceMode,
		ScaleFactor:   d.zoomOf(viewSingle),
		ScaleFactors:  d.zoomByView,
		HTMLPageWidth: d.htmlPageWidth,
		CropTop:       d.cropTop,
		CropBottom:    d.cropBottom,
		CropLeft:      d.cropLeft,
		CropRight:     d.cropRight,
	}
	saveConfigStore(store)
}
