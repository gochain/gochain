// Copyright 2014 The go-ethereum Authors
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

package whisperv2

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opencensus.io/trace"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/p2p"
	"github.com/gochain-io/gochain/rlp"
)

// peer represents a whisper protocol peer connection.
type peer struct {
	host *Whisper
	peer *p2p.Peer
	ws   p2p.MsgReadWriter

	knownMu sync.RWMutex
	known   map[common.Hash]struct{} // Messages already known by the peer to avoid wasting bandwidth

	quit chan struct{}
}

// newPeer creates a new whisper peer object, but does not run the handshake itself.
func newPeer(host *Whisper, remote *p2p.Peer, rw p2p.MsgReadWriter) *peer {
	return &peer{
		host:  host,
		peer:  remote,
		ws:    rw,
		known: make(map[common.Hash]struct{}),
		quit:  make(chan struct{}),
	}
}

// start initiates the peer updater, periodically broadcasting the whisper packets
// into the network.
func (self *peer) start() {
	go self.update()
	log.Debug(fmt.Sprintf("%v: whisper started", self.peer))
}

// stop terminates the peer updater, stopping message forwarding to it.
func (self *peer) stop() {
	close(self.quit)
	log.Debug(fmt.Sprintf("%v: whisper stopped", self.peer))
}

// handshake sends the protocol initiation status message to the remote peer and
// verifies the remote status too.
func (self *peer) handshake() error {
	ctx, span := trace.StartSpan(context.Background(), "peer.handshake")
	defer span.End()

	// Send the handshake status message asynchronously
	errc := make(chan error, 1)
	go func() {
		errc <- p2p.SendItemsCtx(ctx, self.ws, statusCode, protocolVersion)
	}()
	// Fetch the remote status packet and verify protocol match
	packet, err := self.ws.ReadMsg()
	if err != nil {
		return err
	}
	if packet.Code != statusCode {
		return fmt.Errorf("peer sent %x before status packet", packet.Code)
	}
	s := rlp.NewStream(packet.Payload, uint64(packet.Size))
	if _, err := s.List(); err != nil {
		return fmt.Errorf("bad status message: %v", err)
	}
	peerVersion, err := s.Uint()
	if err != nil {
		return fmt.Errorf("bad status message: %v", err)
	}
	if peerVersion != protocolVersion {
		return fmt.Errorf("protocol version mismatch %d != %d", peerVersion, protocolVersion)
	}
	// Wait until out own status is consumed too
	if err := <-errc; err != nil {
		return fmt.Errorf("failed to send status packet: %v", err)
	}
	return nil
}

// update executes periodic operations on the peer, including message transmission
// and expiration.
func (self *peer) update() {
	// Start the tickers for the updates
	expire := time.NewTicker(expirationCycle)
	transmit := time.NewTicker(transmissionCycle)

	// Loop and transmit until termination is requested
	for {
		select {
		case <-expire.C:
			self.expire()

		case <-transmit.C:
			if err := self.broadcast(); err != nil {
				log.Info(fmt.Sprintf("%v: broadcast failed: %v", self.peer, err))
				return
			}

		case <-self.quit:
			return
		}
	}
}

// mark marks an envelope known to the peer so that it won't be sent back.
func (self *peer) mark(envelope *Envelope) {
	h := envelope.Hash()
	self.knownMu.Lock()
	self.known[h] = struct{}{}
	self.knownMu.Unlock()
}

// marked checks if an envelope is already known to the remote peer.
func (self *peer) marked(envelope *Envelope) bool {
	h := envelope.Hash()
	self.knownMu.RLock()
	_, ok := self.known[h]
	self.knownMu.RUnlock()
	return ok
}

// expire iterates over all the known envelopes in the host and removes all
// expired (unknown) ones from the known list.
func (self *peer) expire() {
	// Assemble the list of available envelopes
	available := make(map[common.Hash]struct{})
	for _, envelope := range self.host.envelopes() {
		available[envelope.Hash()] = struct{}{}
	}
	// Cross reference availability with known status
	unmark := make(map[common.Hash]struct{})
	self.knownMu.Lock()
	defer self.knownMu.Unlock()
	for h := range self.known {
		if _, ok := available[h]; !ok {
			unmark[h] = struct{}{}
		}
	}
	// Dump all known but unavailable
	for hash := range unmark {
		delete(self.known, hash)
	}
}

// broadcast iterates over the collection of envelopes and transmits yet unknown
// ones over the network.
func (self *peer) broadcast() error {
	// Fetch the envelopes and collect the unknown ones
	envelopes := self.host.envelopes()
	transmit := make([]*Envelope, 0, len(envelopes))
	for _, envelope := range envelopes {
		if !self.marked(envelope) {
			transmit = append(transmit, envelope)
			self.mark(envelope)
		}
	}
	// Transmit the unknown batch (potentially empty)
	if err := p2p.Send(self.ws, messagesCode, transmit); err != nil {
		return err
	}
	log.Trace(fmt.Sprint(self.peer, "broadcasted", len(transmit), "message(s)"))
	return nil
}
