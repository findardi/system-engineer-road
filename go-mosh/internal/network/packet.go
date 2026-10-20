package network

import (
	"encoding/binary"
	"errors"
)

// Direction lives in nonce bit 63, so client and server never reuse a nonce
// under their shared key.
type Direction uint8

const (
	ToServer Direction = iota
	ToClient
)

const directionBit = uint64(1) << 63

const TimestampNone uint16 = 0xFFFF

// Plaintext: [2B timestamp][2B timestamp_reply][payload], same as mosh.
type Packet struct {
	Seq            uint64
	Dir            Direction
	Timestamp      uint16
	TimestampReply uint16
	Payload        []byte
}

var errShort = errors.New("packet too short for timestamps")

func (p Packet) Seal(s *Session) []byte {
	nonce := p.Seq &^ directionBit
	if p.Dir == ToClient {
		nonce |= directionBit
	}

	pt := make([]byte, 4, 4+len(p.Payload))
	binary.BigEndian.PutUint16(pt[0:], p.Timestamp)
	binary.BigEndian.PutUint16(pt[2:], p.TimestampReply)
	return s.Encrypt(nonce, append(pt, p.Payload...))
}

func Open(s *Session, b []byte) (Packet, error) {
	nonce, pt, err := s.Decrypt(b)
	if err != nil {
		return Packet{}, err
	}

	if len(pt) < 4 {
		return Packet{}, errShort
	}

	p := Packet{
		Seq:            nonce &^ directionBit,
		Timestamp:      binary.BigEndian.Uint16(pt[0:]),
		TimestampReply: binary.BigEndian.Uint16(pt[2:]),
		Payload:        pt[4:],
	}

	if nonce&directionBit != 0 {
		p.Dir = ToClient
	}

	return p, nil
}
