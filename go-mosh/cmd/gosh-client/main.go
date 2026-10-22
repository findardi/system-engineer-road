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
	s, err := bootstrap(ssh, host)
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

	t := network.NewTransport(conn, &statesync.UserStream{}, &statesync.HostStream{})
	t.SendDelay = 1

	fd := int(os.Stdin.Fd())
	saved, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("stdin is not a terminal: %w", err)
	}

	defer term.Restore(fd, saved)
	notice := func(msg string) { fmt.Fprintf(os.Stderr, "\r\n[gosh: %s]\r\n", msg) }

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
	lost, escape := false, false
	for {
		timer := time.NewTimer(t.WaitTime())
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
		case d := <-conn.Incoming():
			if t.Receive(d) != nil {
				break
			}

			var screen statesync.HostStream
			screen.Apply(t.RemoteDiff())
			os.Stdout.Write(screen.Bytes())
		case <-fallback:
			if conn.LastHeard().IsZero() {
				notice("no UDP reply, falling back to SSH relay")
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

		last := conn.LastHeard()
		if silent := !last.IsZero() && time.Since(last) > 5*time.Second; silent != lost {
			lost = silent
			if lost {
				notice("connection lost, retrying...")
			} else {
				notice("reconnected")
			}
		}
	}
}
