package statesync

import (
	"time"

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
	echoAck uint64       // newest user input the screen reflects; used by prediction
	inputs  []inputFrame // server only: input frames waiting to be echo-acked
}

type inputFrame struct {
	num uint64
	at  time.Time
}

// echoTimeout gives the program time to echo input before the server
// declares it echoed (or not).
const echoTimeout = 50 * time.Millisecond

func NewComplete(width, height int) *Complete {
	return &Complete{emu: terminal.NewEmulator(width, height)}
}

// Act feeds program output to the terminal and returns its replies to the
// program, e.g. a cursor position report, which must go back to the PTY.
func (c *Complete) Act(output []byte) []byte { return c.emu.Write(output) }

func (c *Complete) Emulator() *terminal.Emulator { return c.emu }

func (c *Complete) Framebuffer() *terminal.Framebuffer { return c.emu.Framebuffer() }

func (c *Complete) EchoAck() uint64 { return c.echoAck }

func (c *Complete) Clone() *Complete { return &Complete{emu: c.emu.Clone(), echoAck: c.echoAck} }

// RegisterInputFrame notes that the user input of remote state n reached
// the program at now.
func (c *Complete) RegisterInputFrame(n uint64, now time.Time) {
	c.inputs = append(c.inputs, inputFrame{n, now})
}

// SetEchoAck acks every input frame older than echoTimeout: the screen now
// shows whatever echo the program was going to give. The client judges its
// predictions against this. It reports whether the ack changed.
func (c *Complete) SetEchoAck(now time.Time) bool {
	var newest uint64
	for _, f := range c.inputs {
		if !f.at.After(now.Add(-echoTimeout)) {
			newest = f.num
		}
	}
	kept := c.inputs[:0]
	for _, f := range c.inputs {
		if f.num >= newest {
			kept = append(kept, f)
		}
	}
	c.inputs = kept
	changed := c.echoAck != newest
	c.echoAck = newest
	return changed
}

// EchoWait is how long until the next input frame can be echo-acked. The
// first entry is the one already acked.
func (c *Complete) EchoWait(now time.Time) time.Duration {
	if len(c.inputs) < 2 {
		return time.Hour
	}
	return max(c.inputs[1].at.Add(echoTimeout).Sub(now), 0)
}

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
