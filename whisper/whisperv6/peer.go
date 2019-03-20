// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package whisperv6

import (
	"context"
	"fmt"
	"math"
	"time"

	"go.opencensus.io/trace"

	"sync"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/log"
	"github.com/gochain-io/gochain/v3/p2p"
	"github.com/gochain-io/gochain/v3/rlp"
)

// Peer represents a whisper protocol peer connection.
type Peer struct {
	host *Whisper
	peer *p2p.Peer
	ws   p2p.MsgReadWriter

	mu             sync.RWMutex
	trusted        bool
	powRequirement float64
	bloomFilter    []byte
	fullNode       bool

	knownMu sync.RWMutex
	known   map[common.Hash]struct{} // Messages already known by the peer to avoid wasting bandwidth

	quit chan struct{}
}

// newPeer creates a new whisper peer object, but does not run the handshake itself.
func newPeer(host *Whisper, remote *p2p.Peer, rw p2p.MsgReadWriter) *Peer {
	return &Peer{
		host:           host,
		peer:           remote,
		ws:             rw,
		trusted:        false,
		powRequirement: 0.0,
		known:          make(map[common.Hash]struct{}),
		quit:           make(chan struct{}),
		bloomFilter:    makeFullNodeBloom(),
		fullNode:       true,
	}
}

// start initiates the peer updater, periodically broadcasting the whisper packets
// into the network.
func (peer *Peer) start() {
	go peer.update()
	log.Trace("start", "peer", peer.ID())
}

// stop terminates the peer updater, stopping message forwarding to it.
func (peer *Peer) stop() {
	close(peer.quit)
	log.Trace("stop", "peer", peer.ID())
}

// handshake sends the protocol initiation status message to the remote peer and
// verifies the remote status too.
func (peer *Peer) handshake() error {
	// Send the handshake status message asynchronously
	errc := make(chan error, 1)
	go func() {
		pow := peer.host.MinPow()
		powConverted := math.Float64bits(pow)
		bloom := peer.host.BloomFilter()
		errc <- p2p.SendItems(peer.ws, statusCode, ProtocolVersion, powConverted, bloom)
	}()

	// Fetch the remote status packet and verify protocol match
	packet, err := peer.ws.ReadMsg()
	if err != nil {
		return err
	}
	if packet.Code != statusCode {
		return fmt.Errorf("peer [%x] sent packet %x before status packet", peer.ID(), packet.Code)
	}
	s := rlp.NewStream(packet.Payload, uint64(packet.Size))
	defer rlp.Discard(s)
	_, err = s.List()
	if err != nil {
		return fmt.Errorf("peer [%x] sent bad status message: %v", peer.ID(), err)
	}
	peerVersion, err := s.Uint()
	if err != nil {
		return fmt.Errorf("peer [%x] sent bad status message (unable to decode version): %v", peer.ID(), err)
	}
	if peerVersion != ProtocolVersion {
		return fmt.Errorf("peer [%x]: protocol version mismatch %d != %d", peer.ID(), peerVersion, ProtocolVersion)
	}

	// only version is mandatory, subsequent parameters are optional
	powRaw, err := s.Uint()
	if err == nil {
		pow := math.Float64frombits(powRaw)
		if math.IsInf(pow, 0) || math.IsNaN(pow) || pow < 0.0 {
			return fmt.Errorf("peer [%x] sent bad status message: invalid pow", peer.ID())
		}
		peer.mu.Lock()
		peer.powRequirement = pow
		peer.mu.Unlock()

		var bloom []byte
		err = s.Decode(&bloom)
		if err == nil {
			sz := len(bloom)
			if sz != bloomFilterSize && sz != 0 {
				return fmt.Errorf("peer [%x] sent bad status message: wrong bloom filter size %d", peer.ID(), sz)
			}
			peer.setBloomFilter(bloom)
		}
	}

	if err := <-errc; err != nil {
		return fmt.Errorf("peer [%x] failed to send status packet: %v", peer.ID(), err)
	}
	return nil
}

// update executes periodic operations on the peer, including message transmission
// and expiration.
func (peer *Peer) update() {
	// Start the tickers for the updates
	expire := time.NewTicker(expirationCycle)
	transmit := time.NewTicker(transmissionCycle)

	// Loop and transmit until termination is requested
	for {
		select {
		case <-expire.C:
			peer.expire()

		case <-transmit.C:
			ctx, span := trace.StartSpan(context.Background(), "Peer.update.transmit")
			if err := peer.broadcast(ctx); err != nil {
				log.Trace("broadcast failed", "reason", err, "peer", peer.ID())
				span.SetStatus(trace.Status{
					Code:    trace.StatusCodeInternal,
					Message: err.Error(),
				})
				span.End()
				return
			}
			span.End()

		case <-peer.quit:
			return
		}
	}
}

// mark marks an envelope known to the peer so that it won't be sent back.
func (peer *Peer) mark(envelope *Envelope) {
	h := envelope.Hash()
	peer.knownMu.Lock()
	peer.known[h] = struct{}{}
	peer.knownMu.Unlock()
}

// marked checks if an envelope is already known to the remote peer.
func (peer *Peer) marked(envelope *Envelope) bool {
	h := envelope.Hash()
	peer.knownMu.RLock()
	_, ok := peer.known[h]
	peer.knownMu.RUnlock()
	return ok
}

// expire iterates over all the known envelopes in the host and removes all
// expired (unknown) ones from the known list.
func (peer *Peer) expire() {
	unmark := make(map[common.Hash]struct{})
	peer.knownMu.Lock()
	defer peer.knownMu.Unlock()
	for h := range peer.known {
		if !peer.host.isEnvelopeCached(h) {
			unmark[h] = struct{}{}
		}
	}
	// Dump all known but no longer cached
	for hash := range unmark {
		delete(peer.known, hash)
	}
}

// broadcast iterates over the collection of envelopes and transmits yet unknown
// ones over the network.
func (peer *Peer) broadcast(ctx context.Context) error {
	envelopes := peer.host.Envelopes()
	bundle := make([]*Envelope, 0, len(envelopes))
	for _, envelope := range envelopes {
		peer.mu.RLock()
		pow := peer.powRequirement
		peer.mu.RUnlock()
		if !peer.marked(envelope) && envelope.PoW() >= pow && peer.bloomMatch(envelope) {
			bundle = append(bundle, envelope)
		}
	}

	if len(bundle) > 0 {
		// transmit the batch of envelopes
		if err := p2p.Send(peer.ws, messagesCode, bundle); err != nil {
			return err
		}

		// mark envelopes only if they were successfully sent
		for _, e := range bundle {
			peer.mark(e)
		}

		log.Trace("broadcast", "num. messages", len(bundle))
	}
	return nil
}

// ID returns a peer's id
func (peer *Peer) ID() []byte {
	id := peer.peer.ID()
	return id[:]
}

func (peer *Peer) notifyAboutPowRequirementChange(ctx context.Context, pow float64) error {
	i := math.Float64bits(pow)
	return p2p.Send(peer.ws, powRequirementCode, i)
}

func (peer *Peer) notifyAboutBloomFilterChange(ctx context.Context, bloom []byte) error {
	return p2p.Send(peer.ws, bloomFilterExCode, bloom)
}

func (peer *Peer) bloomMatch(env *Envelope) bool {
	peer.mu.RLock()
	bf := peer.bloomFilter
	peer.mu.RUnlock()
	return peer.fullNode || bloomFilterMatch(bf, env.Bloom())
}

func (peer *Peer) setBloomFilter(bloom []byte) {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	peer.bloomFilter = bloom
	peer.fullNode = isFullNode(bloom)
	if peer.fullNode && peer.bloomFilter == nil {
		peer.bloomFilter = makeFullNodeBloom()
	}
}

func makeFullNodeBloom() []byte {
	bloom := make([]byte, bloomFilterSize)
	for i := 0; i < bloomFilterSize; i++ {
		bloom[i] = 0xFF
	}
	return bloom
}
