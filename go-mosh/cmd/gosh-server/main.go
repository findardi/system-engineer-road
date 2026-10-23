package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/findardi/system-engineer-road/go-mosh/internal/network"
	"github.com/findardi/system-engineer-road/go-mosh/internal/statesync"
)

const (
	portLow  = 60001
	portHigh = 60999

	noClientTimeout = 60 * time.Second // nobody connected after start, as mosh-server
	idleTimeout     = 30 * time.Minute // client silent this long: assume it is gone for good
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gosh-server new")
		os.Exit(2)
	}

	var err error

	switch os.Args[1] {
	case "new":
		err = runNew(os.Args[2:])
	case "daemon":
		err = runDaemon(os.Args[2:])
	case "relay": // internal: started by the client over SSH when UDP is blocked
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: gosh-server relay PORT")
			break
		}
		err = network.Relay(os.Stdin, os.Stdout, net.JoinHostPort("127.0.0.1", os.Args[2]))
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "gosh-server:", err)
		os.Exit(1)
	}
}

// parseSize reads the client's window size, "COLSxROWS". Both sides of SSP
// must start from the same screen, and mosh-client starts from its window.
func parseSize(args []string) (w, h int) {
	w, h = 80, 24
	if len(args) > 0 {
		var cw, ch int
		if n, _ := fmt.Sscanf(args[0], "%dx%d", &cw, &ch); n == 2 && cw > 0 && ch > 0 && cw <= 1000 && ch <= 1000 {
			w, h = cw, ch
		}
	}
	return w, h
}

func runNew(args []string) error {
	conn, port, err := listen()
	if err != nil {
		return err
	}

	sock, err := conn.File()
	if err != nil {
		return err
	}

	key := make([]byte, 16)
	rand.Read(key)
	printable := base64.RawStdEncoding.EncodeToString(key)

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	cols, rows := parseSize(args)
	cmd := exec.Command(self, "daemon", fmt.Sprintf("%dx%d", cols, rows))
	cmd.Stdin = r
	cmd.ExtraFiles = []*os.File{sock}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	w.WriteString(printable)
	w.Close()
	cmd.Process.Release()

	fmt.Printf("\nGOSH CONNECT %d %s\n", port, printable)
	return nil
}

func listen() (*net.UDPConn, int, error) {
	for p := portLow; p <= portHigh; p++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{
			Port: p,
		})

		if err == nil {
			return conn, p, nil
		}
	}

	return nil, 0, fmt.Errorf("no free UDP port in %d-%d", portLow, portHigh)
}

func runDaemon(args []string) error {
	key, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}

	sess, err := network.NewSession(string(key))
	if err != nil {
		return err
	}

	f := os.NewFile(3, "udp")
	pc, err := net.FilePacketConn(f)
	f.Close()

	if err != nil {
		return err
	}

	conn := network.NewServer(sess, pc.(*net.UDPConn))

	w, h := parseSize(args)
	shell, err := startShell(w, h)
	if err != nil {
		return err
	}
	defer shell.Close() // closing the PTY master hangs up the shell

	// Both sides start from the same blank screen, sized like the client's window.
	t := network.NewTransport(conn, statesync.NewComplete(w, h), &statesync.UserStream{})
	output := make(chan []byte)
	go func() {
		b := make([]byte, 16*1024)
		for {
			n, err := shell.Read(b)
			if err != nil {
				close(output)
				return
			}

			output <- bytes.Clone(b[:n])
		}
	}()

	started := time.Now()
	for {
		out := output
		if t.ShutdownInProgress() {
			out = nil
		}

		timer := time.NewTimer(t.WaitTime())
		select {
		case b, ok := <-out:
			if !ok {
				t.StartShutdown() // shell exited: tell the client
				break
			}
			// The screen absorbs any amount of output, so the diff stays one
			// screen in size however long the client is away.
			if reply := t.Current().Act(b); len(reply) > 0 {
				shell.Write(reply) // e.g. a cursor position report the program asked for
			}
		case d := <-conn.Incoming():
			if t.Receive(d) != nil {
				break
			}

			var input statesync.UserStream
			input.Apply(t.RemoteDiff())

			emu := t.Current().Emulator()
			var keys []byte
			for _, e := range input.Events() {
				if e.Resize {
					emu.Resize(int(e.Width), int(e.Height))
					pty.Setsize(shell, &pty.Winsize{Rows: uint16(e.Height), Cols: uint16(e.Width)})
				} else {
					keys = append(keys, emu.UserByte(e.Byte)...)
				}
			}

			shell.Write(keys)

		case <-timer.C:
		}

		timer.Stop()

		switch {
		case t.ShutdownInProgress() && (t.ShutdownAcknowledged() || t.ShutdownAckTimedOut()):
			return nil
		case t.CounterpartyShutdownAckSent():
			return nil
		case sessionExpired(started, conn.LastHeard(), time.Now()):
			return nil
		}
		t.Tick() // send errors are transient: SSP resends from the acked state
	}
}

// sessionExpired reports whether the daemon should give up on its client.
// A connected client sends an SSP ack at least every 3s, even when the user
// is not typing, so only a client that is really gone (killed, laptop asleep)
// trips idleTimeout.
func sessionExpired(started, lastHeard, now time.Time) bool {
	if lastHeard.IsZero() {
		return now.Sub(started) > noClientTimeout
	}
	return now.Sub(lastHeard) > idleTimeout
}

// startShell runs the user's login shell on a new PTY, as mosh-server does.
func startShell(w, h int) (*os.File, error) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}

	cmd := exec.Command(sh)
	cmd.Args[0] = "-" + filepath.Base(sh)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	if err != nil {
		return nil, err
	}

	go cmd.Wait()
	return f, nil
}
