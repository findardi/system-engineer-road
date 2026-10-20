package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
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

	_ = key

	conn, err := net.FilePacketConn(os.NewFile(3, "udp"))
	if err != nil {
		return err
	}

	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	buf := make([]byte, 2048)
	conn.ReadFrom(buf)
	return nil
}
