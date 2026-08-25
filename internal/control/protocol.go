package control

import (
	"os"
	"path/filepath"
)

// Request is one command sent as newline-delimited JSON to a running viewer.
type Request struct {
	Cmd  string            `json:"cmd"`
	Args map[string]string `json:"args,omitempty"`
}

// State is the externally visible state of one viewer.
type State struct {
	PID         int     `json:"pid"`
	Path        string  `json:"path"`
	Name        string  `json:"name"`
	FileType    string  `json:"file_type"`
	Page        int     `json:"page"`
	Pages       int     `json:"pages"`
	Fit         string  `json:"fit"`
	Tint        string  `json:"tint"`
	View        string  `json:"view"`
	Dual        string  `json:"dual"`
	HalfOffset  string  `json:"half"`
	Zoom        float64 `json:"zoom"`
	HTMLWidth   int     `json:"html_width"`
	CropTop     float64 `json:"crop_top"`
	CropBottom  float64 `json:"crop_bottom"`
	CropLeft    float64 `json:"crop_left"`
	CropRight   float64 `json:"crop_right"`
	Search      string  `json:"search,omitempty"`
	SearchHits  int     `json:"search_hits"`
	SearchIndex int     `json:"search_index"`
	Reflowable  bool    `json:"reflowable"`
	IsImage     bool    `json:"is_image"`
	AgtermID    string  `json:"agterm_session,omitempty"`
	Socket      string  `json:"socket"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	State *State `json:"state,omitempty"`
	TOC   []TOC  `json:"toc,omitempty"`
}

type TOC struct {
	Level int    `json:"level"`
	Title string `json:"title"`
	Page  int    `json:"page"`
}

func SocketDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "docviewer-ctl")
	}
	return filepath.Join(home, ".cache", "docviewer", "ctl")
}
