package mdns

import (
	"context"
	"net"
	"strconv"

	cfg "github.com/bytom/config"
	"github.com/bytom/event"
	log "github.com/sirupsen/logrus"
	"github.com/zeroconf"
)

const logModule = "p2p/mdns"

type MdnsLanPeerEvent struct {
	NetID string
	IP    []net.IP
	Port  int
}

type LanDiscover struct {
	servicePort     int
	msg             []string
	entries         chan *zeroconf.ServiceEntry
	server          *zeroconf.Server
	eventDispatcher *event.Dispatcher
	txMsgSub        *event.Subscription

	resolverQuit chan struct{}
}

func NewLanDiscover(config *cfg.Config) (*LanDiscover, error) {
	_, port, err := net.SplitHostPort(config.P2P.ListenAddress)
	if err != nil {
		return nil, err
	}

	servicePort, err := strconv.Atoi(port)
	if err != nil {
		return nil, err
	}

	ld := &LanDiscover{
		servicePort:     servicePort,
		msg:             []string{config.ChainID},
		entries:         make(chan *zeroconf.ServiceEntry),
		eventDispatcher: event.NewDispatcher(),
		resolverQuit:    make(chan struct{}),
	}

	if err := ld.registerService(); err != nil {
		return nil, err
	}

	if err := ld.registerResolver(); err != nil {
		return nil, err
	}

	return ld, nil
}

func (ld *LanDiscover) Stop() {
	if ld.server != nil {
		ld.server.Shutdown()
	}

	close(ld.resolverQuit)
	ld.eventDispatcher.Stop()
}

func (ld *LanDiscover) Subscribe() (*event.Subscription, error) {
	return ld.eventDispatcher.Subscribe(MdnsLanPeerEvent{})
}

func (ld *LanDiscover) registerService() error {
	server, err := zeroconf.Register("GoZeroconf", "_workstation._tcp", "local.", ld.servicePort, ld.msg, nil)
	if err != nil {
		log.WithFields(log.Fields{"module": logModule, "err": err}).Error("mdns service register error")
		return err
	}
	ld.server = server
	return nil
}

func (ld *LanDiscover) registerResolver() error {
	go ld.getLanPeerLoop()
	// Discover all services on the network (e.g. _workstation._tcp)
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.WithFields(log.Fields{"module": logModule, "err": err}).Error("mdns resolver register error")
		close(ld.resolverQuit)
		return err
	}

	ctx := context.Background()
	err = resolver.Browse(ctx, "_workstation._tcp", "local.", ld.entries)
	if err != nil {
		log.WithFields(log.Fields{"module": logModule, "err": err}).Error("mdns resolver browse error")
		close(ld.resolverQuit)
		return err
	}
	return nil
}

func (ld *LanDiscover) getLanPeerLoop() {
	for {
		select {
		case entry := <-ld.entries:
			ld.eventDispatcher.Post(MdnsLanPeerEvent{NetID: entry.Text[0], IP: entry.AddrIPv4, Port: entry.Port})

		case ld.resolverQuit:
			return
		}
	}
}
