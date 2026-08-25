package viewer

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// agterm's sidebar and session picker show the session NAME, and a session takes
// its name from the terminal title once — later title changes move the title but
// not the name. So the OSC 2 title alone would leave the session list stuck at
// the page the document opened on. agtermReporter keeps the name current by
// renaming the session through agtermctl as the page changes. A rename is
// sticky, so the name the session had before is captured here and put back on
// exit.
//
// Everything is best-effort: outside agterm, or without agtermctl on PATH,
// newAgtermReporter returns nil and every method is a no-op.
type agtermReporter struct {
	sessionID string
	origName  string

	wake chan struct{} // buffered 1: a pending rename, coalescing rapid page turns
	done chan struct{} // closed on shutdown; the loop exits promptly, even mid-sleep
	wg   sync.WaitGroup
	stop sync.Once

	mu   sync.Mutex
	want string // newest name asked for
	sent string // newest name pushed to agterm
}

// agtermRenameInterval rate-limits renames: a held-down page key must not spawn
// a process per page.
const agtermRenameInterval = 250 * time.Millisecond

// sidebarDecoration matches what agterm prefixes to the name it reports: the
// "⌘3 " / "3· " position marker, and a "[03]" activity counter. Both are display
// text rather than part of the name, so they have to come off before the name
// can be handed back to a rename — otherwise a restore bakes them in.
var sidebarDecoration = regexp.MustCompile(`^(?:(?:⌘\d+|\d+·)\s+)?(?:\[\d+\]\s+)?`)

func newAgtermReporter() *agtermReporter {
	sessionID := os.Getenv("AGTERM_SESSION_ID")
	if os.Getenv("AGTERM_ENABLED") != "1" || sessionID == "" {
		return nil
	}
	// One name covers the whole session, so a reader in a split or scratch pane
	// would label the session after a document while its main pane is running
	// something else entirely. Only the main pane speaks for the session.
	if pane := os.Getenv("AGTERM_PANE"); pane != "" && pane != "left" {
		return nil
	}
	if _, err := exec.LookPath("agtermctl"); err != nil {
		return nil
	}
	r := &agtermReporter{
		sessionID: sessionID,
		origName:  agtermSessionName(sessionID),
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

// report asks for the session to be named title. Called from the render path, so
// it must not block: the rename happens on the reporter's goroutine.
func (r *agtermReporter) report(title string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.want = title
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default: // a rename is already pending; it will pick up the newer name
	}
}

// close stops renaming, waits out any rename in flight, and then restores the
// name the session had at startup — the wait guarantees the restore lands last.
// Both the signal handler and Run's defer call it; the second call is a no-op.
func (r *agtermReporter) close() {
	if r == nil {
		return
	}
	r.stop.Do(func() {
		close(r.done)
		r.wg.Wait()
		r.restore()
	})
}

// restore puts the original name back; skipped when the capture at startup came
// up empty, leaving an unknown name alone rather than guessing at one.
func (r *agtermReporter) restore() {
	if r.origName == "" {
		return
	}
	r.rename(r.origName)
}

func (r *agtermReporter) loop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.done:
			return
		case <-r.wake:
		}
		r.mu.Lock()
		title := r.want
		r.mu.Unlock()
		if title == "" || title == r.sent {
			continue
		}
		r.rename(title)
		r.sent = title

		// Pages turned during the rename collapse into one more pass.
		select {
		case <-r.done:
			return
		case <-time.After(agtermRenameInterval):
		}
		r.mu.Lock()
		pending := r.want != r.sent
		r.mu.Unlock()
		if pending {
			select {
			case r.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (r *agtermReporter) rename(name string) {
	exec.Command("agtermctl", "session", "rename", name, "--target", r.sessionID).Run()
}

// agtermSessionName reads a session's current name out of the tree. Returns ""
// when agterm cannot be reached or does not know the session, which leaves the
// name untouched on exit rather than guessing at one.
func agtermSessionName(sessionID string) string {
	out, err := exec.Command("agtermctl", "tree", "--json").Output()
	if err != nil {
		return ""
	}
	var tree struct {
		Result struct {
			Tree struct {
				Workspaces []struct {
					Sessions []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"sessions"`
				} `json:"workspaces"`
			} `json:"tree"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &tree) != nil {
		return ""
	}
	for _, ws := range tree.Result.Tree.Workspaces {
		for _, s := range ws.Sessions {
			if strings.EqualFold(s.ID, sessionID) {
				return sidebarDecoration.ReplaceAllString(s.Name, "")
			}
		}
	}
	return ""
}
