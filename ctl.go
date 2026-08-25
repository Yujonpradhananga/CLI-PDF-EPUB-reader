package main

// dvctl — the client half of the scripting interface. It is the same binary as
// the viewer: main() dispatches here when argv[0] is dvctl or the first
// argument is "ctl", so there is one thing to build and install and the two
// halves cannot drift apart.
//
// A command names its targets with --target (a pid, a path or file-name
// fragment, or an agterm session id) or --all; with neither, a single running
// viewer is the target and several are an error, so a script never surprises
// the user by acting on the wrong document.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"pdf-cli/internal/control"
)

type ctlRequest = control.Request
type ctlState = control.State
type ctlResponse = control.Response
type ctlTOC = control.TOC

func ctlSocketDir() string { return control.SocketDir() }

type ctlClient struct {
	socket string
	state  ctlState
}

// ctlDeadSocket marks a socket nothing is listening on, so ctlInstances can
// unlink it. Any other failure — including a viewer that answers "busy" — means
// a viewer is alive on the far end and its socket must be left alone.
type ctlDeadSocket struct{ err error }

func (e ctlDeadSocket) Error() string { return e.err.Error() }

// ctlDial sends one request to one viewer.
func ctlDial(socket string, req ctlRequest) (ctlResponse, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return ctlResponse{}, ctlDeadSocket{err}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	data, err := json.Marshal(req)
	if err != nil {
		return ctlResponse{}, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return ctlResponse{}, err
	}
	var resp ctlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return ctlResponse{}, err
	}
	if !resp.OK && resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// ctlInstances returns every viewer that answers, sorted by pid. Sockets left
// behind by a viewer that died are removed as they are found.
func ctlInstances() []ctlClient {
	entries, err := os.ReadDir(ctlSocketDir())
	if err != nil {
		return nil
	}
	var found []ctlClient
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		socket := filepath.Join(ctlSocketDir(), e.Name())
		resp, err := ctlDial(socket, ctlRequest{Cmd: "status"})
		if err != nil {
			var dead ctlDeadSocket
			if errors.As(err, &dead) {
				os.Remove(socket)
			}
			continue
		}
		if resp.State == nil {
			continue
		}
		found = append(found, ctlClient{socket: socket, state: *resp.State})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].state.PID < found[j].state.PID })
	return found
}

// ctlMatches keeps the viewers a target names. An all-digits target is a pid,
// a target equal to an agterm session id (case-insensitive) is that session's
// viewer, and anything else is a case-insensitive substring of the file name
// or of the full path.
func ctlMatches(all []ctlClient, target string) []ctlClient {
	if pid, err := strconv.Atoi(target); err == nil {
		for _, c := range all {
			if c.state.PID == pid {
				return []ctlClient{c}
			}
		}
		return nil
	}
	var out []ctlClient
	for _, c := range all {
		if c.state.AgtermID != "" && strings.EqualFold(c.state.AgtermID, target) {
			return []ctlClient{c}
		}
	}
	lower := strings.ToLower(target)
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.state.Name), lower) ||
			strings.Contains(strings.ToLower(c.state.Path), lower) {
			out = append(out, c)
		}
	}
	return out
}

type ctlOpts struct {
	target string
	all    bool
	asJSON bool
	quiet  bool
}

// ctlSplitFlags pulls the target and output flags out of args, leaving the
// command's own arguments behind. A bare "--" ends flag parsing, so a search
// query or a key payload can contain "--all" and friends as literal text.
func ctlSplitFlags(args []string) ([]string, ctlOpts, error) {
	var rest []string
	var o ctlOpts
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			rest = append(rest, args[i+1:]...)
			return rest, o, nil
		}
		switch args[i] {
		case "--target", "-t":
			if i+1 >= len(args) {
				return nil, o, fmt.Errorf("--target needs a value")
			}
			i++
			o.target = args[i]
		case "--all", "-a":
			o.all = true
		case "--json":
			o.asJSON = true
		case "--quiet", "-q":
			o.quiet = true
		default:
			if strings.HasPrefix(args[i], "--target=") {
				o.target = strings.TrimPrefix(args[i], "--target=")
				continue
			}
			rest = append(rest, args[i])
		}
	}
	return rest, o, nil
}

// ctlResolve picks the viewers a command acts on.
func ctlResolve(o ctlOpts) ([]ctlClient, error) {
	all := ctlInstances()
	if len(all) == 0 {
		return nil, fmt.Errorf("no pdf-cli viewer is running")
	}
	if o.all {
		return all, nil
	}
	if o.target != "" {
		m := ctlMatches(all, o.target)
		if len(m) == 0 {
			return nil, fmt.Errorf("no pdf-cli viewer matches %q", o.target)
		}
		return m, nil
	}
	if len(all) == 1 {
		return all, nil
	}
	var names []string
	for _, c := range all {
		names = append(names, fmt.Sprintf("%d %s", c.state.PID, c.state.Name))
	}
	return nil, fmt.Errorf("%d viewers are running; name one with --target or use --all:\n  %s",
		len(all), strings.Join(names, "\n  "))
}

// ctlSettingFlags turns --tint dim --crop-top +0.02 into the args map the
// server's "set" command reads. Flag names are the setting names with dashes.
func ctlSettingFlags(args []string) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected argument %q", a)
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = args[i]
		}
		out[strings.ReplaceAll(name, "-", "_")] = value
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nothing to set")
	}
	return out, nil
}

// ctlBuiltinPresets are the named setting bundles a hotkey or overlay applies.
// A preset of the same name in ~/.config/docviewer/presets.json replaces the
// built-in one, and new names are simply added.
var ctlBuiltinPresets = map[string]map[string]string{
	"laptop":  {"dual": "half", "fit": "height", "crop_top": "0.04", "crop_bottom": "0.04", "crop_left": "0.06", "crop_right": "0.06"},
	"display": {"dual": "off", "fit": "height", "crop": "0"},
	"white":   {"tint": "off"},
	"pale":    {"tint": "dim"},
	"dark":    {"tint": "dark"},
	"invert":  {"tint": "invert"},
	"trim":    {"crop": "+0.02"},
	"untrim":  {"crop": "-0.02"},
	"notrim":  {"crop": "0"},
}

func ctlPresetPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "docviewer", "presets.json")
}

func ctlPresets() map[string]map[string]string {
	out := map[string]map[string]string{}
	for name, settings := range ctlBuiltinPresets {
		out[name] = settings
	}
	data, err := os.ReadFile(ctlPresetPath())
	if err != nil {
		return out
	}
	var user map[string]map[string]string
	if json.Unmarshal(data, &user) != nil {
		return out
	}
	for name, settings := range user {
		out[name] = settings
	}
	return out
}

// runCtl is the dvctl entry point. It returns the process exit code.
func runCtl(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Print(ctlHelp)
		return 0
	}
	cmd, rest := args[0], args[1:]
	rest, opts, err := ctlSplitFlags(rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dvctl:", err)
		return 2
	}

	switch cmd {
	case "list":
		return ctlList(opts)
	case "presets":
		return ctlListPresets(opts)
	}

	req, err := ctlBuildRequest(cmd, rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dvctl:", err)
		return 2
	}

	targets, err := ctlResolve(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dvctl:", err)
		return 1
	}

	exit := 0
	var states []ctlState
	for _, c := range targets {
		resp, err := ctlDial(c.socket, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dvctl: %d (%s): %v\n", c.state.PID, c.state.Name, err)
			exit = 1
			continue
		}
		if resp.State != nil {
			states = append(states, *resp.State)
		}
		if cmd == "toc" && !opts.asJSON {
			for _, e := range resp.TOC {
				fmt.Printf("%s%s  (p.%d)\n", strings.Repeat("  ", max(e.Level-1, 0)), e.Title, e.Page)
			}
		}
		if opts.asJSON {
			data, _ := json.Marshal(resp)
			fmt.Println(string(data))
		}
	}
	if !opts.asJSON && !opts.quiet && cmd != "toc" {
		ctlPrintStates(states)
	}
	return exit
}

// ctlBuildRequest turns a dvctl subcommand into a socket request.
func ctlBuildRequest(cmd string, rest []string) (ctlRequest, error) {
	switch cmd {
	case "status":
		return ctlRequest{Cmd: "status"}, nil
	case "toc":
		return ctlRequest{Cmd: "toc"}, nil
	case "reload", "refresh", "quit", "back":
		return ctlRequest{Cmd: cmd}, nil
	case "set":
		settings, err := ctlSettingFlags(rest)
		if err != nil {
			return ctlRequest{}, err
		}
		return ctlRequest{Cmd: "set", Args: settings}, nil
	case "apply":
		if len(rest) == 0 {
			return ctlRequest{}, fmt.Errorf("apply needs a preset name (see dvctl presets)")
		}
		presets := ctlPresets()
		merged := map[string]string{}
		for _, name := range rest {
			settings, ok := presets[name]
			if !ok {
				return ctlRequest{}, fmt.Errorf("no preset named %q (see dvctl presets)", name)
			}
			for k, v := range settings {
				merged[k] = v
			}
		}
		return ctlRequest{Cmd: "set", Args: merged}, nil
	case "cycle":
		if len(rest) != 1 {
			return ctlRequest{}, fmt.Errorf("cycle needs one of tint|fit|view|dual")
		}
		return ctlRequest{Cmd: "cycle", Args: map[string]string{"what": rest[0]}}, nil
	case "page":
		if len(rest) != 1 {
			return ctlRequest{}, fmt.Errorf("page needs N, +N, -N, first, last, next or prev")
		}
		return ctlRequest{Cmd: "page", Args: map[string]string{"to": rest[0]}}, nil
	case "search":
		if len(rest) == 1 {
			switch rest[0] {
			case "next", "prev", "clear":
				return ctlRequest{Cmd: "search", Args: map[string]string{"action": rest[0]}}, nil
			}
		}
		query := strings.TrimSpace(strings.Join(rest, " "))
		if query == "" {
			return ctlRequest{}, fmt.Errorf("search needs a query (or next|prev|clear)")
		}
		return ctlRequest{Cmd: "search", Args: map[string]string{"query": query}}, nil
	case "key":
		if len(rest) == 0 {
			return ctlRequest{}, fmt.Errorf("key needs the characters to send")
		}
		return ctlRequest{Cmd: "key", Args: map[string]string{"keys": strings.Join(rest, "")}}, nil
	case "sync":
		if len(rest) != 1 && len(rest) != 3 {
			return ctlRequest{}, fmt.Errorf("sync needs PAGE, or PAGE X Y")
		}
		args := map[string]string{"page": rest[0]}
		if len(rest) == 3 {
			args["x"], args["y"] = rest[1], rest[2]
		}
		return ctlRequest{Cmd: "sync", Args: args}, nil
	}
	return ctlRequest{}, fmt.Errorf("unknown command %q (try dvctl help)", cmd)
}

func ctlList(opts ctlOpts) int {
	all := ctlInstances()
	if opts.target != "" {
		all = ctlMatches(all, opts.target)
	}
	if opts.asJSON {
		states := make([]ctlState, 0, len(all))
		for _, c := range all {
			states = append(states, c.state)
		}
		data, _ := json.MarshalIndent(states, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	if len(all) == 0 {
		if opts.target != "" {
			fmt.Fprintf(os.Stderr, "dvctl: no pdf-cli viewer matches %q\n", opts.target)
		} else {
			fmt.Fprintln(os.Stderr, "dvctl: no pdf-cli viewer is running")
		}
		return 1
	}
	states := make([]ctlState, 0, len(all))
	for _, c := range all {
		states = append(states, c.state)
	}
	ctlPrintStates(states)
	return 0
}

func ctlPrintStates(states []ctlState) {
	if len(states) == 0 {
		return
	}
	fmt.Printf("%-7s %-9s %-7s %-7s %-11s %-6s %-6s %s\n",
		"PID", "PAGE", "TINT", "FIT", "DUAL", "ZOOM", "CROP", "FILE")
	for _, s := range states {
		crop := "-"
		if s.CropTop > 0 || s.CropBottom > 0 || s.CropLeft > 0 || s.CropRight > 0 {
			crop = fmt.Sprintf("%.0f/%.0f/%.0f/%.0f",
				s.CropTop*100, s.CropBottom*100, s.CropLeft*100, s.CropRight*100)
		}
		dual := s.Dual
		if dual == "half" {
			dual = "half:" + s.HalfOffset
		}
		fmt.Printf("%-7d %-9s %-7s %-7s %-11s %-6s %-6s %s\n",
			s.PID, fmt.Sprintf("%d/%d", s.Page, s.Pages), s.Tint, s.Fit, dual,
			fmt.Sprintf("%.0f%%", s.Zoom*100), crop, s.Name)
	}
}

func ctlListPresets(opts ctlOpts) int {
	presets := ctlPresets()
	if opts.asJSON {
		data, _ := json.MarshalIndent(presets, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		keys := make([]string, 0, len(presets[name]))
		for k := range presets[name] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+"="+presets[name][k])
		}
		fmt.Printf("%-10s %s\n", name, strings.Join(parts, " "))
	}
	fmt.Printf("\nUser presets: %s\n", ctlPresetPath())
	return 0
}

const ctlHelp = `dvctl - control running pdf-cli viewers

USAGE:
    dvctl COMMAND [ARGS] [--target T | --all] [--json] [--quiet]

TARGETING:
    --target T   a pid, an agterm session id, or part of the file name or path
    --all        every running viewer
    (neither)    the only running viewer; an error when several are running
    --           ends flag parsing, so a query may contain --all and the like

COMMANDS:
    list                     show every running viewer
    status                   show the target's state
    set --KEY VALUE ...      change settings (see SETTINGS)
    apply PRESET ...         apply named setting bundles (see: dvctl presets)
    presets                  list built-in and user presets
    cycle tint|fit|view|dual advance one setting, as the keyboard does
    page N|+N|-N|first|last|next|prev
    search QUERY | search next|prev|clear
    sync PAGE [X Y]          forward-sync jump, optionally to a point in points
    key CHARS                send raw viewer keys; the ones that open one of the
                             viewer's own prompts (/ g h ? d T) are refused,
                             since only a keyboard can answer those
    toc                      print the document outline
    reload | refresh         re-read the file / re-detect the cell size
    quit | back              close the viewer / return to its file picker

SETTINGS (dvctl set):
    --tint    off | dim | dark | invert     (aliases: white, pale, gray, inv)
    --fit     height | width | auto
    --view    auto | text | image
    --dual    off | vertical | horizontal | half
    --half    top | bottom
    --zoom    0.1-2.0, or a delta such as +0.1
    --crop    0-0.45 for all four edges, or a delta such as +0.02
    --crop-top, --crop-bottom, --crop-left, --crop-right   one edge each
    --html-width  200-3000 points (reflowable documents only)
    --page    same values as the page command

EXAMPLES:
    dvctl list
    dvctl set --tint dim --all
    dvctl apply laptop --all
    dvctl set --dual half --crop 0.06 --target paper.pdf
    dvctl page +1 --target 81234
    dvctl search -- --all cases
    dvctl status --json
`
