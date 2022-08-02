package rendezvous

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	ggio "github.com/gogo/protobuf/io"
	"github.com/libp2p/go-libp2p-core/host"
	inet "github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	pb "github.com/libp2p/go-libp2p-rendezvous/pb"
)

const (
	ServiceType  = "inmem"
	ServiceProto = protocol.ID("/rendezvous/sync/inmem/1.0.0")
)

type PubSub struct {
	mu     sync.RWMutex
	host   host.Host
	topics map[string]*PubSubSubscribers
}

type PubSubSubscribers struct {
	mu               sync.RWMutex
	subscribers      map[peer.ID]ggio.Writer
	lastAnnouncement *pb.RegistrationRecord
}

type PubSubSubscriptionDetails struct {
	PeerID      string
	ChannelName string
}

func NewSyncInMemProvider(host host.Host) (*PubSub, error) {
	ps := &PubSub{
		host:   host,
		topics: map[string]*PubSubSubscribers{},
	}

	ps.Listen()

	return ps, nil
}

func (ps *PubSub) Subscribe(ns string) (syncDetails string, err error) {
	details, err := json.Marshal(&PubSubSubscriptionDetails{
		PeerID:      ps.host.ID().String(),
		ChannelName: ns,
	})

	if err != nil {
		return "", fmt.Errorf("unable to marshal subscription details: %w", err)
	}

	return string(details), nil
}

func (ps *PubSub) GetServiceType() string {
	return ServiceType
}

func (ps *PubSub) getOrCreateTopic(ns string) *PubSubSubscribers {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if subscribers, ok := ps.topics[ns]; ok {
		return subscribers
	}

	ps.topics[ns] = &PubSubSubscribers{
		subscribers:      map[peer.ID]ggio.Writer{},
		lastAnnouncement: nil,
	}
	return ps.topics[ns]
}

func (ps *PubSub) Register(pid peer.ID, ns string, addrs [][]byte, ttlAsSeconds int, counter uint64) {
	subscribers := ps.getOrCreateTopic(ns)
	dataToSend := &pb.RegistrationRecord{
		Id:    pid.String(),
		Addrs: addrs,
		Ns:    ns,
		Ttl:   time.Now().Add(time.Duration(ttlAsSeconds) * time.Second).UnixMilli(),
	}

	subscribers.mu.Lock()
	subscribers.lastAnnouncement = dataToSend
	toNotify := subscribers.subscribers
	subscribers.mu.Unlock()

	for _, stream := range toNotify {
		if err := stream.WriteMsg(dataToSend); err != nil {
			log.Errorf("unable to notify rendezvous data update: %s", err.Error())
		}
	}
}

func (ps *PubSub) Unregister(p peer.ID, ns string) {
	// TODO: unsupported
}

func (ps *PubSub) Listen() {
	ps.host.SetStreamHandler(ServiceProto, ps.handleStream)
}

func (ps *PubSub) handleStream(s inet.Stream) {
	defer s.Reset()

	r := ggio.NewDelimitedReader(s, inet.MessageSizeMax)
	w := ggio.NewDelimitedWriter(s)

	subscribedTopics := map[string]struct{}{}

	for {
		var req pb.Message

		err := r.ReadMsg(&req)
		if err != nil {
			for ns := range subscribedTopics {
				topic := ps.getOrCreateTopic(ns)
				topic.mu.Lock()
				delete(topic.subscribers, s.Conn().RemotePeer())
				topic.mu.Unlock()
			}
			return
		}

		if req.Type != pb.Message_DISCOVER_SUBSCRIBE {
			continue
		}

		topic := ps.getOrCreateTopic(req.DiscoverSubscribe.Ns)
		topic.mu.Lock()
		if _, ok := topic.subscribers[s.Conn().RemotePeer()]; ok {
			topic.mu.Unlock()
			continue
		}

		topic.subscribers[s.Conn().RemotePeer()] = w
		subscribedTopics[req.DiscoverSubscribe.Ns] = struct{}{}
		lastAnnouncement := topic.lastAnnouncement
		topic.mu.Unlock()

		if lastAnnouncement != nil {
			if err := w.WriteMsg(lastAnnouncement); err != nil {
				log.Errorf("unable to write announcement: %s", err.Error())
			}
		}
	}
}

var _ RendezvousSync = (*PubSub)(nil)
var _ RendezvousSyncSubscribable = (*PubSub)(nil)
