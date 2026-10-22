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
	"syscall"
	"time"

	"github.com/findardi/system-engineer-road/go-mosh/internal/network"
	"github.com/findardi/system-engineer-road/go-mosh/internal/statesync"
)

const (
	portLow  = 60001
	portHigh = 60999
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gosh-server new")
		os.Exit(2)
	}

	var err error

	switch os.Args[1] {
	case "new":
		err = runNew()
	case "daemon":
		err = runDaemon()
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

func runNew() error {
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

	cmd := exec.Command(self, "daemon")
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

func runDaemon() error {
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
	empty := &statesync.UserStream{}
	t := network.NewTransport(conn, empty, empty)

	// ponytail: phase 4 echo server over SSP; phase 5 replaces it with a shell on a PTY.
	// Exits after 60s without hearing the client (SSP acks every 3s keep it alive).
	started := time.Now()
	for {
		timer := time.NewTimer(min(t.WaitTime(), time.Second))
		select {
		case d := <-conn.Incoming():
			if t.Receive(d) != nil {
				break // forged, corrupted or reflected: drop silently
			}
			var input statesync.UserStream
			input.Apply(t.RemoteDiff())
			for _, e := range input.Events() {
				if !e.Resize {
					// Uppercase, so the echo is distinguishable from the terminal's local echo.
					t.Current().PushBytes(bytes.ToUpper([]byte{e.Byte}))
				}
			}
		case <-timer.C:
			last := conn.LastHeard()
			if last.IsZero() {
				last = started
			}
			if time.Since(last) > 60*time.Second {
				return nil
			}
		}
		timer.Stop()
		t.Tick() // send errors are transient: SSP resends from the acked state
	}
}
