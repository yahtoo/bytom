package p2p

import (
	"fmt"
	"sync"
	"testing"

	cfg "github.com/bytom/config"
	"github.com/bytom/errors"
	conn "github.com/bytom/p2p/connection"
	"github.com/bytom/version"
)

var (
	testCfg *cfg.Config
)

func init() {
	testCfg = cfg.DefaultConfig()
}

/*
Each peer has one `MConnection` (multiplex connection) instance.

__multiplex__ *noun* a system or signal involving simultaneous transmission of
several messages along a single channel of communication.

Each `MConnection` handles message transmission on multiple abstract communication
`Channel`s.  Each channel has a globally unique byte id.
The byte id and the relative priorities of each `Channel` are configured upon
initialization of the connection.

There are two methods for sending messages:
	func (m MConnection) Send(chID byte, msgBytes []byte) bool {}
	func (m MConnection) TrySend(chID byte, msgBytes []byte}) bool {}

`Send(chID, msgBytes)` is a blocking call that waits until `msg` is
successfully queued for the channel with the given id byte `chID`, or until the
request times out.  The message `msg` is serialized using Go-Amino.

`TrySend(chID, msgBytes)` is a nonblocking call that returns false if the
channel's queue is full.

Inbound message bytes are handled with an onReceive callback function.
*/
type PeerMessage struct {
	PeerID  string
	Bytes   []byte
	Counter int
}

type TestReactor struct {
	BaseReactor

	mtx          sync.Mutex
	channels     []*conn.ChannelDescriptor
	logMessages  bool
	msgsCounter  int
	msgsReceived map[byte][]PeerMessage
}

func NewTestReactor(channels []*conn.ChannelDescriptor, logMessages bool) *TestReactor {
	tr := &TestReactor{
		channels:     channels,
		logMessages:  logMessages,
		msgsReceived: make(map[byte][]PeerMessage),
	}
	tr.BaseReactor = *NewBaseReactor("TestReactor", tr)

	return tr
}

// GetChannels implements Reactor
func (tr *TestReactor) GetChannels() []*conn.ChannelDescriptor {
	return tr.channels
}

// OnStart implements BaseService
func (tr *TestReactor) OnStart() error {
	tr.BaseReactor.OnStart()
	return nil
}

// OnStop implements BaseService
func (tr *TestReactor) OnStop() {
	tr.BaseReactor.OnStop()
}

// AddPeer implements Reactor by sending our state to peer.
func (tr *TestReactor) AddPeer(peer *Peer) error {
	return nil
}

// RemovePeer implements Reactor by removing peer from the pool.
func (tr *TestReactor) RemovePeer(peer *Peer, reason interface{}) {
}

// Receive implements Reactor by handling 4 types of messages (look below).
func (tr *TestReactor) Receive(chID byte, peer *Peer, msgBytes []byte) {
	if tr.logMessages {
		tr.mtx.Lock()
		defer tr.mtx.Unlock()
		fmt.Println("Received: %X, %X\n", chID, msgBytes)
		tr.msgsReceived[chID] = append(tr.msgsReceived[chID], PeerMessage{peer.ID(), msgBytes, tr.msgsCounter})
		tr.msgsCounter++
	}
}

// convenience method for creating two switches connected to each other.
// XXX: note this uses net.Pipe and not a proper TCP conn
func MakeSwitchPair(t testing.TB, initSwitch func(int, *Switch) *Switch) (*Switch, *Switch) {
	// Create two switches that will be interconnected.
	var testCfg []*cfg.Config
	cfg1 := cfg.DefaultConfig()
	cfg1.P2P.ListenAddress = "127.0.0.1:40000"
	cfg1.P2P.SkipUPNP = true
	cfg1.BaseConfig.DBPath = "sw1"
	testCfg = append(testCfg, cfg1)
	cfg2 := cfg.DefaultConfig()
	cfg2.P2P.ListenAddress = "127.0.0.1:50000"
	cfg2.P2P.SkipUPNP = true
	cfg2.BaseConfig.DBPath = "sw2"
	testCfg = append(testCfg, cfg2)
	switches := MakeConnectedSwitches(testCfg, 2, initSwitch, Connect2Switches)
	return switches[0], switches[1]
}

func initSwitchFunc(i int, sw *Switch) *Switch {
	// Make two reactors of two channels each
	sw.AddReactor("foo", NewTestReactor([]*conn.ChannelDescriptor{
		{ID: byte(0x00), Priority: 10},
		{ID: byte(0x01), Priority: 10},
	}, true))
	sw.AddReactor("bar", NewTestReactor([]*conn.ChannelDescriptor{
		{ID: byte(0x02), Priority: 10},
		{ID: byte(0x03), Priority: 10},
	}, true))

	return sw
}

func TestSwitches(t *testing.T) {
	s1, s2 := MakeSwitchPair(t, initSwitchFunc)
	defer s1.Stop()
	defer s2.Stop()

	if s1.Peers().Size() != 1 {
		t.Errorf("Expected exactly 1 peer in s1, got %v", s1.Peers().Size())
	}
	if s2.Peers().Size() != 1 {
		t.Errorf("Expected exactly 1 peer in s2, got %v", s2.Peers().Size())
	}

	// Lets send some messages
	ch0Msg := []byte("channel zero")
	//ch1Msg := []byte("channel foo")
	//ch2Msg := []byte("channel bar")

	//s1.Broadcast(byte(0x00), ch0Msg)
	//s1.Broadcast(byte(0x01), ch1Msg)
	//s1.Broadcast(byte(0x02), ch2Msg)
	for i, peer := range s1.peers.list {
		fmt.Println(i, peer.ID())
		peer.TrySend(byte(0x00), ch0Msg)
	}

	//assertMsgReceivedWithTimeout(t, ch0Msg, byte(0x00), s2.Reactor("foo").(*TestReactor), 10*time.Millisecond, 5*time.Second)
	//assertMsgReceivedWithTimeout(t, ch1Msg, byte(0x01), s2.Reactor("foo").(*TestReactor), 10*time.Millisecond, 5*time.Second)
	//assertMsgReceivedWithTimeout(t, ch2Msg, byte(0x02), s2.Reactor("bar").(*TestReactor), 10*time.Millisecond, 5*time.Second)
}

func TestSwitchFiltersOutItself(t *testing.T) {
	s1 := MakeSwitch(testCfg, 1, "127.0.0.1", version.Version, initSwitchFunc)
	// simulate s1 having a public IP by creating a remote peer with the same ID
	rp := &remotePeer{PrivKey: s1.nodePrivKey, Config: testCfg}
	rp.Start()
	if err := s1.DialPeerWithAddress(rp.addr); errors.Root(err) != ErrConnectSelf {
		t.Fatal(err)
	}
}
