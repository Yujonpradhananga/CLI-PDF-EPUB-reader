package main

import (
	"os"
	"path/filepath"

	"pdf-cli/cmd"
)

func main() {
	// The same binary acts as the scripting client when installed as dvctl or
	// invoked as "pdf-cli ctl".
	if filepath.Base(os.Args[0]) == "dvctl" {
		os.Exit(runCtl(os.Args[1:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(runCtl(os.Args[2:]))
	}
	cmd.Execute()
}
