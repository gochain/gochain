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

// Package netstats implements the network stats reporting service.
package netstats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.opencensus.io/trace"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/common/mclock"
	"github.com/gochain-io/gochain/v3/consensus"
	"github.com/gochain-io/gochain/v3/core"
	"github.com/gochain-io/gochain/v3/core/types"
	"github.com/gochain-io/gochain/v3/eth"
	"github.com/gochain-io/gochain/v3/les"
	"github.com/gochain-io/gochain/v3/log"
	"github.com/gochain-io/gochain/v3/p2p"
	"github.com/gochain-io/gochain/v3/rpc"
	"golang.org/x/net/websocket"
)

const (
	// historyUpdateRange is the number of blocks a node should report upon login or
	// history request.
	historyUpdateRange = 50
)

type txPool interface {
	SubscribeNewTxsEvent(chan<- core.NewTxsEvent, string)
	UnsubscribeNewTxsEvent(chan<- core.NewTxsEvent)
}

type blockChain interface {
	SubscribeChainHeadEvent(chan<- core.ChainHeadEvent, string)
	UnsubscribeChainHeadEvent(chan<- core.ChainHeadEvent)
}

// Service implements an GoChain netstats reporting daemon that pushes local
// chain statistics up to a monitoring server.
type Service struct {
	server *p2p.Server       // Peer-to-peer server to retrieve networking infos
	eth    *eth.GoChain      // Full GoChain service if monitoring a full node
	les    *les.LightGoChain // Light GoChain service if monitoring a light node
	engine consensus.Engine  // Consensus engine to retrieve variadic block fields

	node string // Name of the node to display on the monitoring page
	pass string // Password to authorize access to the monitoring page
	host string // Remote address of the monitoring service

	pongCh chan struct{} // Pong notifications are fed into this channel
	histCh chan []uint64 // History request block numbers are fed into this channel
}

type Config struct {
	Name   string `toml:",omitempty"`
	Secret string `toml:",omitempty"`
	URL    string `toml:",omitempty"`
}

// ParseConfig parses the netstats flag. It has the form "nodename:secret@host:port".
func ParseConfig(flag string) (Config, error) {
	re := regexp.MustCompile("([^:@]*)(:([^@]*))?@(.+)")
	parts := re.FindStringSubmatch(flag)
	if len(parts) != 5 {
		return Config{}, fmt.Errorf("invalid netstats flag: \"%s\", should be nodename:secret@host:port", flag)
	}
	return Config{
		Name:   parts[1],
		Secret: parts[3],
		URL:    parts[4],
	}, nil
}

// New returns a monitoring service ready for stats reporting.
func New(cfg Config, ethServ *eth.GoChain, lesServ *les.LightGoChain) *Service {
	// Assemble and return the stats service
	var engine consensus.Engine
	if ethServ != nil {
		engine = ethServ.Engine()
	} else {
		engine = lesServ.Engine()
	}
	return &Service{
		eth:    ethServ,
		les:    lesServ,
		engine: engine,
		node:   cfg.Name,
		pass:   cfg.Secret,
		host:   cfg.URL,
		pongCh: make(chan struct{}),
		histCh: make(chan []uint64, 1),
	}
}

// Protocols implements node.Service, returning the P2P network protocols used
// by the stats service (nil as it doesn't use the devp2p overlay network).
func (s *Service) Protocols() []p2p.Protocol { return nil }

// APIs implements node.Service, returning the RPC API endpoints provided by the
// stats service (nil as it doesn't provide any user callable APIs).
func (s *Service) APIs() []rpc.API { return nil }

// Start implements node.Service, starting up the monitoring and reporting daemon.
func (s *Service) Start(server *p2p.Server) error {
	s.server = server
	go s.loop()

	log.Info("Stats daemon started")
	return nil
}

// Stop implements node.Service, terminating the monitoring and reporting daemon.
func (s *Service) Stop() error {
	log.Info("Stats daemon stopped")
	return nil
}

// loop keeps trying to connect to the netstats server, reporting chain events
// until termination.
func (s *Service) loop() {
	// Subscribe to chain events to execute updates on
	var blockchain blockChain
	var txpool txPool
	if s.eth != nil {
		blockchain = s.eth.BlockChain()
		txpool = s.eth.TxPool()
	} else {
		blockchain = s.les.BlockChain()
		txpool = s.les.TxPool()
	}

	// Start a goroutine that exhausts the subscriptions to avoid events piling up
	var (
		chainHeadCh = make(chan core.ChainHeadEvent, 1)
		txEventCh   = make(chan core.NewTxsEvent, 1)
		quitCh      = make(chan struct{})
		headCh      = make(chan *types.Block, 1)
		txCh        = make(chan struct{}, 1)
	)
	blockchain.SubscribeChainHeadEvent(chainHeadCh, "netstats.Service")
	txpool.SubscribeNewTxsEvent(txEventCh, "netstats.Service")
	go func() {
		defer close(quitCh)
		defer blockchain.UnsubscribeChainHeadEvent(chainHeadCh)
		defer txpool.UnsubscribeNewTxsEvent(txEventCh)

		var lastTx mclock.AbsTime
		for {
			select {
			// Notify of chain head events.
			case head, ok := <-chainHeadCh:
				if !ok {
					return
				}
				select {
				case headCh <- head.Block:
				default:
				}

			// Notify of new transaction events, but drop if too frequent
			case _, ok := <-txEventCh:
				if !ok {
					return
				}
				if time.Duration(mclock.Now()-lastTx) < time.Second {
					continue
				}
				lastTx = mclock.Now()

				select {
				case txCh <- struct{}{}:
				default:
				}
			}
		}
	}()
	// Resolve the URL, defaulting to TLS, but falling back to none too
	path := fmt.Sprintf("%s/api", s.host)
	urls := []string{path}

	if !strings.Contains(path, "://") { // url.Parse and url.IsAbs is unsuitable (https://github.com/golang/go/issues/19779)
		urls = []string{"wss://" + path, "ws://" + path}
	}
	// Loop reporting until termination
	for {
		conn := s.connect(urls)
		if conn == nil {
			select {
			case <-quitCh:
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		// Respond to specific requests.
		go s.readLoop(conn)
		// Scheduled and triggered reports.
		s.reportLoop(conn, quitCh, txCh, headCh)
		select {
		case <-quitCh:
			return
		default:
		}
	}
}

// connect establishes a websocket connection to the server on any supported URL, or returns nil if unable.
func (s *Service) connect(urls []string) *websocket.Conn {
	var (
		conf *websocket.Config
		conn *websocket.Conn
		err  error
	)
	for _, url := range urls {
		if conf, err = websocket.NewConfig(url, "http://localhost/"); err != nil {
			continue
		}
		conf.Dialer = &net.Dialer{Timeout: 5 * time.Second}
		if conn, err = websocket.DialConfig(conf); err == nil {
			break
		}
	}
	if err != nil {
		log.Warn("Stats server unreachable", "err", err)
		return nil
	}
	// Authenticate the client with the server
	if err = s.login(conn); err != nil {
		log.Warn("Stats login failed", "err", err)
		conn.Close()
		return nil
	}
	return conn
}

// readLoop loops as long as the connection is alive and retrieves data packets
// from the network socket. If any of them match an active request, it forwards
// it, if they themselves are requests it initiates a reply, and lastly it drops
// unknown packets.
func (s *Service) readLoop(conn *websocket.Conn) {
	// If the read loop exists, close the connection
	defer conn.Close()

	for {
		// Retrieve the next generic network packet and bail out on error
		var msg map[string][]interface{}
		if err := websocket.JSON.Receive(conn, &msg); err != nil {
			log.Warn("Failed to decode stats server message", "err", err)
			return
		}
		log.Trace("Received message from stats server", "msg", msg)
		if len(msg["emit"]) == 0 {
			log.Warn("Stats server sent non-broadcast", "msg", msg)
			return
		}
		command, ok := msg["emit"][0].(string)
		if !ok {
			log.Warn("Invalid stats server message type", "type", msg["emit"][0])
			return
		}
		// If the message is a ping reply, deliver (someone must be listening!)
		if len(msg["emit"]) == 2 && command == "node-pong" {
			select {
			case s.pongCh <- struct{}{}:
				// Pong delivered, continue listening
				continue
			default:
				// Ping routine dead, abort
				log.Warn("Stats server pinger seems to have died")
				return
			}
		}
		// If the message is a history request, forward to the event processor
		if len(msg["emit"]) == 2 && command == "history" {
			// Make sure the request is valid and doesn't crash us
			request, ok := msg["emit"][1].(map[string]interface{})
			if !ok {
				log.Warn("Invalid stats history request", "msg", msg["emit"][1])
				s.histCh <- nil
				continue // Netstats sometime sends invalid history requests, ignore those
			}
			list, ok := request["list"].([]interface{})
			if !ok {
				log.Warn("Invalid stats history block list", "list", request["list"])
				return
			}
			// Convert the block number list to an integer list
			numbers := make([]uint64, len(list))
			for i, num := range list {
				n, ok := num.(float64)
				if !ok {
					log.Warn("Invalid stats history block number", "number", num)
					return
				}
				numbers[i] = uint64(n)
			}
			select {
			case s.histCh <- numbers:
				continue
			default:
			}
		}
		// Report anything else and continue
		log.Info("Unknown stats message", "msg", msg)
	}
}

// reportLoop sends status updates until the connection breaks.
func (s *Service) reportLoop(conn *websocket.Conn, quitCh, txCh <-chan struct{}, headCh <-chan *types.Block) {
	defer conn.Close()

	// Send the initial stats so our node looks decent from the get go
	if err := s.report(context.Background(), conn); err != nil {
		log.Warn("Initial stats report failed", "err", err)
		return
	}

	fullReport := time.NewTicker(5 * time.Second)
	defer fullReport.Stop()

	var err error
	for err == nil {
		select {
		case <-quitCh:
			return

		case <-fullReport.C:
			ctx, span := trace.StartSpan(context.Background(), "Service.reportLoop-fullReport")
			if err = s.report(ctx, conn); err != nil {
				log.Warn("Full stats report failed", "err", err)
			}
			span.End()
		case list := <-s.histCh:
			if err = s.reportHistory(conn, list); err != nil {
				log.Warn("Requested history report failed", "err", err)
			}
		case head := <-headCh:
			if err = s.reportBlock(conn, head); err != nil {
				log.Warn("Block stats report failed", "err", err)
			}
			if err = s.reportPending(conn); err != nil {
				log.Warn("Post-block transaction stats report failed", "err", err)
			}
		case <-txCh:
			if err = s.reportPending(conn); err != nil {
				log.Warn("Transaction stats report failed", "err", err)
			}
		}
	}
}

// nodeInfo is the collection of metainformation about a node that is displayed
// on the monitoring page.
type nodeInfo struct {
	Name     string `json:"name"`
	Node     string `json:"node"`
	Port     int    `json:"port"`
	Network  string `json:"net"`
	Protocol string `json:"protocol"`
	API      string `json:"api"`
	Os       string `json:"os"`
	OsVer    string `json:"os_v"`
	Client   string `json:"client"`
	History  bool   `json:"canUpdateHistory"`
}

// authMsg is the authentication infos needed to login to a monitoring server.
type authMsg struct {
	Id     string   `json:"id"`
	Info   nodeInfo `json:"info"`
	Secret string   `json:"secret"`
}

// login tries to authorize the client at the remote server.
func (s *Service) login(conn *websocket.Conn) error {
	// Construct and send the login authentication
	infos := s.server.NodeInfo()

	var network, protocol string
	if info := infos.Protocols["eth"]; info != nil {
		network = fmt.Sprintf("%d", info.(*eth.NodeInfo).Network)
		protocol = fmt.Sprintf("eth/%d", eth.ProtocolVersions[0])
	} else {
		network = fmt.Sprintf("%d", infos.Protocols["les"].(*les.NodeInfo).Network)
		protocol = fmt.Sprintf("les/%d", les.ClientProtocolVersions[0])
	}
	auth := &authMsg{
		Id: s.node,
		Info: nodeInfo{
			Name:     s.node,
			Node:     infos.Name,
			Port:     infos.Ports.Listener,
			Network:  network,
			Protocol: protocol,
			API:      "No",
			Os:       runtime.GOOS,
			OsVer:    runtime.GOARCH,
			Client:   "0.1.1",
			History:  true,
		},
		Secret: s.pass,
	}
	login := map[string][]interface{}{
		"emit": {"hello", auth},
	}
	if err := websocket.JSON.Send(conn, login); err != nil {
		return err
	}
	// Retrieve the remote ack or connection termination
	var ack map[string][]string
	if err := websocket.JSON.Receive(conn, &ack); err != nil || len(ack["emit"]) != 1 || ack["emit"][0] != "ready" {
		return errors.New("unauthorized")
	}
	return nil
}

// report collects all possible data to report and send it to the stats server.
// This should only be used on reconnects or rarely to avoid overloading the
// server. Use the individual methods for reporting subscribed events.
func (s *Service) report(ctx context.Context, conn *websocket.Conn) error {
	ctx, span := trace.StartSpan(ctx, "Service.report")
	defer span.End()

	if err := s.reportLatency(ctx, conn); err != nil {
		return err
	}
	if err := s.reportBlock(conn, nil); err != nil {
		return err
	}
	if err := s.reportPending(conn); err != nil {
		return err
	}
	if err := s.reportStats(ctx, conn); err != nil {
		return err
	}
	return nil
}

// reportLatency sends a ping request to the server, measures the RTT time and
// finally sends a latency update.
func (s *Service) reportLatency(ctx context.Context, conn *websocket.Conn) error {
	ctx, span := trace.StartSpan(ctx, "Service.reportLatency")
	defer span.End()

	// Send the current time to the netstats server
	start := time.Now()

	ping := map[string][]interface{}{
		"emit": {"node-ping", map[string]string{
			"id":         s.node,
			"clientTime": start.String(),
		}},
	}
	if err := websocket.JSON.Send(conn, ping); err != nil {
		return err
	}
	// Wait for the pong request to arrive back
	select {
	case <-s.pongCh:
		// Pong delivered, report the latency
	case <-time.After(5 * time.Second):
		// Ping timeout, abort
		return errors.New("ping timed out")
	}
	latency := strconv.Itoa(int((time.Since(start) / time.Duration(2)).Nanoseconds() / 1000000))

	// Send back the measured latency
	log.Trace("Sending measured latency to netstats", "latency", latency)

	stats := map[string][]interface{}{
		"emit": {"latency", map[string]string{
			"id":      s.node,
			"latency": latency,
		}},
	}
	return websocket.JSON.Send(conn, stats)
}

// blockStats is the information to report about individual blocks.
type blockStats struct {
	Number     *big.Int       `json:"number"`
	Hash       common.Hash    `json:"hash"`
	ParentHash common.Hash    `json:"parentHash"`
	Timestamp  *big.Int       `json:"timestamp"`
	Miner      common.Address `json:"miner"`
	GasUsed    uint64         `json:"gasUsed"`
	GasLimit   uint64         `json:"gasLimit"`
	Diff       string         `json:"difficulty"`
	TotalDiff  string         `json:"totalDifficulty"`
	Txs        []txStats      `json:"transactions"`
	TxCount    uint64         `json:"transactionCount"`
	TxHash     common.Hash    `json:"transactionsRoot"`
	Root       common.Hash    `json:"stateRoot"`
	Uncles     uncleStats     `json:"uncles"`
}

// txStats is the information to report about individual transactions.
type txStats struct {
	Hash common.Hash `json:"hash"`
}

// uncleStats is a custom wrapper around an uncle array to force serializing
// empty arrays instead of returning null for them.
type uncleStats []*types.Header

func (s uncleStats) MarshalJSON() ([]byte, error) {
	if uncles := ([]*types.Header)(s); len(uncles) > 0 {
		return json.Marshal(uncles)
	}
	return []byte("[]"), nil
}

// reportBlock retrieves the current chain head and repors it to the stats server.
func (s *Service) reportBlock(conn *websocket.Conn, block *types.Block) error {
	// Gather the block details from the header or block chain
	details := s.assembleBlockStats(block)

	// Assemble the block report and send it to the server
	log.Trace("Sending new block to netstats", "number", details.Number, "hash", details.Hash)

	stats := map[string]interface{}{
		"id":    s.node,
		"block": details,
	}
	report := map[string][]interface{}{
		"emit": {"block", stats},
	}
	return websocket.JSON.Send(conn, report)
}

// assembleBlockStats retrieves any required metadata to report a single block
// and assembles the block stats. If block is nil, the current head is processed.
func (s *Service) assembleBlockStats(block *types.Block) *blockStats {
	// Gather the block infos from the local blockchain
	var (
		header  *types.Header
		td      *big.Int
		txs     []txStats
		txCount int
		uncles  []*types.Header
	)
	if s.eth != nil {
		// Full nodes have all needed information available
		if block == nil {
			block = s.eth.BlockChain().CurrentBlock()
		}
		header = block.Header()
		td = s.eth.BlockChain().GetTd(header.Hash(), header.Number.Uint64())

		txCount = len(block.Transactions())
		txs = make([]txStats, txCount)
		for i, tx := range block.Transactions() {
			txs[i].Hash = tx.Hash()
		}
		uncles = block.Uncles()
	} else {
		// Light nodes would need on-demand lookups for transactions/uncles, skip
		if block != nil {
			header = block.Header()
		} else {
			header = s.les.BlockChain().CurrentHeader()
		}
		td = s.les.BlockChain().GetTd(header.Hash(), header.Number.Uint64())
		txs = []txStats{}
	}
	// Assemble and return the block stats
	author, _ := s.engine.Author(header)

	return &blockStats{
		Number:     header.Number,
		Hash:       header.Hash(),
		ParentHash: header.ParentHash,
		Timestamp:  header.Time,
		Miner:      author,
		GasUsed:    header.GasUsed,
		GasLimit:   header.GasLimit,
		Diff:       header.Difficulty.String(),
		TotalDiff:  td.String(),
		TxCount:    uint64(txCount),
		Txs:        txs,
		TxHash:     header.TxHash,
		Root:       header.Root,
		Uncles:     uncles,
	}
}

// reportHistory retrieves the most recent batch of blocks and reports it to the
// stats server.
func (s *Service) reportHistory(conn *websocket.Conn, list []uint64) error {
	// Figure out the indexes that need reporting
	indexes := make([]uint64, 0, historyUpdateRange)
	if len(list) > 0 {
		// Specific indexes requested, send them back in particular
		indexes = append(indexes, list...)
	} else {
		// No indexes requested, send back the top ones
		var head int64
		if s.eth != nil {
			head = s.eth.BlockChain().CurrentHeader().Number.Int64()
		} else {
			head = s.les.BlockChain().CurrentHeader().Number.Int64()
		}
		start := head - historyUpdateRange + 1
		if start < 0 {
			start = 0
		}
		for i := uint64(start); i <= uint64(head); i++ {
			indexes = append(indexes, i)
		}
	}
	// Gather the batch of blocks to report
	history := make([]*blockStats, len(indexes))
	for i, number := range indexes {
		// Retrieve the next block if it's known to us
		var block *types.Block
		if s.eth != nil {
			block = s.eth.BlockChain().GetBlockByNumber(number)
		} else {
			if header := s.les.BlockChain().GetHeaderByNumber(number); header != nil {
				block = types.NewBlockWithHeader(header)
			}
		}
		// If we do have the block, add to the history and continue
		if block != nil {
			history[len(history)-1-i] = s.assembleBlockStats(block)
			continue
		}
		// Ran out of blocks, cut the report short and send
		history = history[len(history)-i:]
		break
	}
	// Assemble the history report and send it to the server
	if len(history) > 0 {
		log.Trace("Sending historical blocks to netstats", "first", history[0].Number, "last", history[len(history)-1].Number)
	} else {
		log.Trace("No history to send to stats server")
	}
	stats := map[string]interface{}{
		"id":      s.node,
		"history": history,
	}
	report := map[string][]interface{}{
		"emit": {"history", stats},
	}
	return websocket.JSON.Send(conn, report)
}

// pendStats is the information to report about pending transactions.
type pendStats struct {
	Pending int `json:"pending"`
}

// reportPending retrieves the current number of pending transactions and reports
// it to the stats server.
func (s *Service) reportPending(conn *websocket.Conn) error {
	// Retrieve the pending count from the local blockchain
	var pending int
	if s.eth != nil {
		pending, _ = s.eth.TxPool().Stats()
	} else {
		pending = s.les.TxPool().Stats()
	}
	// Assemble the transaction stats and send it to the server
	log.Trace("Sending pending transactions to netstats", "count", pending)

	stats := map[string]interface{}{
		"id": s.node,
		"stats": &pendStats{
			Pending: pending,
		},
	}
	report := map[string][]interface{}{
		"emit": {"pending", stats},
	}
	return websocket.JSON.Send(conn, report)
}

// nodeStats is the information to report about the local node.
type nodeStats struct {
	Active   bool `json:"active"`
	Syncing  bool `json:"syncing"`
	Mining   bool `json:"mining"`
	Hashrate int  `json:"hashrate"`
	Peers    int  `json:"peers"`
	GasPrice int  `json:"gasPrice"`
	Uptime   int  `json:"uptime"`
}

// reportPending retrieves various stats about the node at the networking and
// mining layer and reports it to the stats server.
func (s *Service) reportStats(ctx context.Context, conn *websocket.Conn) error {
	ctx, span := trace.StartSpan(ctx, "Service.reportStats")
	defer span.End()

	// Gather the syncing and mining infos from the local miner instance
	var (
		mining   bool
		syncing  bool
		gasprice int
	)
	if s.eth != nil {
		mining = s.eth.Miner().Mining()

		sync := s.eth.Downloader().Progress()
		syncing = s.eth.BlockChain().CurrentHeader().Number.Uint64() >= sync.HighestBlock

		price, _ := s.eth.APIBackend.SuggestPrice(context.Background())
		gasprice = int(price.Uint64())
	} else {
		sync := s.les.Downloader().Progress()
		syncing = s.les.BlockChain().CurrentHeader().Number.Uint64() >= sync.HighestBlock
	}
	// Assemble the node stats and send it to the server
	log.Trace("Sending node details to netstats")

	stats := map[string]interface{}{
		"id": s.node,
		"stats": &nodeStats{
			Active:   true,
			Mining:   mining,
			Peers:    s.server.PeerCount(),
			GasPrice: gasprice,
			Syncing:  syncing,
			Uptime:   100,
		},
	}
	report := map[string][]interface{}{
		"emit": {"stats", stats},
	}
	return websocket.JSON.Send(conn, report)
}
