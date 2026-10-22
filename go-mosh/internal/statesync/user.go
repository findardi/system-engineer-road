package statesync

import (
	"slices"

	"github.com/findardi/system-engineer-road/go-mosh/internal/pb/userpb"
	"google.golang.org/protobuf/proto"
)

// UserEvent is one keystroke byte or one window resize.
type UserEvent struct {
	Resize        bool
	Byte          byte
	Width, Height int32
}

// UserStream is the client-to-server state: an append-only log of user input,
// same as mosh's UserStream (src/statesync/user.cc). The wire diff is a
// ClientBuffers.UserMessage.
type UserStream struct {
	events []UserEvent
}

func (u *UserStream) PushBytes(b []byte) {
	for _, c := range b {
		u.events = append(u.events, UserEvent{Byte: c})
	}
}

func (u *UserStream) PushResize(width, height int32) {
	u.events = append(u.events, UserEvent{Resize: true, Width: width, Height: height})
}

func (u *UserStream) Events() []UserEvent { return u.events }

func (u *UserStream) Clone() *UserStream { return &UserStream{slices.Clone(u.events)} }

func (u *UserStream) Equal(o *UserStream) bool { return slices.Equal(u.events, o.events) }

// Subtract drops the prefix both sides already have. The log is append-only,
// so the prefix is always the first len(prefix) events.
func (u *UserStream) Subtract(prefix *UserStream) {
	if u == prefix {
		u.events = nil
		return
	}
	u.events = u.events[min(len(prefix.events), len(u.events)):]
}

// DiffFrom encodes the events after existing, merging consecutive bytes into
// one Keystroke instruction like mosh does.
func (u *UserStream) DiffFrom(existing *UserStream) []byte {
	var msg userpb.UserMessage
	var keys *userpb.Keystroke
	for _, e := range u.events[min(len(existing.events), len(u.events)):] {
		ins := &userpb.Instruction{}
		if e.Resize {
			keys = nil
			proto.SetExtension(ins, userpb.E_Resize, &userpb.ResizeMessage{Width: proto.Int32(e.Width), Height: proto.Int32(e.Height)})
		} else if keys != nil {
			keys.Keys = append(keys.Keys, e.Byte)
			continue
		} else {
			keys = &userpb.Keystroke{Keys: []byte{e.Byte}}
			proto.SetExtension(ins, userpb.E_Keystroke, keys)
		}
		msg.Instruction = append(msg.Instruction, ins)
	}
	if len(msg.Instruction) == 0 {
		return nil
	}
	b, _ := proto.Marshal(&msg)
	return b
}

func (u *UserStream) Apply(diff []byte) error {
	var msg userpb.UserMessage
	if err := proto.Unmarshal(diff, &msg); err != nil {
		return err
	}
	for _, ins := range msg.Instruction {
		if k := proto.GetExtension(ins, userpb.E_Keystroke).(*userpb.Keystroke); k != nil {
			u.PushBytes(k.Keys)
		} else if r := proto.GetExtension(ins, userpb.E_Resize).(*userpb.ResizeMessage); r != nil {
			u.PushResize(r.GetWidth(), r.GetHeight())
		}
	}
	return nil
}
