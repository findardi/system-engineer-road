package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/findardi/system-engineer-road/go-mosh/internal/network"
	"github.com/findardi/system-engineer-road/go-mosh/internal/statesync"
)

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

	if err := echoLoop(s, relayDialer(ssh, host, s)); err != nil {
		fmt.Fprintln(os.Stderr, "gosh:", err)
		os.Exit(1)
	}
}

// ponytail: phase 4 demo, stdin lines go to the server over SSP and its echo is
// printed; phase 5 replaces it with a raw-mode terminal.
func echoLoop(s session, relay network.StreamDialer) error {
	sess, err := network.NewSession(s.Key)
	if err != nil {
		return err
	}
	conn, err := network.Dial(sess, net.JoinHostPort(s.IP, s.Port))
	if err != nil {
		return err
	}
	empty := &statesync.UserStream{}
	t := network.NewTransport(conn, empty, empty)
	t.SendDelay = 1 // keystrokes go out at once, as in mosh-client
	fmt.Fprintf(os.Stderr, "gosh: connected to %s:%s, type lines, Ctrl-D to quit\n", s.IP, s.Port)

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
	lost := false
	for {
		timer := time.NewTimer(t.WaitTime())
		select {
		case b, ok := <-input:
			if !ok {
				return nil
			}
			t.Current().PushBytes(b)
		case d := <-conn.Incoming():
			if t.Receive(d) != nil {
				break
			}
			var echo statesync.UserStream
			echo.Apply(t.RemoteDiff())
			for _, e := range echo.Events() {
				os.Stdout.Write([]byte{e.Byte})
			}
		case <-fallback:
			if conn.LastHeard().IsZero() {
				fmt.Fprintln(os.Stderr, "gosh: no UDP reply, falling back to SSH relay")
				if err := conn.UseStream(relay); err != nil {
					return err
				}
			}
		case <-timer.C:
		}
		timer.Stop()
		t.Tick() // send errors are transient: SSP resends from the acked state

		// Report only after first contact; SSP acks arrive at least every 3s.
		last := conn.LastHeard()
		if silent := !last.IsZero() && time.Since(last) > 5*time.Second; silent != lost {
			lost = silent
			if lost {
				fmt.Fprintln(os.Stderr, "gosh: connection lost, retrying...")
			} else {
				fmt.Fprintln(os.Stderr, "gosh: reconnected")
			}
		}
	}
}
