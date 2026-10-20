package network

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ProtonMail/go-crypto/ocb"
)

// Datagram: [8B nonce, big-endian][ciphertext][16B OCB tag], same as mosh.
// Full OCB nonce is 12 bytes: 4 zero bytes + the 8 sent on the wire.
const (
	nonceLen = 12
	tagLen   = 16
	wireLen  = 8

	Overhead = wireLen + tagLen
)

var ErrAuth = errors.New("packet failed integrity check")

type Session struct {
	aead   cipher.AEAD
	blocks uint64
}

func NewSession(printableKey string) (*Session, error) {
	key, err := base64.RawStdEncoding.Strict().DecodeString(printableKey)
	if err != nil || len(key) != 16 {
		return nil, fmt.Errorf("key must be 22 chars of base64 encoding 16 bytes")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aead, err := ocb.NewOCBWithNonceAndTagSize(block, nonceLen, tagLen)
	if err != nil {
		return nil, err
	}

	return &Session{
		aead: aead,
	}, nil
}

func (s *Session) Encrypt(nonce uint64, pt []byte) []byte {
	// OCB degrades past 2^48 blocks; both sides share the key, so each stops at 2^47.
	s.blocks += uint64(len(pt)+15) / 16
	if s.blocks>>47 != 0 {
		panic("network: encrypted 2^47 blocks with one key")
	}

	var n [nonceLen]byte
	binary.BigEndian.PutUint64(n[4:], nonce)
	return s.aead.Seal(n[4:], n[:], pt, nil)
}

func (s *Session) Decrypt(b []byte) (nonce uint64, pt []byte, err error) {
	if len(b) < Overhead {
		return 0, nil, ErrAuth
	}

	var n [nonceLen]byte
	copy(n[4:], b[:wireLen])
	pt, err = s.aead.Open(nil, n[:], b[wireLen:], nil)
	if err != nil {
		return 0, nil, ErrAuth
	}

	return binary.BigEndian.Uint64(n[4:]), pt, nil
}
