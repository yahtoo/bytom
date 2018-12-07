package p2p

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tendermint/go-crypto"
	cmn "github.com/tendermint/tmlibs/common"
	dbm "github.com/tendermint/tmlibs/db"

	cfg "github.com/bytom/config"
	"github.com/bytom/consensus"
	"github.com/bytom/errors"
	"github.com/bytom/p2p/connection"
	"github.com/bytom/p2p/discover"
	"github.com/bytom/p2p/trust"
	"github.com/bytom/protocol/bc"
	"github.com/bytom/protocol/bc/types"
	"github.com/bytom/version"
)

const (
	bannedPeerKey       = "BannedPeer"
	defaultBanDuration  = time.Hour * 1
	minNumOutboundPeers = 3
)

//pre-define errors for connecting fail
var (
	ErrDuplicatePeer     = errors.New("Duplicate peer")
	ErrConnectSelf       = errors.New("Connect self")
	ErrConnectBannedPeer = errors.New("Connect banned peer")
	ErrConnectSpvPeer    = errors.New("Outbound connect spv peer")
)

type Discv interface {
	ReadRandomNodes(buf []*discover.Node) (n int)
}

// Switch handles peer connections and exposes an API to receive incoming messages
// on `Reactors`.  Each `Reactor` is responsible for handling incoming messages of one
// or more `Channels`.  So while sending outgoing messages is typically performed on the peer,
// incoming messages are received on the reactor.
type Switch struct {
	cmn.BaseService

	Config       *cfg.Config
	peerConfig   *PeerConfig
	listeners    []Listener
	reactors     map[string]Reactor
	chDescs      []*connection.ChannelDescriptor
	reactorsByCh map[byte]Reactor
	peers        *PeerSet
	dialing      *cmn.CMap
	nodeInfo     *NodeInfo             // our node info
	nodePrivKey  crypto.PrivKeyEd25519 // our node privkey
	discv        Discv
	bannedPeer   map[string]time.Time
	db           dbm.DB
	mtx          sync.Mutex
	nodeInfoMu   sync.Mutex
}

func NewDiscover(config *cfg.Config, priv *crypto.PrivKeyEd25519, port uint16) (*discover.Network, error) {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort("0.0.0.0", strconv.FormatUint(uint64(port), 10)))
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	realaddr := conn.LocalAddr().(*net.UDPAddr)
	ntab, err := discover.ListenUDP(priv, conn, realaddr, path.Join(config.DBDir(), "discover.db"), nil)
	if err != nil {
		return nil, err
	}

	// add the seeds node to the discover table
	if config.P2P.Seeds == "" {
		return ntab, nil
	}
	nodes := []*discover.Node{}
	for _, seed := range strings.Split(config.P2P.Seeds, ",") {
		version.Status.AddSeed(seed)
		url := "enode://" + hex.EncodeToString(crypto.Sha256([]byte(seed))) + "@" + seed
		nodes = append(nodes, discover.MustParseNode(url))
	}
	if err = ntab.SetFallbackNodes(nodes); err != nil {
		return nil, err
	}
	return ntab, nil
}

// Defaults to tcp
func protocolAndAddress(listenAddr string) (string, string) {
	p, address := "tcp", listenAddr
	parts := strings.SplitN(address, "://", 2)
	if len(parts) == 2 {
		p, address = parts[0], parts[1]
	}
	return p, address
}

type InitDiscover func(config *cfg.Config, priv *crypto.PrivKeyEd25519, port uint16) (*discover.Network, error)

// NewSwitch creates a new Switch with the given config.
func NewSwitch(config *cfg.Config, genesisHash bc.Hash, bestBlockHeader types.BlockHeader, initDiscover InitDiscover) (*Switch, error) {
	// Create & add listener
	var l Listener
	var discv *discover.Network
	var listenAddr string

	privKey := crypto.GenPrivKeyEd25519()
	if !config.VaultMode {
		var listenerStatus bool
		p, address := protocolAndAddress(config.P2P.ListenAddress)
		l, listenerStatus = NewDefaultListener(p, address, config.P2P.SkipUPNP)
		var err error
		discv, err = initDiscover(config, &privKey, l.ExternalAddress().Port)
		if err != nil {
			return nil, err
		}

		// We assume that the rpcListener has the same ExternalAddress.
		// This is probably true because both P2P and RPC listeners use UPnP,
		// except of course if the rpc is only bound to localhost
		if listenerStatus {
			listenAddr = cmn.Fmt("%v:%v", l.ExternalAddress().IP.String(), l.ExternalAddress().Port)
		} else {
			listenAddr = cmn.Fmt("%v:%v", l.InternalAddress().IP.String(), l.InternalAddress().Port)
		}
	}

	sw := &Switch{
		Config:       config,
		peerConfig:   DefaultPeerConfig(config.P2P),
		reactors:     make(map[string]Reactor),
		chDescs:      make([]*connection.ChannelDescriptor, 0),
		reactorsByCh: make(map[byte]Reactor),
		peers:        NewPeerSet(),
		dialing:      cmn.NewCMap(),
		nodePrivKey:  privKey,
		discv:        discv,
		db:           dbm.NewDB("trusthistory", config.DBBackend, config.DBDir()),
	}
	sw.AddListener(l)
	sw.nodeInfo = newNodeInfo(config, privKey, genesisHash, bestBlockHeader, listenAddr)
	sw.BaseService = *cmn.NewBaseService(nil, "P2P Switch", sw)
	sw.bannedPeer = make(map[string]time.Time)
	if datajson := sw.db.Get([]byte(bannedPeerKey)); datajson != nil {
		if err := json.Unmarshal(datajson, &sw.bannedPeer); err != nil {
			return nil, err
		}
	}

	trust.Init()
	return sw, nil
}

// OnStart implements BaseService. It starts all the reactors, peers, and listeners.
func (sw *Switch) OnStart() error {
	for _, reactor := range sw.reactors {
		if _, err := reactor.Start(); err != nil {
			return err
		}
	}
	for _, listener := range sw.listeners {
		go sw.listenerRoutine(listener)
	}
	go sw.ensureOutboundPeersRoutine()
	return nil
}

// OnStop implements BaseService. It stops all listeners, peers, and reactors.
func (sw *Switch) OnStop() {
	for _, listener := range sw.listeners {
		listener.Stop()
	}
	sw.listeners = nil

	for _, peer := range sw.peers.List() {
		peer.Stop()
		sw.peers.Remove(peer)
	}

	for _, reactor := range sw.reactors {
		reactor.Stop()
	}
}

//AddBannedPeer add peer to blacklist
func (sw *Switch) AddBannedPeer(ip string) error {
	sw.mtx.Lock()
	defer sw.mtx.Unlock()

	sw.bannedPeer[ip] = time.Now().Add(defaultBanDuration)
	datajson, err := json.Marshal(sw.bannedPeer)
	if err != nil {
		return err
	}

	sw.db.Set([]byte(bannedPeerKey), datajson)
	return nil
}

// AddPeer performs the P2P handshake with a peer
// that already has a SecretConnection. If all goes well,
// it starts the peer and adds it to the switch.
// NOTE: This performs a blocking handshake before the peer is added.
// CONTRACT: If error is returned, peer is nil, and conn is immediately closed.
func (sw *Switch) AddPeer(pc *peerConn) error {
	peerNodeInfo, err := pc.HandshakeTimeout(sw.NodeInfo(), time.Duration(sw.peerConfig.HandshakeTimeout))
	if err != nil {
		return err
	}

	if err := version.Status.CheckUpdate(sw.nodeInfo.version(), peerNodeInfo.Version, peerNodeInfo.RemoteAddr); err != nil {
		return err
	}

	if err := sw.nodeInfo.compatibleWith(peerNodeInfo, version.CompatibleWith); err != nil {
		return err
	}

	peer := newPeer(pc, peerNodeInfo, sw.reactorsByCh, sw.chDescs, sw.StopPeerForError)
	if err := sw.filterConnByPeer(peer); err != nil {
		return err
	}

	if pc.outbound && !peer.ServiceFlag().IsEnable(consensus.SFFullNode) {
		return ErrConnectSpvPeer
	}

	// Start peer
	if sw.IsRunning() {
		if err := sw.startInitPeer(peer); err != nil {
			return err
		}
	}

	return sw.peers.Add(peer)
}

// AddReactor adds the given reactor to the switch.
// NOTE: Not goroutine safe.
func (sw *Switch) AddReactor(name string, reactor Reactor) Reactor {
	// Validate the reactor.
	// No two reactors can share the same channel.
	for _, chDesc := range reactor.GetChannels() {
		chID := chDesc.ID
		if sw.reactorsByCh[chID] != nil {
			cmn.PanicSanity(fmt.Sprintf("Channel %X has multiple reactors %v & %v", chID, sw.reactorsByCh[chID], reactor))
		}
		sw.chDescs = append(sw.chDescs, chDesc)
		sw.reactorsByCh[chID] = reactor
	}
	sw.reactors[name] = reactor
	reactor.SetSwitch(sw)
	return reactor
}

// AddListener adds the given listener to the switch for listening to incoming peer connections.
// NOTE: Not goroutine safe.
func (sw *Switch) AddListener(l Listener) {
	sw.listeners = append(sw.listeners, l)
}

//DialPeerWithAddress dial node from net address
func (sw *Switch) DialPeerWithAddress(addr *NetAddress) error {
	log.Debug("Dialing peer address:", addr)
	sw.dialing.Set(addr.IP.String(), addr)
	defer sw.dialing.Delete(addr.IP.String())
	if err := sw.filterConnByIP(addr.IP.String()); err != nil {
		return err
	}

	pc, err := newOutboundPeerConn(addr, sw.nodePrivKey, sw.peerConfig)
	if err != nil {
		log.WithFields(log.Fields{"address": addr, " err": err}).Debug("DialPeer fail on newOutboundPeerConn")
		return err
	}

	if err = sw.AddPeer(pc); err != nil {
		log.WithFields(log.Fields{"address": addr, " err": err}).Debug("DialPeer fail on switch AddPeer")
		pc.CloseConn()
		return err
	}
	log.Debug("DialPeer added peer:", addr)
	return nil
}

//IsDialing prevent duplicate dialing
func (sw *Switch) IsDialing(addr *NetAddress) bool {
	return sw.dialing.Has(addr.IP.String())
}

// IsListening returns true if the switch has at least one listener.
// NOTE: Not goroutine safe.
func (sw *Switch) IsListening() bool {
	return len(sw.listeners) > 0
}

// Listeners returns the list of listeners the switch listens on.
// NOTE: Not goroutine safe.
func (sw *Switch) Listeners() []Listener {
	return sw.listeners
}

// NumPeers Returns the count of outbound/inbound and outbound-dialing peers.
func (sw *Switch) NumPeers() (outbound, inbound, dialing int) {
	peers := sw.peers.List()
	for _, peer := range peers {
		if peer.outbound {
			outbound++
		} else {
			inbound++
		}
	}
	dialing = sw.dialing.Size()
	return
}

// NodeInfo returns the switch's NodeInfo.
// NOTE: Not goroutine safe.
func (sw *Switch) NodeInfo() *NodeInfo {
	sw.nodeInfoMu.Lock()
	defer sw.nodeInfoMu.Unlock()

	return sw.nodeInfo
}

//Peers return switch peerset
func (sw *Switch) Peers() *PeerSet {
	return sw.peers
}

func (sw *Switch) UpdateNodeInfoHeight(bestHeight uint64, bestHash bc.Hash) {
	sw.nodeInfoMu.Lock()
	defer sw.nodeInfoMu.Unlock()

	sw.nodeInfo.updateBestHeight(bestHeight, bestHash)
}

// StopPeerForError disconnects from a peer due to external error.
func (sw *Switch) StopPeerForError(peer *Peer, reason interface{}) {
	log.WithFields(log.Fields{"peer": peer, " err": reason}).Debug("stopping peer for error")
	sw.stopAndRemovePeer(peer, reason)
}

// StopPeerGracefully disconnect from a peer gracefully.
func (sw *Switch) StopPeerGracefully(peerID string) {
	if peer := sw.peers.Get(peerID); peer != nil {
		sw.stopAndRemovePeer(peer, nil)
	}
}

func (sw *Switch) addPeerWithConnection(conn net.Conn) error {
	peerConn, err := newInboundPeerConn(conn, sw.nodePrivKey, sw.Config.P2P)
	if err != nil {
		conn.Close()
		return err
	}

	if err = sw.AddPeer(peerConn); err != nil {
		conn.Close()
		return err
	}
	return nil
}

func (sw *Switch) checkBannedPeer(peer string) error {
	sw.mtx.Lock()
	defer sw.mtx.Unlock()

	if banEnd, ok := sw.bannedPeer[peer]; ok {
		if time.Now().Before(banEnd) {
			return ErrConnectBannedPeer
		}
		sw.delBannedPeer(peer)
	}
	return nil
}

func (sw *Switch) delBannedPeer(addr string) error {
	sw.mtx.Lock()
	defer sw.mtx.Unlock()

	delete(sw.bannedPeer, addr)
	datajson, err := json.Marshal(sw.bannedPeer)
	if err != nil {
		return err
	}

	sw.db.Set([]byte(bannedPeerKey), datajson)
	return nil
}

func (sw *Switch) filterConnByIP(ip string) error {
	if ip == sw.nodeInfo.listenHost() {
		return ErrConnectSelf
	}
	return sw.checkBannedPeer(ip)
}

func (sw *Switch) filterConnByPeer(peer *Peer) error {
	if err := sw.checkBannedPeer(peer.remoteAddrHost()); err != nil {
		return err
	}

	if sw.nodeInfo.getPubkey().Equals(peer.PubKey().Wrap()) {
		return ErrConnectSelf
	}

	if sw.peers.Has(peer.Key) {
		return ErrDuplicatePeer
	}
	return nil
}

func (sw *Switch) listenerRoutine(l Listener) {
	for {
		inConn, ok := <-l.Connections()
		if !ok {
			break
		}

		// disconnect if we alrady have MaxNumPeers
		if sw.peers.Size() >= sw.Config.P2P.MaxNumPeers {
			inConn.Close()
			log.Info("Ignoring inbound connection: already have enough peers.")
			continue
		}

		// New inbound connection!
		if err := sw.addPeerWithConnection(inConn); err != nil {
			log.Info("Ignoring inbound connection: error while adding peer.", " address:", inConn.RemoteAddr().String(), " error:", err)
			continue
		}
	}
}

func (sw *Switch) dialPeerWorker(a *NetAddress, wg *sync.WaitGroup) {
	if err := sw.DialPeerWithAddress(a); err != nil {
		log.WithFields(log.Fields{"addr": a, "err": err}).Error("dialPeerWorker fail on dial peer")
	}
	wg.Done()
}

func (sw *Switch) ensureOutboundPeers() {
	numOutPeers, _, numDialing := sw.NumPeers()
	numToDial := (minNumOutboundPeers - (numOutPeers + numDialing))
	log.WithFields(log.Fields{"numOutPeers": numOutPeers, "numDialing": numDialing, "numToDial": numToDial}).Debug("ensure peers")
	if numToDial <= 0 {
		return
	}

	connectedPeers := make(map[string]struct{})
	for _, peer := range sw.Peers().List() {
		connectedPeers[peer.remoteAddrHost()] = struct{}{}
	}

	var wg sync.WaitGroup
	nodes := make([]*discover.Node, numToDial)
	n := sw.discv.ReadRandomNodes(nodes)
	for i := 0; i < n; i++ {
		try := NewNetAddressIPPort(nodes[i].IP, nodes[i].TCP)
		if sw.NodeInfo().ListenAddr == try.String() {
			continue
		}
		if dialling := sw.IsDialing(try); dialling {
			continue
		}
		if _, ok := connectedPeers[try.IP.String()]; ok {
			continue
		}

		wg.Add(1)
		go sw.dialPeerWorker(try, &wg)
	}
	wg.Wait()
}

func (sw *Switch) ensureOutboundPeersRoutine() {
	sw.ensureOutboundPeers()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sw.ensureOutboundPeers()
		case <-sw.Quit:
			return
		}
	}
}

func (sw *Switch) startInitPeer(peer *Peer) error {
	peer.Start() // spawn send/recv routines
	for _, reactor := range sw.reactors {
		if err := reactor.AddPeer(peer); err != nil {
			return err
		}
	}
	return nil
}

func (sw *Switch) stopAndRemovePeer(peer *Peer, reason interface{}) {
	sw.peers.Remove(peer)
	for _, reactor := range sw.reactors {
		reactor.RemovePeer(peer, reason)
	}
	peer.Stop()

	sentStatus, receivedStatus := peer.TrafficStatus()
	log.WithFields(log.Fields{
		"address":               peer.Addr().String(),
		"reason":                reason,
		"duration":              sentStatus.Duration.String(),
		"total_sent":            sentStatus.Bytes,
		"total_received":        receivedStatus.Bytes,
		"average_sent_rate":     sentStatus.AvgRate,
		"average_received_rate": receivedStatus.AvgRate,
	}).Info("disconnect with peer")
}
