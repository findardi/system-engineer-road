package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
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

	s, err := bootstrap(strings.Fields(*sshCmd), flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gosh:", err)
		os.Exit(1)
	}

	fmt.Printf("server %s:%s key %s\n", s.IP, s.Port, s.Key)
}
