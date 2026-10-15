package main

import (
	"encoding/binary"
	"flag"
	"log"
	"net"
)

func main() {
	count := flag.Int("n", 1000, "jumlah paket")
	size := flag.Int("size", 64, "ukuran paket (byte, min 8)")
	flag.Parse()

	conn, err := net.Dial("udp", "127.0.0.1:1212")
	if err != nil {
		log.Fatal(err)
	}

	buffer := make([]byte, max(*size, 8))
	binary.BigEndian.PutUint32(buffer[4:8], uint32(*count))

	for i := 0; i < *count; i++ {
		binary.BigEndian.PutUint32(buffer[0:4], uint32(i))
		if _, err := conn.Write(buffer); err != nil {
			log.Printf("write %d: %v", i, err)
		}
	}

	log.Printf("sent %d packets", *count)
}
