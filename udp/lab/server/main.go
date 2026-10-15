package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"time"
)

func main() {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 1212,
	})

	if err != nil {
		log.Fatal(err)
	}

	// conn.SetReadBuffer(16 << 20)

	defer conn.Close()

	buffer := make([]byte, 65535)
	seen := map[uint32]bool{}
	var total, reordered, expected int
	var maxSeq uint32

	for {
		if total > 0 {
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		}

		n, _, err := conn.ReadFromUDP(buffer)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			break
		}

		if err != nil || n < 8 {
			continue
		}

		seq := binary.BigEndian.Uint32(buffer[0:4])
		expected = int(binary.BigEndian.Uint32(buffer[4:8]))
		total++

		if !seen[seq] && seq < maxSeq {
			reordered++
		}

		seen[seq] = true
		maxSeq = max(maxSeq, seq)
	}

	fmt.Printf("expected : %d\n", expected)
	fmt.Printf("received : %d\n", total)
	fmt.Printf("unique   : %d\n", len(seen))
	fmt.Printf("lost     : %d\n", expected-len(seen))
	fmt.Printf("duplicate: %d\n", total-len(seen))
	fmt.Printf("reordered: %d\n", reordered)
}
