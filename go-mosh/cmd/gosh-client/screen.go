package main

import (
	"io"
	"strings"

	"github.com/findardi/system-engineer-road/go-mosh/internal/statesync"
	"github.com/findardi/system-engineer-road/go-mosh/internal/terminal"
)

// ponytail: assumes an xterm-like terminal (alternate screen, ECH, BCE);
// phase 6c reads the real capabilities from terminfo.
const (
	enterAltScreen = "\033[?1049h"
	exitAltScreen  = "\033[?1049l"
)

// screen paints the remote terminal onto the user's terminal, redrawing only
// what changed since the last paint, with an optional notice bar on top like
// mosh's.
type screen struct {
	out         io.Writer
	display     *terminal.Display
	painted     *terminal.Framebuffer // what the user's terminal shows now
	initialized bool                  // false: assume nothing, repaint everything
	lastRemote  *statesync.Complete
	lastNotice  string
}

func newScreen(out io.Writer, width, height int) *screen {
	s := &screen{out: out, display: terminal.NewDisplay(), painted: terminal.NewFramebuffer(width, height)}
	io.WriteString(out, enterAltScreen+s.display.Open())
	return s
}

func (s *screen) close() { io.WriteString(s.out, s.display.Close()+exitAltScreen) }

// invalidate forces a full repaint, e.g. after the window was resized.
func (s *screen) invalidate() { s.initialized = false }

func (s *screen) paint(remote *statesync.Complete, notice string) {
	if s.initialized && remote == s.lastRemote && notice == s.lastNotice {
		return
	}
	fb := remote.Framebuffer()
	if notice != "" {
		// Draw the bar on a copy, then restore cursor and pen, so the bar
		// never leaks into the session's state.
		e := remote.Emulator().Clone()
		w := fb.DS.Width()
		bar := []rune(" " + notice)
		if len(bar) > w {
			bar = bar[:w]
		}
		e.Write([]byte("\0337\033[?7l\033[1;1H\033[0;7m" + string(bar) + strings.Repeat(" ", w-len(bar)) + "\0338"))
		fb = e.Framebuffer()
	}
	io.WriteString(s.out, s.display.NewFrame(s.initialized, s.painted, fb))
	s.painted, s.initialized = fb.Clone(), true
	s.lastRemote, s.lastNotice = remote, notice
}
