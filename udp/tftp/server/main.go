package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	opRRQ   = 1
	opDATA  = 3
	opACK   = 4
	opERROR = 5
)

const (
	errNotDefined   = 0
	errFileNotFound = 1
	errUnknownTID   = 5
)

const (
	blockSize  = 512
	maxRetries = 5
	timeout    = 1 * time.Second
)

func main() {
	addr := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 6969,
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	log.Printf("TFTP server listening on: %s", addr)

	buf := make([]byte, 1024)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("read: %v", err)
			continue
		}

		filename, err := parseRRQ(buf[:n])
		if err != nil {
			log.Printf("bad request from %s: %v", clientAddr, err)
			sendError(conn, clientAddr, errNotDefined, err.Error())
			continue
		}

		log.Printf("RRQ %q from %s", filename, clientAddr)
		go handleRead(clientAddr, filename)
	}

}

func handleRead(clientAddr *net.UDPAddr, filename string) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0,
	})

	if err != nil {
		log.Printf("Listen: %v", err)
		return
	}

	defer conn.Close()

	data, err := os.ReadFile(filepath.Base(filename))
	if err != nil {
		sendError(conn, clientAddr, errFileNotFound, "file not found")
		return
	}

	buf := make([]byte, 1024)
	block := uint16(1)
	for offset := 0; ; offset += blockSize {
		end := min(offset+blockSize, len(data))
		packet := buildData(block, data[offset:end])

		if !sendAndWaitACK(conn, clientAddr, packet, block, buf) {
			log.Printf("transfer %q to %s aborted at block %d", filename, clientAddr, block)
			return
		}

		if end-offset < blockSize {
			log.Printf("sent %q to %s: %d bytes, %d blocks", filename, clientAddr, len(data), block)
			return
		}
		block++
	}
}

func sendAndWaitACK(conn *net.UDPConn, clientAddr *net.UDPAddr, packet []byte, block uint16, buf []byte) bool {
	for attemps := 1; attemps <= maxRetries; attemps++ {
		if attemps > 1 {
			log.Printf("timeout block %d, retransmit (attemp %d)", block, attemps)
		}

		if _, err := conn.WriteToUDP(packet, clientAddr); err != nil {
			log.Printf("write: %v", err)
			return false
		}

		conn.SetReadDeadline(time.Now().Add(timeout))
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}

			if err != nil {
				log.Printf("read: %v", err)
				return false
			}

			if !from.IP.Equal(clientAddr.IP) || from.Port != clientAddr.Port {
				sendError(conn, from, errUnknownTID, "unknow transfer ID")
				continue
			}

			if n >= 2 && binary.BigEndian.Uint16(buf[0:2]) == opERROR {
				log.Printf("client %s sent Error, abort", from)
				return false
			}

			ack, err := parseACK(buf[:n])
			if err != nil || ack != block {
				continue
			}

			return true
		}
	}

	return false
}

func parseRRQ(p []byte) (string, error) {
	if len(p) < 4 {
		return "", errors.New("packet too short")
	}

	if op := binary.BigEndian.Uint16(p[0:2]); op != opRRQ {
		return "", errors.New("only RRQ supported")
	}

	parts := bytes.Split(p[2:], []byte{0})
	if len(parts) < 2 || len(parts[0]) == 0 {
		return "", errors.New("malformed RRQ")
	}

	if mode := string(bytes.ToLower(parts[1])); mode != "octet" {
		return "", errors.New("only octet mode supported")
	}

	return string(parts[0]), nil
}

func buildData(block uint16, data []byte) []byte {
	p := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(p[0:2], opDATA)
	binary.BigEndian.PutUint16(p[2:4], block)
	copy(p[4:], data)
	return p
}

func parseACK(buf []byte) (uint16, error) {
	if len(buf) < 4 || binary.BigEndian.Uint16(buf[0:2]) != opACK {
		return 0, errors.New("not an ACK")
	}

	return binary.BigEndian.Uint16(buf[2:4]), nil
}

func sendError(conn *net.UDPConn, to *net.UDPAddr, code uint16, msg string) {
	p := make([]byte, 4, 5+len(msg))
	binary.BigEndian.PutUint16(p[0:2], opERROR)
	binary.BigEndian.PutUint16(p[2:4], code)
	p = append(p, msg...)
	p = append(p, 0)
	conn.WriteToUDP(p, to)
}
