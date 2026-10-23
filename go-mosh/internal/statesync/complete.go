package statesync

import (
	"github.com/findardi/system-engineer-road/go-mosh/internal/pb/hostpb"
	"github.com/findardi/system-engineer-road/go-mosh/internal/terminal"
	"google.golang.org/protobuf/proto"
)

// diffDisplay renders SSP diffs; it may assume a capable terminal because
// the receiver applies the diff to its own emulator, not to a real screen.
var diffDisplay = terminal.NewDisplay()

// Complete is the server-to-client state: the whole terminal, as mosh's
// Terminal::Complete (src/statesync/completeterminal.cc). Its diff is a
// HostBuffers.HostMessage holding the escape sequences that redraw the
// screen, so its size is bounded by the window, not by the program's output.
type Complete struct {
	emu     *terminal.Emulator
	echoAck uint64 // newest user input the screen reflects; used by prediction
}

func NewComplete(width, height int) *Complete {
	return &Complete{emu: terminal.NewEmulator(width, height)}
}

// Act feeds program output to the terminal and returns its replies to the
// program, e.g. a cursor position report, which must go back to the PTY.
func (c *Complete) Act(output []byte) []byte { return c.emu.Write(output) }

func (c *Complete) Emulator() *terminal.Emulator { return c.emu }

func (c *Complete) Framebuffer() *terminal.Framebuffer { return c.emu.Framebuffer() }

func (c *Complete) EchoAck() uint64 { return c.echoAck }

func (c *Complete) Clone() *Complete { return &Complete{c.emu.Clone(), c.echoAck} }

func (c *Complete) Equal(o *Complete) bool {
	return c.echoAck == o.echoAck && c.Framebuffer().Equal(o.Framebuffer())
}

// Subtract does nothing: a screen has no history to trim.
func (c *Complete) Subtract(*Complete) {}

func (c *Complete) DiffFrom(existing *Complete) []byte {
	var msg hostpb.HostMessage
	add := func(set func(*hostpb.Instruction)) {
		ins := &hostpb.Instruction{}
		set(ins)
		msg.Instruction = append(msg.Instruction, ins)
	}
	if existing.echoAck != c.echoAck {
		add(func(ins *hostpb.Instruction) {
			proto.SetExtension(ins, hostpb.E_Echoack, &hostpb.EchoAck{EchoAckNum: proto.Uint64(c.echoAck)})
		})
	}
	old, cur := existing.Framebuffer(), c.Framebuffer()
	if !old.Equal(cur) {
		if old.DS.Width() != cur.DS.Width() || old.DS.Height() != cur.DS.Height() {
			add(func(ins *hostpb.Instruction) {
				proto.SetExtension(ins, hostpb.E_Resize, &hostpb.ResizeMessage{
					Width: proto.Int32(int32(cur.DS.Width())), Height: proto.Int32(int32(cur.DS.Height()))})
			})
		}
		if update := diffDisplay.NewFrame(true, old, cur); update != "" {
			add(func(ins *hostpb.Instruction) {
				proto.SetExtension(ins, hostpb.E_Hostbytes, &hostpb.HostBytes{Hoststring: []byte(update)})
			})
		}
	}
	if len(msg.Instruction) == 0 {
		return nil
	}
	b, _ := proto.Marshal(&msg)
	return b
}

func (c *Complete) Apply(diff []byte) error {
	var msg hostpb.HostMessage
	if err := proto.Unmarshal(diff, &msg); err != nil {
		return err
	}
	for _, ins := range msg.Instruction {
		if hb := proto.GetExtension(ins, hostpb.E_Hostbytes).(*hostpb.HostBytes); hb != nil {
			c.emu.Write(hb.Hoststring) // the server never interrogates the client's terminal
		} else if r := proto.GetExtension(ins, hostpb.E_Resize).(*hostpb.ResizeMessage); r != nil {
			c.emu.Resize(int(r.GetWidth()), int(r.GetHeight()))
		} else if e := proto.GetExtension(ins, hostpb.E_Echoack).(*hostpb.EchoAck); e != nil {
			c.echoAck = e.GetEchoAckNum()
		}
	}
	return nil
}
