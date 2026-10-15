package main

import (
	"fmt"
	"log"
	"net"
)

// this is just basic UDP
func main() {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:1212")
	if err != nil {
		log.Fatal("Couldn't resolve address:", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal("Listen failed:", err)
	}

	defer conn.Close()

	// Set Buffer Size
	buffer := make([]byte, 1024)

	for {
		n, clientAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			log.Printf("Read error: %v", err)
			continue
		}

		fmt.Printf("Got message from %s: %s", clientAddr, string(buffer[:n]))

		// UDP Server Response Back same as Client request
		response := fmt.Sprintf("Response From %s:%d : ", addr.IP, addr.Port)
		message := []byte(response + string(buffer[:n]))
		_, err = conn.WriteToUDP(message, clientAddr)
		if err != nil {
			log.Printf("write error: %v", err)
		}
	}
}
