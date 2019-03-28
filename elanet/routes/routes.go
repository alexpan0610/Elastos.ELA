/*
This package provides the DPOS routes(network addresses) protocol, it can
collect all DPOS peer addresses from the normal P2P network.
*/
package routes

import (
	"fmt"
	"time"

	"github.com/elastos/Elastos.ELA/blockchain"
	"github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/crypto"
	dp "github.com/elastos/Elastos.ELA/dpos/p2p/peer"
	"github.com/elastos/Elastos.ELA/elanet/peer"
	"github.com/elastos/Elastos.ELA/events"
	"github.com/elastos/Elastos.ELA/p2p/msg"
)

const (
	// minPeersToAnnounce defines the minimum connected peers to announce
	// DPOS address into the P2P network.
	minPeersToAnnounce = 3

	// maxTimeOffset indicates the maximum time offset with the to accept an DAddr
	// message.
	maxTimeOffset = 30 * time.Second

	// minAnnounceDuration indicates the minimum allowed time duration to
	// announce a new DAddr.
	minAnnounceDuration = 30 * time.Second
)

// cache stores the requested DAddrs from a peer.
type cache struct {
	requested map[common.Uint256]struct{}
}

// Config defines the parameters to create a Route instance.
type Config struct {
	// The PID of this peer if it is an arbiter.
	PID []byte

	// The network address of this arbiter.
	Addr string

	// TimeSource is the median time source of the P2P network.
	TimeSource blockchain.MedianTimeSource

	// Sign the addr message of this arbiter.
	Sign func(data []byte) (signature []byte)

	// IsCurrent returns whether BlockChain synced to best height.
	IsCurrent func() bool

	// RelayAddr relays the addresses inventory to the P2P network.
	RelayAddr func(iv *msg.InvVect, data interface{})

	// OnCipherAddr will be invoked when an address cipher received.
	OnCipherAddr func(pid dp.PID, addr []byte)
}

// state stores the DPOS addresses and other additional information tracking
// addresses syncing status.
type state struct {
	peers     map[dp.PID]struct{}
	addrIndex map[dp.PID]map[dp.PID]common.Uint256
	knownAddr map[common.Uint256]*msg.DAddr
	requested map[common.Uint256]struct{}
	peerCache map[*peer.Peer]*cache
}

type newPeerMsg *peer.Peer

type donePeerMsg *peer.Peer

type peersMsg struct {
	peers []dp.PID
}

type invMsg struct {
	peer *peer.Peer
	msg  *msg.Inv
}

type getDataMsg struct {
	peer *peer.Peer
	msg  *msg.GetData
}

type dAddrMsg struct {
	peer *peer.Peer
	msg  *msg.DAddr
}

// Routes is the DPOS routes implementation.
type Routes struct {
	pid      dp.PID
	cfg      *Config
	addr     string
	sign     func([]byte) []byte
	queue    chan interface{}
	announce chan struct{}
	quit     chan struct{}
}

// addrHandler is the main handler to syncing the addresses state.
func (r *Routes) addrHandler() {
	// lastAnnounce stores the last time announced
	var lastAnnounce time.Time

	state := &state{
		peers:     make(map[dp.PID]struct{}),
		addrIndex: make(map[dp.PID]map[dp.PID]common.Uint256),
		knownAddr: make(map[common.Uint256]*msg.DAddr),
		requested: make(map[common.Uint256]struct{}),
		peerCache: make(map[*peer.Peer]*cache),
	}

out:
	for {
		select {
		// Handle the messages from queue.
		case m := <-r.queue:
			switch m := m.(type) {
			case newPeerMsg:
				r.handleNewPeer(state, m)

			case donePeerMsg:
				r.handleDonePeer(state, m)

			case invMsg:
				r.handleInv(state, m.peer, m.msg)

			case getDataMsg:
				r.handleGetData(state, m.peer, m.msg)

			case dAddrMsg:
				r.handleDAddr(state, m.peer, m.msg)

			case *peersMsg:
				r.handlePeersMsg(state, m.peers)
			}

		// Handle the announce request.
		case <-r.announce:

			// Do not announce address if connected peers not enough.
			if len(state.peerCache) < minPeersToAnnounce {
				r.announce <- struct{}{}
				continue
			}

			// Do not announce address too frequent.
			now := time.Now()
			if lastAnnounce.Add(minAnnounceDuration).After(now) {
				r.announce <- struct{}{}
				continue
			}

			r.handleAnnounce(state)
			lastAnnounce = time.Now()

		case <-r.quit:
			break out
		}
	}
}

func (r *Routes) handleNewPeer(s *state, p *peer.Peer) {
	// Create state for the new peer.
	s.peerCache[p] = &cache{requested: make(map[common.Uint256]struct{})}
}

func (r *Routes) handleDonePeer(s *state, p *peer.Peer) {
	c, exists := s.peerCache[p]
	if !exists {
		log.Warnf("Received done peer message for unknown peer %s", p)
		return
	}

	// Remove done peer from peer state.
	delete(s.peerCache, p)

	// Clear cached information.
	for pid := range c.requested {
		delete(c.requested, pid)
	}
}

func (r *Routes) handlePeersMsg(state *state, peers []dp.PID) {
	// Compare current peers and new peers to find the difference.
	var newPeers = make(map[dp.PID]struct{})
	for _, pid := range peers {
		newPeers[pid] = struct{}{}

		// Initiate address index.
		_, ok := state.addrIndex[pid]
		if !ok {
			state.addrIndex[pid] = make(map[dp.PID]common.Uint256)
		}
	}

	// Remove peers that not in new peers list.
	var delPeers []dp.PID
	for pid := range state.peers {
		if _, ok := newPeers[pid]; ok {
			continue
		}
		delPeers = append(delPeers, pid)
	}

	for _, pid := range delPeers {
		// Remove from index and known addr.
		pids, ok := state.addrIndex[pid]
		if !ok {
			continue
		}
		for _, pid := range pids {
			delete(state.knownAddr, pid)
		}
		delete(state.addrIndex, pid)
	}

	// Announce address into P2P network if we become arbiter.
	_, isArbiter := newPeers[r.pid]
	_, wasArbiter := state.peers[r.pid]
	if isArbiter && !wasArbiter {
		r.announce <- struct{}{}
	}

	// Update peers list.
	state.peers = newPeers
}

func (r *Routes) handleInv(s *state, p *peer.Peer, m *msg.Inv) {
	c, exists := s.peerCache[p]
	if !exists {
		log.Warnf("Received inv message for unknown peer %s", p)
		return
	}

	// Push GetData message according to the Inv message.
	getData := msg.NewGetData()
	for _, iv := range m.InvList {
		switch iv.Type {
		case msg.InvTypeAddress:
		default:
			continue
		}

		// Add the inventory to the cache of known inventory
		// for the peer.
		p.AddKnownInventory(iv)

		_, ok := s.knownAddr[iv.Hash]
		if ok {
			continue
		}

		if _, ok := s.requested[iv.Hash]; ok {
			continue
		}

		c.requested[iv.Hash] = struct{}{}
		s.requested[iv.Hash] = struct{}{}
		getData.AddInvVect(msg.NewInvVect(msg.InvTypeAddress, &iv.Hash))
	}

	if len(getData.InvList) > 0 {
		p.QueueMessage(getData, nil)
	}
}

func (r *Routes) handleGetData(s *state, p *peer.Peer, m *msg.GetData) {
	_, exists := s.peerCache[p]
	if !exists {
		log.Warnf("Received getdata message for unknown peer %s", p)
		return
	}

	done := make(chan struct{})
	for _, iv := range m.InvList {
		switch iv.Type {
		case msg.InvTypeAddress:
			// Attempt to fetch the requested addr.
			addr, ok := s.knownAddr[iv.Hash]
			if !ok {
				done <- struct{}{}
				continue
			}

			p.QueueMessage(addr, done)
			<-done

		default:
			continue
		}
	}
}

func (r *Routes) appendAddr(s *state, m *msg.DAddr) error {
	hash := m.Hash()

	_, ok := s.peers[m.PID]
	if !ok {
		return fmt.Errorf("PID not in arbiter list")
	}

	// Append received addr into known addr index.
	s.addrIndex[m.PID][m.Encode] = hash
	s.knownAddr[hash] = m

	// Relay addr to the P2P network.
	iv := msg.NewInvVect(msg.InvTypeAddress, &hash)
	r.cfg.RelayAddr(iv, m)
	return nil
}

// verifyDAddr verifies if this is a valid DPOS address message.
func (r *Routes) verifyDAddr(s *state, m *msg.DAddr) error {
	// Verify signature of the message.
	pubKey, err := crypto.DecodePoint(m.PID[:])
	if err != nil {
		return fmt.Errorf("invalid public key")
	}
	err = crypto.Verify(*pubKey, m.Data(), m.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature")
	}

	// Verify timestamp of the message. A DAddr to same arbiter can not be sent
	// frequently to prevent attack, and a DAddr timestamp must not to far from
	// the P2P network median time.
	if index, ok := s.addrIndex[m.PID]; ok {
		if hash, ok := index[m.Encode]; ok {
			ka := s.knownAddr[hash]

			// Abandon address older than the known address to the same arbiter.
			if ka.Timestamp.After(m.Timestamp) {
				return fmt.Errorf("timestamp is older than known")
			}

			// Check if timestamp out of median time offset.
			medianTime := r.cfg.TimeSource.AdjustedTime()
			minTime := medianTime.Add(-maxTimeOffset)
			maxTime := medianTime.Add(maxTimeOffset)
			if m.Timestamp.Before(minTime) || m.Timestamp.After(maxTime) {
				return fmt.Errorf("timestamp out of offset range")
			}

			// Check if the address announces too frequent.
			if ka.Timestamp.Add(minAnnounceDuration).After(m.Timestamp) {
				return fmt.Errorf("address announce too frequent")
			}
		}
	}

	return nil
}

func (r *Routes) handleDAddr(s *state, p *peer.Peer, m *msg.DAddr) {
	c, exists := s.peerCache[p]
	if !exists {
		log.Warnf("Received getdaddr message for unknown peer %s", p)
		return
	}

	hash := m.Hash()

	if _, ok := c.requested[hash]; !ok {
		log.Warnf("Got unrequested addr %s from %s -- disconnecting",
			hash, p)
		p.Disconnect()
		return
	}

	delete(c.requested, hash)
	delete(s.requested, hash)

	if err := r.verifyDAddr(s, m); err != nil {
		log.Warnf("Got invalid addr %s %s from %s -- disconnecting",
			hash, err, p)
		p.Disconnect()
		return
	}

	// Append received addr into state.
	if err := r.appendAddr(s, m); err != nil {
		log.Warnf("Got invalid addr %s from %s -- disconnecting",
			err, p)
		p.Disconnect()
		return
	}

	// Notify the received DPOS address if the Encode matches.
	if r.pid.Equal(m.Encode) && r.cfg.OnCipherAddr != nil {
		r.cfg.OnCipherAddr(m.PID, m.Cipher)
	}
}

func (r *Routes) handleAnnounce(s *state) {
	for pid := range s.peers {
		// Do not create address for self.
		if r.pid.Equal(pid) {
			continue
		}

		pubKey, err := crypto.DecodePoint(pid[:])
		if err != nil {
			continue
		}

		// Generate DAddr for the given PID.
		cipher, err := crypto.Encrypt(pubKey, []byte(r.addr))
		if err != nil {
			log.Warnf("encrypt addr failed %s", err)
			continue
		}
		addr := msg.DAddr{
			PID:       r.pid,
			Timestamp: r.cfg.TimeSource.AdjustedTime(),
			Encode:    pid,
			Cipher:    cipher,
		}
		addr.Signature = r.sign(addr.Data())

		// Append and relay the address.
		r.appendAddr(s, &addr)
	}
}

// Start starts the Routes instance to sync DPOS addresses.
func (r *Routes) Start() {
	go r.addrHandler()
}

// Stop quits the syncing address handler.
func (r *Routes) Stop() {
	close(r.quit)
}

// NewPeer notifies the new connected peer.
func (r *Routes) NewPeer(peer *peer.Peer) {
	r.queue <- newPeerMsg(peer)
}

// DonePeer notifies the disconnected peer.
func (r *Routes) DonePeer(peer *peer.Peer) {
	r.queue <- donePeerMsg(peer)
}

// QueueInv adds the passed Inv message and peer to the addr handling queue.
func (r *Routes) QueueInv(p *peer.Peer, m *msg.Inv) {
	r.queue <- invMsg{peer: p, msg: m}
}

// QueueInv adds the passed GetData message and peer to the addr handling queue.
func (r *Routes) QueueGetData(p *peer.Peer, m *msg.GetData) {
	r.queue <- getDataMsg{peer: p, msg: m}
}

// QueueInv adds the passed DAddr message and peer to the addr handling queue.
func (r *Routes) QueueDAddr(p *peer.Peer, m *msg.DAddr) {
	r.queue <- dAddrMsg{peer: p, msg: m}
}

// New creates and return a Routes instance.
func New(cfg *Config) *Routes {
	var pid dp.PID
	copy(pid[:], cfg.PID)

	r := Routes{
		pid:      pid,
		cfg:      cfg,
		addr:     cfg.Addr,
		sign:     cfg.Sign,
		queue:    make(chan interface{}, 256),
		announce: make(chan struct{}, 1),
		quit:     make(chan struct{}),
	}

	queuePeers := func(peers []dp.PID) {
		// Ignore if BlockChain not sync to current.
		if !cfg.IsCurrent() {
			return
		}
		r.queue <- &peersMsg{peers: peers}
	}

	events.Subscribe(func(e *events.Event) {
		switch e.Type {
		case events.ETDirectPeersChanged:
			go queuePeers(e.Data.([]dp.PID))
		}
	})
	return &r
}