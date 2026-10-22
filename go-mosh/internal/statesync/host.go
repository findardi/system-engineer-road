package statesync

import (
	"bytes"

	"github.com/findardi/system-engineer-road/go-mosh/internal/pb/hostpb"
	"google.golang.org/protobuf/proto"
)

// HostStream is the server-to-client state for now: an append-only log of raw
// terminal output, sent as HostBuffers.HostMessage so mosh-client renders it too.
//
// ponytail: unacked output is resent whole; phase 6 replaces this with a
// screen diff whose size is bounded by the window, not by the output volume.
type HostStream struct {
	data []byte
}

func (h *HostStream) PushBytes(b []byte) { h.data = append(h.data, b...) }

func (h *HostStream) Bytes() []byte { return h.data }

func (h *HostStream) Clone() *HostStream { return &HostStream{bytes.Clone(h.data)} }

func (h *HostStream) Equal(o *HostStream) bool { return bytes.Equal(h.data, o.data) }

func (h *HostStream) Subtract(prefix *HostStream) {
	if h == prefix {
		h.data = nil
		return
	}

	h.data = h.data[min(len(prefix.data), len(h.data)):]
}

func (h *HostStream) DiffFrom(existing *HostStream) []byte {
	added := h.data[min(len(existing.data), len(h.data)):]
	if len(added) == 0 {
		return nil
	}
	ins := &hostpb.Instruction{}
	proto.SetExtension(ins, hostpb.E_Hostbytes, &hostpb.HostBytes{Hoststring: added})
	b, _ := proto.Marshal(&hostpb.HostMessage{Instruction: []*hostpb.Instruction{ins}})
	return b
}

func (h *HostStream) Apply(diff []byte) error {
	var msg hostpb.HostMessage
	if err := proto.Unmarshal(diff, &msg); err != nil {
		return err
	}

	for _, ins := range msg.Instruction {
		if hb := proto.GetExtension(ins, hostpb.E_Hostbytes).(*hostpb.HostBytes); hb != nil {
			h.PushBytes(hb.Hoststring)
		}
	}

	return nil
}
