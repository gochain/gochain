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
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"go.opencensus.io/trace"

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

	// maxQueuedTxs is the maximum number of transaction lists to queue up before
	// dropping broadcasts. This is a sensitive number as a transaction list might
	// contain a single transaction, or thousands.
	maxQueuedTxs = 16384

	// maxQueuedProps is the maximum number of block propagations to queue up before
	// dropping broadcasts.
	maxQueuedProps = 32

	// maxQueuedAnns is the maximum number of block announcements to queue up before
	// dropping broadcasts.
	maxQueuedAnns = 32
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

func (s *knownHashes) Add(ctx context.Context, h common.Hash) {
	_, span := trace.StartSpan(ctx, "knownHashes.Add")
	s.Lock()
	if s.m == nil || (s.forgetInterval != 0 && time.Since(s.lastReset) > s.forgetInterval) {
		s.reset()
	}
	s.m[h] = struct{}{}
	s.Unlock()
	span.End()
}

func (s *knownHashes) AddAll(ctx context.Context, txs types.Transactions) {
	_, span := trace.StartSpan(ctx, "knownHashes.AddAll")
	s.Lock()
	if s.m == nil || (s.forgetInterval != 0 && time.Since(s.lastReset) > s.forgetInterval) {
		s.reset()
	}
	for _, tx := range txs {
		s.m[tx.Hash()] = struct{}{}
	}
	s.Unlock()
	span.End()
}

// AddCapped is like Add, but first makes room if cap has been reached.
func (s *knownHashes) AddCapped(ctx context.Context, h common.Hash) {
	_, span := trace.StartSpan(ctx, "knownHashes.AddCapped")
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
	span.End()
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

// propEvent is a block propagation, waiting for its turn in the broadcast queue.
type propEvent struct {
	block *types.Block
	td    *big.Int
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

	queuedTxs   chan types.Transactions // Queue of transactions to broadcast to the peer
	queuedProps chan *propEvent         //Queue of blocks to broadcast to the peer
	queuedAnns  chan *types.Block       //Queue of blocks to announce to the peer
	term        chan struct{}           // Termination channel to stop the broadcaster
}

func newPeer(version int, p *p2p.Peer, rw p2p.MsgReadWriter) *peer {
	return &peer{
		Peer:        p,
		rw:          rw,
		version:     version,
		id:          fmt.Sprintf("%x", p.ID().Bytes()[:8]),
		knownTxs:    knownHashes{cap: maxKnownTxs, forgetInterval: forgetTxsInterval},
		knownBlocks: knownHashes{cap: maxKnownBlocks},
		queuedTxs:   make(chan types.Transactions, maxQueuedTxs),
		queuedProps: make(chan *propEvent, maxQueuedProps),
		queuedAnns:  make(chan *types.Block, maxQueuedAnns),
		term:        make(chan struct{}),
	}
}

// broadcast is a write loop that multiplexes block propagations, announcements
// and transaction broadcasts into the remote peer. The goal is to have an async
// writer that does not lock up node internals.
func (p *peer) broadcast() {
	for {
		select {
		case txs := <-p.queuedTxs:
			ctx, span := trace.StartSpan(context.Background(), "peer.broadcast-queuedTxs")

			const batchSize = 1000
		batchLoop:
			for len(txs) < batchSize {
				select {
				case more := <-p.queuedTxs:
					txs = append(txs, more...)
				default:
					break batchLoop
				}
			}
			span.AddAttributes(trace.Int64Attribute("txs", int64(len(txs))))
			if err := p.SendTransactions(ctx, txs); err != nil {
				if err != p2p.ErrShuttingDown {
					p.Log().Error("Failed to broadcast txs", "len", len(txs), "err", err)
				}
				span.SetStatus(trace.Status{
					Code:    trace.StatusCodeInternal,
					Message: err.Error(),
				})
			} else {
				p.Log().Trace("Broadcast txs", "len", len(txs))
			}

			span.End()

		case prop := <-p.queuedProps:
			ctx, span := trace.StartSpan(context.Background(), "peer.broadcast-queuedProps")
			span.AddAttributes(trace.Int64Attribute("num", int64(prop.block.NumberU64())))
			if err := p.SendNewBlock(ctx, prop.block, prop.td); err != nil {
				p.Log().Error("Failed to propagate block", "number", prop.block.Number(), "hash", prop.block.Hash(), "td", prop.td, "err", err)
				span.SetStatus(trace.Status{
					Code:    trace.StatusCodeInternal,
					Message: err.Error(),
				})
			} else {
				p.Log().Trace("Propagated block", "number", prop.block.Number(), "hash", prop.block.Hash(), "td", prop.td)
			}
			span.End()

		case block := <-p.queuedAnns:
			ctx, span := trace.StartSpan(context.Background(), "peer.broadcast-queuedAnns")
			span.AddAttributes(trace.Int64Attribute("num", int64(block.NumberU64())))
			if err := p.SendNewBlockHash(ctx, block.Hash(), block.NumberU64()); err != nil {
				p.Log().Error("Failed to announce block", "number", block.Number(), "hash", block.Hash(), "err", err)
				span.SetStatus(trace.Status{
					Code:    trace.StatusCodeInternal,
					Message: err.Error(),
				})
			} else {
				p.Log().Trace("Announced block", "number", block.Number(), "hash", block.Hash())
			}
			span.End()

		case <-p.term:
			return
		}
	}
}

// Close signals the broadcast goroutine to terminate.
func (p *peer) Close() {
	close(p.term)
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
func (p *peer) MarkBlock(ctx context.Context, hash common.Hash) {
	p.knownBlocks.AddCapped(ctx, hash)
}

// MarkTransaction marks a transaction as known for the peer, ensuring that it
// will never be propagated to this particular peer.
func (p *peer) MarkTransaction(ctx context.Context, hash common.Hash) {
	p.knownTxs.AddCapped(ctx, hash)
}

// SendTransactions sends transactions to the peer and includes the hashes
// in its transaction hash set for future reference.
func (p *peer) SendTransactions(ctx context.Context, txs types.Transactions) error {
	ctx, span := trace.StartSpan(ctx, "peer.Transactions")
	defer span.End()

	if err := p2p.SendCtx(ctx, p.rw, TxMsg, txs); err != nil {
		span.SetStatus(trace.Status{
			Code:    trace.StatusCodeInternal,
			Message: err.Error(),
		})
		return err
	}
	p.knownTxs.AddAll(ctx, txs)
	return nil
}

// SendTransactionsAsync queues txs for broadcast, or drops them if the queue is full.
func (p *peer) SendTransactionsAsync(txs types.Transactions) {
	select {
	case p.queuedTxs <- txs:
	default:
		p.Log().Trace("Dropping transaction propagation: queue full", "count", len(txs))
	}
}

// SendNewBlockAsync queues a block for propagation, or drops it if the queue is full.
func (p *peer) SendNewBlockAsync(block *types.Block, td *big.Int) {
	select {
	case p.queuedProps <- &propEvent{block: block, td: td}:
	default:
		p.Log().Info("Dropping block propagation; queue full", "number", block.NumberU64(), "hash", block.Hash(), "diff", block.Difficulty(), "parent", block.ParentHash())
	}
}

// SendNewBlockHashAsync queues a block announcement, or drops it if the queue is full.
func (p *peer) SendNewBlockHashAsync(block *types.Block) {
	select {
	case p.queuedAnns <- block:
	default:
		p.Log().Info("Dropping block announcement; queue full", "number", block.NumberU64(), "hash", block.Hash(), "diff", block.Difficulty(), "parent", block.ParentHash())
	}
}

// SendNewBlockHash announces the availability of a block.
func (p *peer) SendNewBlockHash(ctx context.Context, hash common.Hash, number uint64) error {
	ctx, span := trace.StartSpan(ctx, "peer.SendNewBlockHash")
	defer span.End()

	b, err := rlp.EncodeToBytesCtx(ctx, newBlockHashesData{{Hash: hash, Number: number}})
	if err != nil {
		return err
	}
	msg := p2p.Msg{Code: NewBlockHashesMsg, Size: uint32(len(b)), Payload: bytes.NewReader(b)}

	if err := p.rw.WriteMsg(ctx, msg); err != nil {
		return err
	}
	p.knownBlocks.Add(ctx, hash)
	return nil
}

// SendNewBlock propagates an entire block.
func (p *peer) SendNewBlock(ctx context.Context, block *types.Block, td *big.Int) error {
	ctx, span := trace.StartSpan(ctx, "peer.SendNewBlock")
	defer span.End()

	b, err := rlp.EncodeToBytesCtx(ctx, []interface{}{block, td})
	if err != nil {
		return err
	}
	msg := p2p.Msg{Code: NewBlockMsg, Size: uint32(len(b)), Payload: bytes.NewReader(b)}

	_, ws := trace.StartSpan(ctx, "MsgWriter.WriteMsg")
	if err := p.rw.WriteMsg(ctx, msg); err != nil {
		ws.SetStatus(trace.Status{
			Code:    trace.StatusCodeInternal,
			Message: err.Error(),
		})
		ws.End()
		return err
	}
	ws.End()
	p.knownBlocks.Add(ctx, block.Hash())
	return nil
}

// SendBlockHeaders sends a batch of block headers to the remote peer.
func (p *peer) SendBlockHeaders(ctx context.Context, headers []*types.Header) error {
	return p2p.SendCtx(ctx, p.rw, BlockHeadersMsg, headers)
}

// SendBlockBodiesRLP sends a batch of block contents to the remote peer from
// an already RLP encoded format.
func (p *peer) SendBlockBodiesRLP(ctx context.Context, bodies []rlp.RawValue) error {
	return p2p.SendCtx(ctx, p.rw, BlockBodiesMsg, bodies)
}

// SendNodeDataRLP sends a batch of arbitrary internal data, corresponding to the
// hashes requested.
func (p *peer) SendNodeData(ctx context.Context, data [][]byte) error {
	return p2p.SendCtx(ctx, p.rw, NodeDataMsg, data)
}

// SendReceiptsRLP sends a batch of transaction receipts, corresponding to the
// ones requested from an already RLP encoded format.
func (p *peer) SendReceiptsRLP(ctx context.Context, receipts []rlp.RawValue) error {
	return p2p.SendCtx(ctx, p.rw, ReceiptsMsg, receipts)
}

// RequestOneHeader is a wrapper around the header query functions to fetch a
// single header. It is used solely by the fetcher.
func (p *peer) RequestOneHeader(ctx context.Context, hash common.Hash) error {
	p.Log().Debug("Fetching single header", "hash", hash)
	return p2p.SendCtx(ctx, p.rw, GetBlockHeadersMsg,
		&getBlockHeadersData{Origin: hashOrNumber{Hash: hash}, Amount: uint64(1), Skip: uint64(0), Reverse: false})
}

// RequestHeadersByHash fetches a batch of blocks' headers corresponding to the
// specified header query, based on the hash of an origin block.
func (p *peer) RequestHeadersByHash(ctx context.Context, origin common.Hash, amount int, skip int, reverse bool) error {
	p.Log().Debug("Fetching batch of headers", "count", amount, "fromhash", origin, "skip", skip, "reverse", reverse)
	return p2p.SendCtx(ctx, p.rw, GetBlockHeadersMsg,
		&getBlockHeadersData{Origin: hashOrNumber{Hash: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse})
}

// RequestHeadersByNumber fetches a batch of blocks' headers corresponding to the
// specified header query, based on the number of an origin block.
func (p *peer) RequestHeadersByNumber(ctx context.Context, origin uint64, amount int, skip int, reverse bool) error {
	p.Log().Debug("Fetching batch of headers", "count", amount, "fromnum", origin, "skip", skip, "reverse", reverse)
	return p2p.SendCtx(ctx, p.rw, GetBlockHeadersMsg,
		&getBlockHeadersData{Origin: hashOrNumber{Number: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse})
}

// RequestBodies fetches a batch of blocks' bodies corresponding to the hashes
// specified.
func (p *peer) RequestBodies(ctx context.Context, hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of block bodies", "count", len(hashes))
	return p2p.SendCtx(ctx, p.rw, GetBlockBodiesMsg, hashes)
}

// RequestNodeData fetches a batch of arbitrary data from a node's known state
// data, corresponding to the specified hashes.
func (p *peer) RequestNodeData(ctx context.Context, hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of state data", "count", len(hashes))
	return p2p.SendCtx(ctx, p.rw, GetNodeDataMsg, hashes)
}

// RequestReceipts fetches a batch of transaction receipts from a remote node.
func (p *peer) RequestReceipts(ctx context.Context, hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of receipts", "count", len(hashes))
	return p2p.SendCtx(ctx, p.rw, GetReceiptsMsg, hashes)
}

// Handshake executes the eth protocol handshake, negotiating version number,
// network IDs, difficulties, head and genesis blocks.
func (p *peer) Handshake(network uint64, td *big.Int, head common.Hash, genesis common.Hash) error {
	ctx, span := trace.StartSpan(context.Background(), "peer.Handshake-send-StatusMsg")
	defer span.End()

	// Send out own handshake in a new thread
	errc := make(chan error, 2)
	var status statusData // safe to read after two values have been received from errc

	go func() {
		errc <- p2p.SendCtx(ctx, p.rw, StatusMsg, &statusData{
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
			span.SetStatus(trace.Status{
				Code:    trace.StatusCodeDeadlineExceeded,
				Message: p2p.DiscReadTimeout.Error(),
			})
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
// peer is already known. If a new peer is registered, its broadcast loop is also
// started.
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
	go p.broadcast()
	return nil
}

// Unregister removes a remote peer from the active set, disabling any further
// actions to/from that particular entity.
func (ps *peerSet) Unregister(id string) error {
	ps.lock.Lock()
	defer ps.lock.Unlock()
	p, ok := ps.peers[id]
	if !ok {
		return errNotRegistered
	}
	delete(ps.peers, id)
	p.Close()
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
// their set of known hashes. cap is the total number of peers.
func (ps *peerSet) PeersWithoutBlock(ctx context.Context, hash common.Hash) []*peer {
	ctx, span := trace.StartSpan(ctx, "peerSet.PeersWithoutBlock")
	defer span.End()

	ps.lock.RLock()
	defer ps.lock.RUnlock()

	l := len(ps.peers)

	span.AddAttributes(trace.Int64Attribute("peers", int64(l)))

	list := make([]*peer, 0, l)
	for _, p := range ps.peers {
		if !p.knownBlocks.Has(hash) {
			list = append(list, p)
		}
	}
	return list
}

// PeersWithoutTxs retrieves a map of peers to transactions from txs which are not in their set of known hashes.
// Each transaction will be included in the lists of, at most, square root of total peers.
func (ps *peerSet) PeersWithoutTxs(ctx context.Context, txs types.Transactions) map[*peer]types.Transactions {
	ctx, span := trace.StartSpan(ctx, "peerSet.PeersWithoutTxs")
	defer span.End()
	span.AddAttributes(trace.Int64Attribute("txs", int64(len(txs))))

	peerTxs := make(map[*peer]types.Transactions)
	tracing := log.Tracing()

	ps.lock.RLock()
	defer ps.lock.RUnlock()

	span.AddAttributes(trace.Int64Attribute("peers", int64(len(ps.peers))))

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
		if tracing && count > 0 {
			log.Trace("Broadcast transaction", "hash", hash, "recipients", count)
		}
	}

	return peerTxs
}

// BestPeer retrieves the known peer with the currently highest total difficulty.
func (ps *peerSet) BestPeer(ctx context.Context) *peer {
	ctx, span := trace.StartSpan(ctx, "peerSet.BestPeer")
	defer span.End()
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
