package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/findardi/system-engineer-road/go-mosh/internal/network"
	"github.com/findardi/system-engineer-road/go-mosh/internal/statesync"
	"github.com/findardi/system-engineer-road/go-mosh/internal/terminal"
	"golang.org/x/term"
)

const escapeKey = 0x1e

func main() {
	sshCmd := flag.String("ssh", "ssh", "ssh command, e.g \"ssh -p 2222\"")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gosh-client [-ssh CMD] [user@]host")
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	ssh, host := strings.Fields(*sshCmd), flag.Arg(0)
	width, height := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		width, height = w, h
	}
	s, err := bootstrap(ssh, host, width, height)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gosh:", err)
		os.Exit(1)
	}

	err = shellLoop(s, relayDialer(ssh, host, s))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gosh:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "[gosh is exiting.]")
}

func shellLoop(s session, relay network.StreamDialer) error {
	sess, err := network.NewSession(s.Key)
	if err != nil {
		return err
	}

	conn, err := network.Dial(sess, net.JoinHostPort(s.IP, s.Port))
	if err != nil {
		return err
	}

	// Same blank start as the server, sized like our window; see gosh-server.
	t := network.NewTransport(conn, &statesync.UserStream{}, statesync.NewComplete(s.Width, s.Height))
	t.SendDelay = 1

	fd := int(os.Stdin.Fd())
	saved, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("stdin is not a terminal: %w", err)
	}
	defer term.Restore(fd, saved)

	scr := newScreen(os.Stdout, os.Getenv("TERM"), s.Width, s.Height)
	defer scr.close()
	pe := newPredictionEngine()

	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	resize <- syscall.SIGWINCH

	input := make(chan []byte)
	go func() {
		b := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(b)
			if err != nil {
				close(input)
				return
			}
			input <- bytes.Clone(b[:n])
		}
	}()

	fallback := time.After(3 * time.Second)
	var msg message // notice text other than the "last contact" countdown
	setMessage := func(text string, d time.Duration, quitHint bool) { msg = message{text, time.Now().Add(d), quitHint} }
	escape := false
	for {
		// Wake at least every second to keep the "last contact" notice current.
		timer := time.NewTimer(min(t.WaitTime(), time.Second, pe.WaitTime()))
		select {
		case b, ok := <-input:
			if !ok {
				input = nil
				t.StartShutdown()
				break
			}
			if t.ShutdownInProgress() {
				break
			}

			pe.SetFrameSent(t.SentStateLast())
			paste := len(b) > 100 // don't predict bulk input
			if paste {
				pe.Reset()
			}
			for _, c := range b {
				if t.ShutdownInProgress() {
					break
				}
				if !paste {
					pe.NewUserByte(c, scr.painted)
				}

				if escape {
					escape = false
					msg = message{}
					switch c {
					case '.': // Ctrl-^ . quits
						setMessage("Exiting on user request...", time.Hour, true)
						t.StartShutdown()
					case '^': // Ctrl-^ ^ types a literal Ctrl-^
						t.Current().PushBytes([]byte{escapeKey})
					default: // anything else goes through with the escape key
						t.Current().PushBytes([]byte{escapeKey, c})
					}
					continue
				}

				if c == escapeKey {
					escape = true
					setMessage(`Commands: "." quits, "^" gives literal Ctrl-^`, time.Hour, false)
					continue
				}

				if c == 0x0c { // Ctrl-L: the user asks for a redraw, repaint locally too
					scr.invalidate()
				}
				t.Current().PushBytes([]byte{c})
			}

		case <-resize:
			if w, h, err := term.GetSize(fd); err == nil && !t.ShutdownInProgress() {
				t.Current().PushResize(int32(w), int32(h))
			}
			scr.invalidate() // the terminal reflowed its contents
			pe.Reset()
		case d := <-conn.Incoming():
			t.Receive(d) // the screen is repainted below
		case <-fallback:
			if conn.LastHeard().IsZero() {
				setMessage("No UDP reply, using SSH relay.", 3*time.Second, true)
				if err := conn.UseStream(relay); err != nil {
					return err
				}
			}
		case <-timer.C:
		}
		timer.Stop()

		switch {
		case t.ShutdownInProgress() && (t.ShutdownAcknowledged() || t.ShutdownAckTimedOut()):
			return nil
		case t.CounterpartyShutdownAckSent(): // shell exited on the server
			return nil
		}
		t.Tick()

		pe.SetFrameAcked(t.SentStateAcked())
		pe.SetFrameLateAcked(t.Remote().EchoAck())
		pe.SetSendInterval(t.SendInterval())
		scr.paint(t.Remote(), pe, notice(msg, conn.LastHeard(), time.Now()))
	}
}

type message struct {
	text     string
	until    time.Time
	quitHint bool // append "[To quit: Ctrl-^ .]"
}

// notice is the notification bar text, in mosh's wording. A server silent for
// over 6.5s (SSP acks come every 3s) gets a running "last contact" count.
func notice(m message, lastHeard, now time.Time) string {
	if now.After(m.until) {
		m = message{}
	}
	since := now.Sub(lastHeard)
	late := !lastHeard.IsZero() && since > 6500*time.Millisecond
	quit := ""
	if m.quitHint || m.text == "" {
		quit = " [To quit: Ctrl-^ .]"
	}
	switch {
	case m.text != "" && late:
		return "gosh: " + m.text + " (" + humanDuration(since, "s") + " without contact.)" + quit
	case m.text != "":
		return "gosh: " + m.text + quit
	case late:
		return "gosh: Last contact " + humanDuration(since, "seconds") + " ago." + quit
	}
	return ""
}

// newPredictionEngine reads GOSH_PREDICTION (adaptive, always, never,
// experimental) and GOSH_PREDICTION_OVERWRITE=yes, like mosh's
// MOSH_PREDICTION_DISPLAY and MOSH_PREDICTION_OVERWRITE.
func newPredictionEngine() *terminal.PredictionEngine {
	pe := terminal.NewPredictionEngine()
	pe.Mode = map[string]terminal.PredictionMode{
		"always": terminal.PredictAlways, "never": terminal.PredictNever, "experimental": terminal.PredictExperimental,
	}[os.Getenv("GOSH_PREDICTION")] // anything else: adaptive
	pe.Overwrite = os.Getenv("GOSH_PREDICTION_OVERWRITE") == "yes"
	return pe
}
