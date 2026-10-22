package network

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"

	"github.com/findardi/system-engineer-road/go-mosh/internal/pb/transportpb"
	"google.golang.org/protobuf/proto"
)

// One Instruction is protobuf-encoded, zlib-compressed, then split into
// fragments that each fit one datagram, same as mosh (src/network/transportfragment.cc).
// Fragment header: [8B instruction id][2B final<<15 | fragment number], big-endian.
const (
	fragHeaderLen = 10
	maxInflated   = 2048 * 2048 // mosh's decompression buffer: caps a zlib bomb
)

var errFragment = errors.New("malformed fragment")

type fragment struct {
	id       uint64
	num      uint16
	final    bool
	contents []byte
}

func (f fragment) marshal() []byte {
	b := make([]byte, fragHeaderLen, fragHeaderLen+len(f.contents))
	binary.BigEndian.PutUint64(b, f.id)
	n := f.num
	if f.final {
		n |= 0x8000
	}
	binary.BigEndian.PutUint16(b[8:], n)
	return append(b, f.contents...)
}

func parseFragment(b []byte) (fragment, error) {
	if len(b) < fragHeaderLen {
		return fragment{}, errFragment
	}
	n := binary.BigEndian.Uint16(b[8:])
	return fragment{
		id:       binary.BigEndian.Uint64(b),
		num:      n & 0x7FFF,
		final:    n&0x8000 != 0,
		contents: b[fragHeaderLen:],
	}, nil
}

type fragmenter struct {
	nextID  uint64
	last    *transportpb.Instruction
	lastMTU int
}

// split gives a new id whenever the instruction header changes, so the
// receiver never mixes fragments of two different instructions.
func (f *fragmenter) split(inst *transportpb.Instruction, mtu int) ([]fragment, error) {
	mtu -= fragHeaderLen
	l := f.last
	if l == nil || inst.GetOldNum() != l.GetOldNum() || inst.GetNewNum() != l.GetNewNum() ||
		inst.GetAckNum() != l.GetAckNum() || inst.GetThrowawayNum() != l.GetThrowawayNum() ||
		!bytes.Equal(inst.GetChaff(), l.GetChaff()) || inst.GetProtocolVersion() != l.GetProtocolVersion() ||
		mtu != f.lastMTU {
		f.nextID++
	}
	f.last, f.lastMTU = inst, mtu

	raw, err := proto.Marshal(inst)
	if err != nil {
		return nil, err
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(raw)
	zw.Close()
	payload := z.Bytes()

	var frags []fragment
	for num := uint16(0); len(payload) > 0; num++ {
		n := min(len(payload), mtu)
		frags = append(frags, fragment{id: f.nextID, num: num, final: n == len(payload), contents: payload[:n]})
		payload = payload[n:]
	}
	return frags, nil
}

// assembly collects the fragments of the newest instruction id; fragments of
// an older id are discarded as soon as a new id shows up.
type assembly struct {
	id    uint64
	parts [][]byte // nil = not arrived yet
	got   int
	total int // -1 until the final fragment arrives
}

// add returns the decoded instruction once all fragments are in, else nil.
func (a *assembly) add(f fragment) (*transportpb.Instruction, error) {
	if a.parts == nil || f.id != a.id {
		*a = assembly{id: f.id, total: -1}
	}
	if int(f.num) >= len(a.parts) {
		a.parts = append(a.parts, make([][]byte, int(f.num)+1-len(a.parts))...)
	}
	if a.parts[f.num] == nil {
		a.parts[f.num] = bytes.Clone(f.contents)
		a.got++
	}
	if f.final {
		a.total = int(f.num) + 1
		a.parts = a.parts[:a.total]
	}
	if a.got != a.total {
		return nil, nil
	}

	zr, err := zlib.NewReader(bytes.NewReader(bytes.Join(a.parts, nil)))
	*a = assembly{}
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxInflated))
	if err != nil {
		return nil, err
	}
	inst := &transportpb.Instruction{}
	return inst, proto.Unmarshal(raw, inst)
}
