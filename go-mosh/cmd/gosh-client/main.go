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

	scr := newScreen(os.Stdout, s.Width, s.Height)
	defer scr.close()

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
	var flash string // short-lived notice
	var flashUntil time.Time
	escape := false
	for {
		// Wake at least every second to keep the "last contact" notice current.
		timer := time.NewTimer(min(t.WaitTime(), time.Second))
		select {
		case b, ok := <-input:
			if !ok {
				input = nil
				t.StartShutdown()
				break
			}

			for _, c := range b {
				if t.ShutdownInProgress() {
					break
				}

				if escape {
					escape = false
					if c == '.' {
						t.StartShutdown()
						continue
					}
					t.Current().PushBytes([]byte{escapeKey})
				}

				if c == escapeKey {
					escape = true
					continue
				}

				t.Current().PushBytes([]byte{c})
			}

		case <-resize:
			if w, h, err := term.GetSize(fd); err == nil && !t.ShutdownInProgress() {
				t.Current().PushResize(int32(w), int32(h))
			}
			scr.invalidate() // the terminal reflowed its contents
		case d := <-conn.Incoming():
			t.Receive(d) // the screen is repainted below
		case <-fallback:
			if conn.LastHeard().IsZero() {
				flash, flashUntil = "gosh: no UDP reply, using SSH relay", time.Now().Add(3*time.Second)
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

		notice := ""
		if time.Now().Before(flashUntil) {
			notice = flash
		}
		// SSP acks arrive at least every 3s, so silence means trouble.
		if last := conn.LastHeard(); !last.IsZero() && time.Since(last) > 5*time.Second {
			notice = fmt.Sprintf("gosh: last contact %s ago. [To quit: Ctrl-^ .]", time.Since(last).Round(time.Second))
		}
		scr.paint(t.Remote(), notice)
	}
}
