package main

import (
	"io"
	"os"
	"os/exec"

	"github.com/findardi/system-engineer-road/go-mosh/internal/network"
)

const relayScript = `for d in ` + installDirs + `; do
  [ -x "$d/$1" ] && [ -O "$d/$1" ] && "$d/$1" probe 2>/dev/null && exec "$d/$1" relay "$2"
done
exit 1`

// relayDialer starts "gosh-server relay" over a new SSH session each time it
// is called. With key or agent auth this reconnects silently after a network change.
func relayDialer(ssh []string, host string, s session) network.StreamDialer {
	return func() (io.ReadWriteCloser, error) {
		cmd := sshCommand(ssh, host, relayScript, s.Server, s.Port)
		cmd.Stderr = os.Stderr
		w, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		r, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &sshStream{cmd, r, w}, nil
	}
}

type sshStream struct {
	cmd *exec.Cmd
	io.Reader
	w io.WriteCloser
}

func (s *sshStream) Write(b []byte) (int, error) { return s.w.Write(b) }

func (s *sshStream) Close() error {
	s.w.Close()
	s.cmd.Process.Kill()
	return s.cmd.Wait()
}
