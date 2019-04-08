package mdns

import (
	"context"
	"net"
	"strconv"

	cfg "github.com/bytom/config"
	"github.com/bytom/event"
	log "github.com/sirupsen/logrus"
	"github.com/zeroconf"
	"strings"
	"time"
)

const logModule = "p2p/mdns"

type LanPeerEvent struct {
	IP   []net.IP
	Port int
}

type LanDiscover struct {
	hasResolver     bool
	servicePort     int
	msg             []string
	entries         chan *zeroconf.ServiceEntry
	server          *zeroconf.Server
	eventDispatcher *event.Dispatcher
	txMsgSub        *event.Subscription

	resolverQuit chan struct{}
	serviceQuit  chan struct{}
}

func protocolAndAddress(listenAddr string) (string, string) {
	p, address := "tcp", listenAddr
	parts := strings.SplitN(address, "://", 2)
	if len(parts) == 2 {
		p, address = parts[0], parts[1]
	}
	return p, address
}

func NewLanDiscover(config *cfg.Config) (*LanDiscover, error) {
	_, addr := protocolAndAddress(config.P2P.ListenAddress)
	_, port, err := net.SplitHostPort(addr)
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
		serviceQuit:     make(chan struct{}),
	}

	go ld.registerServiceRoutine()

	return ld, nil
}

func (ld *LanDiscover) Stop() {
	close(ld.resolverQuit)
	close(ld.serviceQuit)
	ld.eventDispatcher.Stop()
}

func (ld *LanDiscover) Subscribe() (*event.Subscription, error) {
	sub, err := ld.eventDispatcher.Subscribe(LanPeerEvent{})
	if err != nil {
		return nil, err
	}
	if !ld.hasResolver {
		if err = ld.registerResolver(); err != nil {
			return nil, err
		}
		ld.hasResolver = true
	}
	return sub, nil
}

func (ld *LanDiscover) registerServiceRoutine() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	server, err := registerService(ld.servicePort)
	if err != nil {
		log.WithFields(log.Fields{"module": logModule, "err": err}).Error("mdns service register error")
		return
	}
	for {
		select {
		case <-ticker.C:
			server.Shutdown()
			if _, err := registerService(ld.servicePort); err != nil {
				log.WithFields(log.Fields{"module": logModule, "err": err}).Error("mdns service register error")
				return
			}
		case <-ld.serviceQuit:
			return
		}
	}
}

func registerService(port int) (*zeroconf.Server, error) {
	return zeroconf.Register("bytomd", "lanDiscv", "local.", port, nil, nil)
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
	err = resolver.Browse(ctx, "lanDiscv", "local.", ld.entries)
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
			ld.eventDispatcher.Post(LanPeerEvent{IP: entry.AddrIPv4, Port: entry.Port})

		case <-ld.resolverQuit:
			return
		}
	}
}
