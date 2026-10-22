package network

import (
	"crypto/rand"
	"errors"
	"math"
	"time"

	"github.com/findardi/system-engineer-road/go-mosh/internal/pb/transportpb"
	"google.golang.org/protobuf/proto"
)

// State Synchronization Protocol (SSP), ported from mosh's TransportSender
// and Transport (src/network/transportsender-impl.h, networktransport-impl.h).
//
// Each side owns one state object and the peer keeps a copy. Instead of a byte
// stream, the sender ships "diff from state #old to state #new"; the receiver
// acks the newest state it holds. Lost packets are never retransmitted as-is:
// the next diff simply starts from the newest state the receiver acked.

// State is what SSP synchronizes. T is the pointer type itself, e.g. *UserStream.
type State[T any] interface {
	Clone() T
	Equal(T) bool
	DiffFrom(existing T) []byte // diff that turns existing into this state
	Apply(diff []byte) error
	Subtract(prefix T) // drop history both sides already agree on
}

const protocolVersion = 2 // mosh's MOSH_PROTOCOL_VERSION

// Timing, in milliseconds, same as mosh.
const (
	sendIntervalMin    = 20    // fastest frame rate
	sendIntervalMax    = 250   // slowest frame rate
	ackInterval        = 3000  // empty ack as keepalive
	ackDelay           = 100   // delayed ack after receiving data
	activeRetryTimeout = 10000 // stop resending if peer silent this long
	never              = math.MaxInt64
	shutdownRetries    = 16
	shutdownNum        = math.MaxUint64
)

var errProtocolVersion = errors.New("peer speaks a different protocol version")

type timestamped[T any] struct {
	ms    int64 // when sent (sender) or received (receiver)
	num   uint64
	state T
}

// Transport is not safe for concurrent use; run it from the Conn's event loop.
type Transport[M State[M], R State[R]] struct {
	conn *Conn

	// sender
	current       M
	sent          []timestamped[M] // [0] acked by receiver, last = last sent
	assumed       int              // index into sent: what the receiver probably has
	frag          fragmenter
	nextAckTime   int64
	nextSendTime  int64
	ackNum        uint64 // newest remote state we hold, sent back as ack
	pendingAck    bool   // received data: ack soon
	lastHeard     int64  // last time a new remote state arrived
	mindelayClock int64  // first unsent change to current, or never
	shutdown      bool
	shutdownTries int
	shutdownStart int64

	// SendDelay batches changes for this long before sending (mosh: 8ms server, 1ms client).
	SendDelay int64

	// receiver
	received     []timestamped[R] // sorted by num
	quenchUntil  int64
	lastReceiver R // remote state as of the last RemoteDiff call
	asm          assembly
}

func NewTransport[M State[M], R State[R]](conn *Conn, initial M, initialRemote R) *Transport[M, R] {
	now := conn.ms()
	return &Transport[M, R]{
		conn:          conn,
		current:       initial.Clone(),
		sent:          []timestamped[M]{{now, 0, initial.Clone()}},
		nextAckTime:   now,
		nextSendTime:  now,
		mindelayClock: never,
		SendDelay:     8,
		received:      []timestamped[R]{{now, 0, initialRemote.Clone()}},
		lastReceiver:  initialRemote.Clone(),
	}
}

// Current is the local state; mutate it and the next Tick ships the change.
func (t *Transport[M, R]) Current() M { return t.current }

// Remote is the newest state received from the peer.
func (t *Transport[M, R]) Remote() R { return t.received[len(t.received)-1].state }

// RemoteDiff returns what changed in the remote state since the previous call.
func (t *Transport[M, R]) RemoteDiff() []byte {
	diff := t.Remote().DiffFrom(t.lastReceiver)
	// Reverse order: the oldest state subtracts itself last.
	oldest := t.received[0].state
	for i := len(t.received) - 1; i >= 0; i-- {
		t.received[i].state.Subtract(oldest)
	}
	t.lastReceiver = t.Remote().Clone()
	return diff
}

// sendInterval aims for two frames per round trip.
func (t *Transport[M, R]) sendInterval() int64 {
	return min(max(int64(math.Ceil(t.conn.srtt/2)), sendIntervalMin), sendIntervalMax)
}

func (t *Transport[M, R]) timeout() int64 { return t.conn.Timeout().Milliseconds() }

func (t *Transport[M, R]) calculateTimers() {
	now := t.conn.ms()
	t.updateAssumedReceiverState()
	t.rationalizeStates()

	if t.pendingAck && t.nextAckTime > now+ackDelay {
		t.nextAckTime = now + ackDelay
	}

	last := t.sent[len(t.sent)-1]
	switch {
	case !t.current.Equal(last.state): // unsent changes
		if t.mindelayClock == never {
			t.mindelayClock = now
		}
		t.nextSendTime = max(t.mindelayClock+t.SendDelay, last.ms+t.sendInterval())
	case !t.current.Equal(t.sent[t.assumed].state) && t.lastHeard+activeRetryTimeout > now: // probably lost
		t.nextSendTime = last.ms + t.sendInterval()
		if t.mindelayClock != never {
			t.nextSendTime = max(t.nextSendTime, t.mindelayClock+t.SendDelay)
		}
	case !t.current.Equal(t.sent[0].state) && t.lastHeard+activeRetryTimeout > now: // unacked
		t.nextSendTime = last.ms + t.timeout() + ackDelay
	default:
		t.nextSendTime = never
	}

	if t.shutdown || t.ackNum == shutdownNum {
		t.nextAckTime = t.sent[len(t.sent)-1].ms + t.sendInterval()
	}
}

// WaitTime is how long the event loop may sleep before calling Tick again.
func (t *Transport[M, R]) WaitTime() time.Duration {
	t.calculateTimers()
	if !t.conn.remote.IsValid() {
		return time.Hour
	}
	wake := min(t.nextAckTime, t.nextSendTime)
	return time.Duration(max(wake-t.conn.ms(), 0)) * time.Millisecond
}

// Tick sends a diff or an empty ack when one is due.
func (t *Transport[M, R]) Tick() error {
	t.calculateTimers()
	if !t.conn.remote.IsValid() {
		return nil
	}
	now := t.conn.ms()
	if now < t.nextAckTime && now < t.nextSendTime {
		return nil
	}

	diff := t.current.DiffFrom(t.sent[t.assumed].state)
	diff = t.prospectiveResend(diff)

	var err error
	if len(diff) == 0 {
		if now >= t.nextAckTime {
			err = t.sendEmptyAck()
		}
		if now >= t.nextSendTime {
			t.nextSendTime = never
		}
	} else {
		err = t.sendToReceiver(diff)
	}
	t.mindelayClock = never
	return err
}

func (t *Transport[M, R]) sendEmptyAck() error {
	now := t.conn.ms()
	newNum := t.sent[len(t.sent)-1].num + 1
	if t.shutdown {
		newNum = shutdownNum
	}
	t.addSentState(now, newNum, t.current.Clone())
	err := t.sendInFragments(nil, newNum)
	t.nextAckTime = now + ackInterval
	t.nextSendTime = never
	return err
}

func (t *Transport[M, R]) sendToReceiver(diff []byte) error {
	now := t.conn.ms()
	last := &t.sent[len(t.sent)-1]
	newNum := last.num
	if !t.current.Equal(last.state) {
		newNum++
	}
	if t.shutdown {
		newNum = shutdownNum
	}
	if newNum == last.num {
		last.ms = now
	} else {
		t.addSentState(now, newNum, t.current.Clone())
	}
	err := t.sendInFragments(diff, newNum)
	t.assumed = len(t.sent) - 1
	t.nextAckTime = now + ackInterval
	t.nextSendTime = never
	return err
}

// addSentState caps the queue at 32 by dropping from the middle, keeping the
// acked state and the most recent ones.
func (t *Transport[M, R]) addSentState(ms int64, num uint64, s M) {
	t.sent = append(t.sent, timestamped[M]{ms, num, s})
	if len(t.sent) > 32 {
		i := len(t.sent) - 16
		t.sent = append(t.sent[:i], t.sent[i+1:]...)
		if t.assumed >= i {
			t.assumed--
		}
	}
}

// updateAssumedReceiverState gives recently sent, unacked states the benefit
// of the doubt: they are probably in flight, not lost.
func (t *Transport[M, R]) updateAssumedReceiverState() {
	now := t.conn.ms()
	t.assumed = 0
	for i := 1; i < len(t.sent); i++ {
		if now-t.sent[i].ms >= t.timeout()+ackDelay {
			return
		}
		t.assumed = i
	}
}

func (t *Transport[M, R]) rationalizeStates() {
	known := t.sent[0].state
	t.current.Subtract(known)
	// Reverse order: the known state subtracts itself last.
	for i := len(t.sent) - 1; i >= 0; i-- {
		t.sent[i].state.Subtract(known)
	}
}

// prospectiveResend diffs against the acked state instead, when that costs
// little extra: it survives the loss of every packet still in flight.
func (t *Transport[M, R]) prospectiveResend(diff []byte) []byte {
	if t.assumed == 0 {
		return diff
	}
	resend := t.current.DiffFrom(t.sent[0].state)
	if len(resend) <= len(diff) || (len(resend) < 1000 && len(resend)-len(diff) < 100) {
		t.assumed = 0
		return resend
	}
	return diff
}

func (t *Transport[M, R]) sendInFragments(diff []byte, newNum uint64) error {
	chaff := make([]byte, randByte()%17) // 0..16 random bytes hide the true length
	rand.Read(chaff)
	inst := &transportpb.Instruction{
		ProtocolVersion: proto.Uint32(protocolVersion),
		OldNum:          proto.Uint64(t.sent[t.assumed].num),
		NewNum:          proto.Uint64(newNum),
		AckNum:          proto.Uint64(t.ackNum),
		ThrowawayNum:    proto.Uint64(t.sent[0].num),
		Diff:            diff,
		Chaff:           chaff,
	}
	frags, err := t.frag.split(inst, sendMTU-packetOverhead)
	if err != nil {
		return err
	}
	t.pendingAck = false
	if newNum == shutdownNum {
		t.shutdownTries++
	}
	for _, f := range frags {
		if serr := t.conn.Send(f.marshal()); serr != nil {
			err = serr // keep going: later fragments may still get through
		}
	}
	return err
}

func randByte() byte {
	var b [1]byte
	rand.Read(b[:])
	return b[0]
}

// processAck drops sent states the receiver no longer needs. An ack for a
// state we already culled is ignored.
func (t *Transport[M, R]) processAck(ack uint64) {
	for _, s := range t.sent {
		if s.num == ack {
			kept := t.sent[:0]
			for _, s := range t.sent {
				if s.num >= ack {
					kept = append(kept, s)
				}
			}
			t.sent = kept
			t.assumed = 0
			return
		}
	}
}

// Receive feeds one datagram from conn.Incoming() into the transport.
// Errors mean the datagram was dropped; the caller just carries on.
func (t *Transport[M, R]) Receive(d Datagram) error {
	payload, err := t.conn.Receive(d)
	if err != nil {
		return err
	}
	f, err := parseFragment(payload)
	if err != nil {
		return err
	}
	inst, err := t.asm.add(f)
	if inst == nil || err != nil {
		return err
	}
	if inst.GetProtocolVersion() != protocolVersion {
		return errProtocolVersion
	}

	t.processAck(inst.GetAckNum())
	// End-to-end round trip proven: tells Conn not to hop.
	t.conn.lastRoundTrip = t.conn.at(t.sent[0].ms)

	newNum, oldNum := inst.GetNewNum(), inst.GetOldNum()
	ref := -1
	for i, s := range t.received {
		if s.num == newNum {
			return nil // duplicate
		}
		if s.num == oldNum {
			ref = i
		}
	}
	// Without the reference state the diff cannot be applied. This also
	// enforces idempotency, so it is security-sensitive.
	if ref < 0 {
		return nil
	}
	refState := t.received[ref].state

	t.processThrowaway(inst.GetThrowawayNum())

	// A malicious or very long one-way sender could grow this forever.
	if len(t.received) > 1024 {
		now := t.conn.ms()
		if now < t.quenchUntil {
			return nil
		}
		t.quenchUntil = now + 15000
	}

	next := timestamped[R]{t.conn.ms(), newNum, refState.Clone()}
	if diff := inst.GetDiff(); len(diff) > 0 {
		if err := next.state.Apply(diff); err != nil {
			return err
		}
	}

	for i, s := range t.received {
		if s.num > newNum { // arrived out of order: keep sorted, do not ack it yet
			t.received = append(t.received[:i], append([]timestamped[R]{next}, t.received[i:]...)...)
			return nil
		}
	}
	t.received = append(t.received, next)
	t.ackNum = newNum
	t.lastHeard = next.ms
	if len(inst.GetDiff()) > 0 {
		t.pendingAck = true
	}
	return nil
}

func (t *Transport[M, R]) StartShutdown() {
	if !t.shutdown {
		t.shutdown, t.shutdownStart = true, t.conn.ms()
	}
}

func (t *Transport[M, R]) ShutdownInProgress() bool { return t.shutdown }

func (t *Transport[M, R]) ShutdownAcknowledged() bool { return t.sent[0].num == shutdownNum }

// ShutdownAckTimedOut: give up waiting for the peer, e.g. it is gone.
func (t *Transport[M, R]) ShutdownAckTimedOut() bool {
	return t.shutdown && (t.shutdownTries >= shutdownRetries || t.conn.ms()-t.shutdownStart >= activeRetryTimeout)
}

func (t *Transport[M, R]) CounterpartyShutdownAckSent() bool {
	return t.frag.last != nil && t.frag.last.GetAckNum() == shutdownNum
}

// processThrowaway drops received states older than what the sender will
// ever diff from again.
func (t *Transport[M, R]) processThrowaway(num uint64) {
	kept := t.received[:0]
	for _, s := range t.received {
		if s.num >= num {
			kept = append(kept, s)
		}
	}
	t.received = kept
}
