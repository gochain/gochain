// Copyright 2015 The go-ethereum Authors
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

package eth

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/p2p"
	"github.com/gochain-io/gochain/rlp"
)

var (
	errClosed            = errors.New("peer set is closed")
	errAlreadyRegistered = errors.New("peer is already registered")
	errNotRegistered     = errors.New("peer is not registered")
)

const (
	maxKnownTxs       = 65536           // Maximum transactions hashes to keep in the known list (prevent DOS)
	maxKnownBlocks    = 1024            // Maximum block hashes to keep in the known list (prevent DOS)
	forgetTxsInterval = 2 * time.Minute // Timer interval to forget known txs
	handshakeTimeout  = 5 * time.Second
)

// PeerInfo represents a short summary of the GoChain sub-protocol metadata known
// about a connected peer.
type PeerInfo struct {
	Version    int      `json:"version"`    // GoChain protocol version negotiated
	Difficulty *big.Int `json:"difficulty"` // Total difficulty of the peer's blockchain
	Head       string   `json:"head"`       // SHA3 hash of the peer's best owned block
}

// knownHashes is a capped set of common.Hash safe for concurrent access.
// If forgetInterval is set, then the backing map is ignored or reset when older
// than forgetInterval.
type knownHashes struct {
	sync.RWMutex
	m              map[common.Hash]struct{}
	cap            int
	lastReset      time.Time
	forgetInterval time.Duration
}

func (s *knownHashes) reset() {
	s.m = make(map[common.Hash]struct{}, s.cap)
	s.lastReset = time.Now()
}

func (s *knownHashes) Add(h common.Hash) {
	s.Lock()
	if s.m == nil || (s.forgetInterval != 0 && time.Since(s.lastReset) > s.forgetInterval) {
		s.reset()
	}
	s.m[h] = struct{}{}
	s.Unlock()
}

func (s *knownHashes) AddAll(txs types.Transactions) {
	s.Lock()
	if s.m == nil || (s.forgetInterval != 0 && time.Since(s.lastReset) > s.forgetInterval) {
		s.reset()
	}
	for _, tx := range txs {
		s.m[tx.Hash()] = struct{}{}
	}
	s.Unlock()
}

// AddCapped is like Add, but first makes room if cap has been reached.
func (s *knownHashes) AddCapped(h common.Hash) {
	s.Lock()
	if s.m == nil || (s.forgetInterval != 0 && time.Since(s.lastReset) > s.forgetInterval) {
		s.reset()
	} else if len(s.m) >= s.cap {
		i := len(s.m) + 1 - s.cap
		for d := range s.m {
			delete(s.m, d)
			i--
			if i == 0 {
				break
			}
		}
	}
	s.m[h] = struct{}{}
	s.Unlock()
}

func (s *knownHashes) Has(h common.Hash) bool {
	var ok bool
	s.RLock()
	if s.m == nil || (s.forgetInterval != 0 && time.Since(s.lastReset) > s.forgetInterval) {
		ok = false
	} else {
		_, ok = s.m[h]
	}
	s.RUnlock()
	return ok
}

type peer struct {
	id string

	*p2p.Peer
	rw p2p.MsgReadWriter

	version  int         // Protocol version negotiated
	forkDrop *time.Timer // Timed connection dropper if forks aren't validated in time

	head common.Hash
	td   *big.Int
	lock sync.RWMutex

	knownTxs    knownHashes // Set of transaction hashes known to be known by this peer
	knownBlocks knownHashes // Set of block hashes known to be known by this peer
}

func newPeer(version int, p *p2p.Peer, rw p2p.MsgReadWriter) *peer {
	id := p.ID()

	r := &peer{
		Peer:        p,
		rw:          rw,
		version:     version,
		id:          fmt.Sprintf("%x", id[:8]),
		knownTxs:    knownHashes{cap: maxKnownTxs, forgetInterval: forgetTxsInterval},
		knownBlocks: knownHashes{cap: maxKnownBlocks},
	}

	return r
}

// Info gathers and returns a collection of metadata known about a peer.
func (p *peer) Info() *PeerInfo {
	hash, td := p.Head()

	return &PeerInfo{
		Version:    p.version,
		Difficulty: td,
		Head:       hash.Hex(),
	}
}

// Head retrieves a copy of the current head hash and total difficulty of the
// peer.
func (p *peer) Head() (hash common.Hash, td *big.Int) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	copy(hash[:], p.head[:])
	return hash, new(big.Int).Set(p.td)
}

// SetHead updates the head hash and total difficulty of the peer.
func (p *peer) SetHead(hash common.Hash, td *big.Int) {
	p.lock.Lock()
	defer p.lock.Unlock()

	copy(p.head[:], hash[:])
	p.td.Set(td)
}

// MarkBlock marks a block as known for the peer, ensuring that the block will
// never be propagated to this particular peer.
func (p *peer) MarkBlock(hash common.Hash) {
	p.knownBlocks.AddCapped(hash)
}

// MarkTransaction marks a transaction as known for the peer, ensuring that it
// will never be propagated to this particular peer.
func (p *peer) MarkTransaction(hash common.Hash) {
	p.knownTxs.AddCapped(hash)
}

// SendTransactions sends transactions to the peer and includes the hashes
// in its transaction hash set for future reference.
func (p *peer) SendTransactions(txs types.Transactions) error {
	err := p2p.Send(p.rw, TxMsg, txs)
	if err == nil {
		p.knownTxs.AddAll(txs)
	}
	return err
}

// SendNewBlockHashParallel announces the availability of a number of blocks through
// a hash notification to peers in parallel.
func SendNewBlockHashParallel(peers []*peer, hash common.Hash, number uint64) error {
	b, err := rlp.EncodeToBytes(newBlockHashesData{{Hash: hash, Number: number}})
	if err != nil {
		return err
	}
	for _, p := range peers {
		go func(p *peer) {
			msg := p2p.Msg{Code: NewBlockHashesMsg, Size: uint32(len(b)), Payload: bytes.NewReader(b)}
			if err := p.rw.WriteMsg(msg); err != nil {
				log.Error("Failed to send new block hash", "peer", p.ID(), "hash", hash, "number", number, "size", len(b), "err", err)
			} else {
				p.knownBlocks.Add(hash)
			}
		}(p)
	}
	return nil
}

// SendNewBlockParallel propagates an entire block to peers.
func SendNewBlockParallel(peers []*peer, block *types.Block, td *big.Int) error {
	b, err := rlp.EncodeToBytes([]interface{}{block, td})
	if err != nil {
		return err
	}
	for _, p := range peers {
		go func(p *peer) {
			msg := p2p.Msg{Code: NewBlockMsg, Size: uint32(len(b)), Payload: bytes.NewReader(b)}
			if err := p.rw.WriteMsg(msg); err != nil {
				log.Error("Failed to send new block hash", "peer", p.ID(), "hash", block.Hash(), "number", block.NumberU64(), "size", len(b), "err", err)
			} else {
				p.knownBlocks.Add(block.Hash())
			}
		}(p)
	}
	return nil
}

// SendBlockHeaders sends a batch of block headers to the remote peer.
func (p *peer) SendBlockHeaders(headers []*types.Header) error {
	return p2p.Send(p.rw, BlockHeadersMsg, headers)
}

// SendBlockBodiesRLP sends a batch of block contents to the remote peer from
// an already RLP encoded format.
func (p *peer) SendBlockBodiesRLP(bodies []rlp.RawValue) error {
	return p2p.Send(p.rw, BlockBodiesMsg, bodies)
}

// SendNodeDataRLP sends a batch of arbitrary internal data, corresponding to the
// hashes requested.
func (p *peer) SendNodeData(data [][]byte) error {
	return p2p.Send(p.rw, NodeDataMsg, data)
}

// SendReceiptsRLP sends a batch of transaction receipts, corresponding to the
// ones requested from an already RLP encoded format.
func (p *peer) SendReceiptsRLP(receipts []rlp.RawValue) error {
	return p2p.Send(p.rw, ReceiptsMsg, receipts)
}

// RequestOneHeader is a wrapper around the header query functions to fetch a
// single header. It is used solely by the fetcher.
func (p *peer) RequestOneHeader(hash common.Hash) error {
	p.Log().Debug("Fetching single header", "hash", hash)
	return p2p.Send(p.rw, GetBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Hash: hash}, Amount: uint64(1), Skip: uint64(0), Reverse: false})
}

// RequestHeadersByHash fetches a batch of blocks' headers corresponding to the
// specified header query, based on the hash of an origin block.
func (p *peer) RequestHeadersByHash(origin common.Hash, amount int, skip int, reverse bool) error {
	p.Log().Debug("Fetching batch of headers", "count", amount, "fromhash", origin, "skip", skip, "reverse", reverse)
	return p2p.Send(p.rw, GetBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Hash: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse})
}

// RequestHeadersByNumber fetches a batch of blocks' headers corresponding to the
// specified header query, based on the number of an origin block.
func (p *peer) RequestHeadersByNumber(origin uint64, amount int, skip int, reverse bool) error {
	p.Log().Debug("Fetching batch of headers", "count", amount, "fromnum", origin, "skip", skip, "reverse", reverse)
	return p2p.Send(p.rw, GetBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Number: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse})
}

// RequestBodies fetches a batch of blocks' bodies corresponding to the hashes
// specified.
func (p *peer) RequestBodies(hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of block bodies", "count", len(hashes))
	return p2p.Send(p.rw, GetBlockBodiesMsg, hashes)
}

// RequestNodeData fetches a batch of arbitrary data from a node's known state
// data, corresponding to the specified hashes.
func (p *peer) RequestNodeData(hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of state data", "count", len(hashes))
	return p2p.Send(p.rw, GetNodeDataMsg, hashes)
}

// RequestReceipts fetches a batch of transaction receipts from a remote node.
func (p *peer) RequestReceipts(hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of receipts", "count", len(hashes))
	return p2p.Send(p.rw, GetReceiptsMsg, hashes)
}

// Handshake executes the eth protocol handshake, negotiating version number,
// network IDs, difficulties, head and genesis blocks.
func (p *peer) Handshake(network uint64, td *big.Int, head common.Hash, genesis common.Hash) error {
	// Send out own handshake in a new thread
	errc := make(chan error, 2)
	var status statusData // safe to read after two values have been received from errc

	go func() {
		errc <- p2p.Send(p.rw, StatusMsg, &statusData{
			ProtocolVersion: uint32(p.version),
			NetworkId:       network,
			TD:              td,
			CurrentBlock:    head,
			GenesisBlock:    genesis,
		})
	}()
	go func() {
		errc <- p.readStatus(network, &status, genesis)
	}()
	timeout := time.NewTimer(handshakeTimeout)
	defer timeout.Stop()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errc:
			if err != nil {
				return err
			}
		case <-timeout.C:
			return p2p.DiscReadTimeout
		}
	}
	p.td, p.head = status.TD, status.CurrentBlock
	return nil
}

func (p *peer) readStatus(network uint64, status *statusData, genesis common.Hash) (err error) {
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Code != StatusMsg {
		return errResp(ErrNoStatusMsg, "first msg has code %x (!= %x)", msg.Code, StatusMsg)
	}
	if msg.Size > ProtocolMaxMsgSize {
		return errResp(ErrMsgTooLarge, "%v > %v", msg.Size, ProtocolMaxMsgSize)
	}
	// Decode the handshake and make sure everything matches
	if err := msg.Decode(&status); err != nil {
		return errResp(ErrDecode, "msg %v: %v", msg, err)
	}
	if status.GenesisBlock != genesis {
		return errResp(ErrGenesisBlockMismatch, "%x (!= %x)", status.GenesisBlock[:8], genesis[:8])
	}
	if status.NetworkId != network {
		return errResp(ErrNetworkIdMismatch, "%d (!= %d)", status.NetworkId, network)
	}
	if int(status.ProtocolVersion) != p.version {
		return errResp(ErrProtocolVersionMismatch, "%d (!= %d)", status.ProtocolVersion, p.version)
	}
	return nil
}

// String implements fmt.Stringer.
func (p *peer) String() string {
	return fmt.Sprintf("Peer %s [%s]", p.id,
		fmt.Sprintf("eth/%2d", p.version),
	)
}

// peerSet represents the collection of active peers currently participating in
// the GoChain sub-protocol.
type peerSet struct {
	peers  map[string]*peer
	lock   sync.RWMutex
	closed bool
}

// newPeerSet creates a new peer set to track the active participants.
func newPeerSet() *peerSet {
	return &peerSet{
		peers: make(map[string]*peer),
	}
}

// Register injects a new peer into the working set, or returns an error if the
// peer is already known.
func (ps *peerSet) Register(p *peer) error {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	if ps.closed {
		return errClosed
	}
	if _, ok := ps.peers[p.id]; ok {
		return errAlreadyRegistered
	}
	ps.peers[p.id] = p
	return nil
}

// Unregister removes a remote peer from the active set, disabling any further
// actions to/from that particular entity.
func (ps *peerSet) Unregister(id string) error {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	if _, ok := ps.peers[id]; !ok {
		return errNotRegistered
	}
	delete(ps.peers, id)
	return nil
}

// Peer retrieves the registered peer with the given id.
func (ps *peerSet) Peer(id string) *peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	return ps.peers[id]
}

// Len returns if the current number of peers in the set.
func (ps *peerSet) Len() int {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	return len(ps.peers)
}

// All returns all current peers.
func (ps *peerSet) All() []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	all := make([]*peer, 0, ps.Len())
	for _, p := range ps.peers {
		all = append(all, p)
	}
	return all
}

// PeersWithoutBlock retrieves a list of peers that do not have a given block in
// their set of known hashes.
func (ps *peerSet) PeersWithoutBlock(hash common.Hash) []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.knownBlocks.Has(hash) {
			list = append(list, p)
		}
	}
	return list
}

// PeersWithoutTxs retrieves a map of peers to transactions from txs which are not in their set of known hashes.
func (ps *peerSet) PeersWithoutTxs(txs types.Transactions) map[*peer]types.Transactions {
	peerTxs := make(map[*peer]types.Transactions)
	tracing := log.Tracing()

	ps.lock.RLock()
	defer ps.lock.RUnlock()

	for _, tx := range txs {
		hash := tx.Hash()
		var count int
		for _, p := range ps.peers {
			if p.knownTxs.Has(hash) {
				continue
			}
			peerTxs[p] = append(peerTxs[p], tx)
			count++
		}
		if !tracing || count == 0 {
			continue
		}
		log.Trace("Broadcast transaction", "hash", hash, "recipients", count)
	}
	return peerTxs
}

// BestPeer retrieves the known peer with the currently highest total difficulty.
func (ps *peerSet) BestPeer() *peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	var (
		bestPeer *peer
		bestTd   *big.Int
	)
	for _, p := range ps.peers {
		if _, td := p.Head(); bestPeer == nil || td.Cmp(bestTd) > 0 {
			bestPeer, bestTd = p, td
		}
	}
	return bestPeer
}

// Close disconnects all peers.
// No new peers can be registered after Close has returned.
func (ps *peerSet) Close() {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	for _, p := range ps.peers {
		p.Disconnect(p2p.DiscQuitting)
	}
	ps.closed = true
}
