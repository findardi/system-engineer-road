package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

//go:embed dist
var dist embed.FS

var arches = map[string]string{
	"Linux x86_64":  "linux-amd64",
	"Linux aarch64": "linux-arm64",
}

// installDirs are tried in order: home may be mounted noexec, so fall back
// to the runtime dir and /tmp. A directory must be ours, so nobody else can
// swap the binary (a pre-created /tmp/gosh-UID is skipped).
const installDirs = `"$HOME/.gosh/bin" ${XDG_RUNTIME_DIR:+"$XDG_RUNTIME_DIR/gosh"} "/tmp/gosh-$(id -u)"`

// Remote scripts run under sh explicitly: the user's login shell may be fish/zsh.
// $1 = binary name; run: $2 = window size; install: $2 = sha256, $3 = window size.
// "probe" proves the binary can execute there, which [ -x ] cannot tell on noexec.
const runScript = `for d in ` + installDirs + `; do
  b="$d/$1"
  [ -x "$b" ] && [ -O "$b" ] && "$b" probe 2>/dev/null && exec "$b" new "$2"
done
echo "GOSH NEED $(uname -sm)"`

const installScript = `set -e
umask 077
src=
for d in ` + installDirs + `; do
  mkdir -p "$d" 2>/dev/null && [ -O "$d" ] && chmod 700 "$d" || continue
  t="$d/$1.tmp"
  if [ -z "$src" ]; then
    src="$t"
    cat > "$src"
    [ "$(sha256sum < "$src" | cut -d" " -f1)" = "$2" ] || { rm -f "$src"; echo "gosh: checksum mismatch" >&2; exit 1; }
  elif ! cp "$src" "$t"; then
    continue
  fi
  chmod 700 "$t"
  if "$t" probe 2>/dev/null; then
    mv "$t" "$d/$1"
    rm -f "$src"
    for f in "$d"/gosh-server-*; do # old versions, unless a live session still runs one
      [ "$f" = "$d/$1" ] || pgrep -f "$f" >/dev/null || rm -f "$f"
    done
    exec "$d/$1" new "$3"
  fi
  [ "$t" = "$src" ] || rm -f "$t"
done
rm -f "$src"
echo "gosh: no directory on the server can run programs (all mounted noexec?)" >&2
exit 1`

var connectRe = regexp.MustCompile(`^GOSH CONNECT (\d+) ([A-Za-z0-9/+]{22})$`)

type session struct {
	IP, Port, Key string
	Width, Height int    // window size at start: the initial screen on both sides
	Server        string // binary name in installDirs, needed to start the relay
}

func bootstrap(ssh []string, host string, width, height int) (session, error) {
	ip, err := resolveIP(ssh, host)
	if err != nil {
		return session{}, err
	}
	name, err := serverName()
	if err != nil {
		return session{}, err
	}

	size := fmt.Sprintf("%dx%d", width, height)
	out, err := remote(ssh, host, runScript, nil, name, size)
	if err != nil {
		return session{}, err
	}

	s, need, err := parse(out)
	if need == "" {
		s.IP, s.Server, s.Width, s.Height = ip, name, width, height
		return s, err
	}

	arch, ok := arches[need]
	if !ok {
		return session{}, fmt.Errorf("unsupported server platform %q", need)
	}

	bin, err := dist.ReadFile("dist/gosh-server-" + arch)
	if err != nil {
		return session{}, fmt.Errorf("client built without %s server binary, run make build", arch)
	}

	sum := sha256.Sum256(bin)
	fmt.Fprintf(os.Stderr, "gosh: installing server on %s (%s, %d KB)\n", host, arch, len(bin)/1024)
	if out, err = remote(ssh, host, installScript, bin, name, hex.EncodeToString(sum[:]), size); err != nil {
		return session{}, err
	}
	s, _, err = parse(out)
	s.IP, s.Server, s.Width, s.Height = ip, name, width, height
	return s, err
}

// resolveIP returns the address the client itself dialed for SSH, not the one
// the server reports: behind NAT the server only knows its private address.
// ssh -G applies ~/.ssh/config, so aliases resolve to the real HostName.
func resolveIP(ssh []string, host string) (string, error) {
	argv := append(append(ssh[1:], "-G"), host)
	out, err := exec.Command(ssh[0], argv...).Output()
	if err != nil {
		return "", fmt.Errorf("ssh -G %s: %w", host, err)
	}
	hostname := host
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "hostname "); ok {
			hostname = v
			break
		}
	}
	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return "", err
	}
	return addrs[0], nil
}

func serverName() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(dist, "dist", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := dist.ReadFile(path)
		h.Write(b)
		return err
	})

	return "gosh-server-" + hex.EncodeToString(h.Sum(nil))[:16], err
}

// sshCommand runs script under sh on host, so ~/.ssh/config, agent,
// known_hosts and ProxyJump all work exactly like plain ssh.
func sshCommand(ssh []string, host, script string, args ...string) *exec.Cmd {
	cmdLine := "sh -c '" + script + "' gosh " + strings.Join(args, " ")
	argv := append(append(ssh[1:], "-T", host, "--"), cmdLine)
	return exec.Command(ssh[0], argv...)
}

func remote(ssh []string, host, script string, stdin []byte, args ...string) ([]byte, error) {
	cmd := sshCommand(ssh, host, script, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", host, err)
	}
	return out, nil
}

func parse(out []byte) (s session, need string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "GOSH NEED "):
			need = strings.TrimPrefix(line, "GOSH NEED ")
		case strings.HasPrefix(line, "GOSH CONNECT "):
			m := connectRe.FindStringSubmatch(line)
			if m == nil {
				return s, "", fmt.Errorf("bad connect line %q", line)
			}
			s.Port, s.Key = m[1], m[2]
		}
	}

	if need == "" && s.Key == "" {
		return s, "", fmt.Errorf("server did not start, output: %q", out)
	}

	return s, need, nil
}
