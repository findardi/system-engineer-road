package main

import (
	"fmt"
	"io"
	"time"

	"github.com/findardi/system-engineer-road/go-mosh/internal/statesync"
	"github.com/findardi/system-engineer-road/go-mosh/internal/terminal"
)

// screen paints the remote terminal onto the user's terminal, redrawing only
// what changed since the last paint. On top go the local predictions and
// mosh's notification bar.
type screen struct {
	out         io.Writer
	display     *terminal.Display
	painted     *terminal.Framebuffer // what the user's terminal shows now
	initialized bool                  // false: assume nothing, repaint everything
}

func newScreen(out io.Writer, term string, width, height int) *screen {
	s := &screen{out: out, display: terminal.NewDisplayForTerminal(term), painted: terminal.NewFramebuffer(width, height)}
	io.WriteString(out, s.display.Open())
	return s
}

func (s *screen) close() { io.WriteString(s.out, s.display.Close()) }

// invalidate forces a full repaint, e.g. after a resize or Ctrl-L.
func (s *screen) invalidate() { s.initialized = false }

func (s *screen) paint(remote *statesync.Complete, pe *terminal.PredictionEngine, notice string) {
	fb := remote.Framebuffer().Clone()
	pe.Cull(fb)
	pe.Apply(fb)
	if notice != "" {
		terminal.DrawNotice(fb, notice)
	}
	io.WriteString(s.out, s.display.NewFrame(s.initialized, s.painted, fb))
	s.painted, s.initialized = fb, true
}

// humanDuration formats like mosh: "12 seconds", "3:07", "1:02:03".
func humanDuration(d time.Duration, secondsWord string) string {
	n := int(d.Seconds())
	switch {
	case n < 60:
		return fmt.Sprintf("%d %s", n, secondsWord)
	case n < 3600:
		return fmt.Sprintf("%d:%02d", n/60, n%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", n/3600, n/60%60, n%60)
}
