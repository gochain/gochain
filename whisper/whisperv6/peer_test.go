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
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	mrand "math/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/common/hexutil"
	"github.com/gochain-io/gochain/v3/crypto"
	"github.com/gochain-io/gochain/v3/p2p"
	"github.com/gochain-io/gochain/v3/p2p/discover"
	"github.com/gochain-io/gochain/v3/p2p/nat"
)

var keys = []string{
	"d49dcf37238dc8a7aac57dc61b9fee68f0a97f062968978b9fafa7d1033d03a9",
	"73fd6143c48e80ed3c56ea159fe7494a0b6b393a392227b422f4c3e8f1b54f98",
	"119dd32adb1daa7a4c7bf77f847fb28730785aa92947edf42fdd997b54de40dc",
	"deeda8709dea935bb772248a3144dea449ffcc13e8e5a1fd4ef20ce4e9c87837",
	"5bd208a079633befa349441bdfdc4d85ba9bd56081525008380a63ac38a407cf",
	"1d27fb4912002d58a2a42a50c97edb05c1b3dffc665dbaa42df1fe8d3d95c9b5",
	"15def52800c9d6b8ca6f3066b7767a76afc7b611786c1276165fbc61636afb68",
	"51be6ab4b2dc89f251ff2ace10f3c1cc65d6855f3e083f91f6ff8efdfd28b48c",
	"ef1ef7441bf3c6419b162f05da6037474664f198b58db7315a6f4de52414b4a0",
	"09bdf6985aabc696dc1fbeb5381aebd7a6421727343872eb2fadfc6d82486fd9",
	"15d811bf2e01f99a224cdc91d0cf76cea08e8c67905c16fee9725c9be71185c4",
	"2f83e45cf1baaea779789f755b7da72d8857aeebff19362dd9af31d3c9d14620",
	"73f04e34ac6532b19c2aae8f8e52f38df1ac8f5cd10369f92325b9b0494b0590",
	"1e2e07b69e5025537fb73770f483dc8d64f84ae3403775ef61cd36e3faf162c1",
	"8963d9bbb3911aac6d30388c786756b1c423c4fbbc95d1f96ddbddf39809e43a",
	"0422da85abc48249270b45d8de38a4cc3c02032ede1fcf0864a51092d58a2f1f",
	"8ae5c15b0e8c7cade201fdc149831aa9b11ff626a7ffd27188886cc108ad0fa8",
	"acd8f5a71d4aecfcb9ad00d32aa4bcf2a602939b6a9dd071bab443154184f805",
	"a285a922125a7481600782ad69debfbcdb0316c1e97c267aff29ef50001ec045",
	"28fd4eee78c6cd4bf78f39f8ab30c32c67c24a6223baa40e6f9c9a0e1de7cef5",
	"c5cca0c9e6f043b288c6f1aef448ab59132dab3e453671af5d0752961f013fc7",
	"46df99b051838cb6f8d1b73f232af516886bd8c4d0ee07af9a0a033c391380fd",
	"c6a06a53cbaadbb432884f36155c8f3244e244881b5ee3e92e974cfa166d793f",
	"783b90c75c63dc72e2f8d11b6f1b4de54d63825330ec76ee8db34f06b38ea211",
	"9450038f10ca2c097a8013e5121b36b422b95b04892232f930a29292d9935611",
	"e215e6246ed1cfdcf7310d4d8cdbe370f0d6a8371e4eb1089e2ae05c0e1bc10f",
	"487110939ed9d64ebbc1f300adeab358bc58875faf4ca64990fbd7fe03b78f2b",
	"824a70ea76ac81366da1d4f4ac39de851c8ac49dca456bb3f0a186ceefa269a5",
	"ba8f34fa40945560d1006a328fe70c42e35cc3d1017e72d26864cd0d1b150f15",
	"30a5dfcfd144997f428901ea88a43c8d176b19c79dde54cc58eea001aa3d246c",
	"de59f7183aca39aa245ce66a05245fecfc7e2c75884184b52b27734a4a58efa2",
	"92629e2ff5f0cb4f5f08fffe0f64492024d36f045b901efb271674b801095c5a",
	"7184c1701569e3a4c4d2ddce691edd983b81e42e09196d332e1ae2f1e062cff4",
}

type TestData struct {
	counter [NumNodes]int
	mutex   sync.RWMutex
}

type TestNode struct {
	shh     *Whisper
	id      *ecdsa.PrivateKey
	server  *p2p.Server
	filerID string
}

const NumNodes = 8 // must not exceed the number of keys (32)

var result TestData
var nodes [NumNodes]*TestNode
var sharedKey = hexutil.MustDecode("0x03ca634cae0d49acb401d8a4c6b6fe8c55b70d115bf400769cc1400f3258cd31")
var wrongKey = hexutil.MustDecode("0xf91156714d7ec88d3edc1c652c2181dbb3044e8771c683f3b30d33c12b986b11")
var sharedTopic = TopicType{0xF, 0x1, 0x2, 0}
var wrongTopic = TopicType{0, 0, 0, 0}
var expectedMessage = []byte("per aspera ad astra")
var unexpectedMessage = []byte("per rectum ad astra")
var masterBloomFilter []byte
var masterPow = 0.00000001
var round = 1
var debugMode = false
var prevTime time.Time
var cntPrev int

func TestSimulation(t *testing.T) {
	// create a chain of whisper nodes,
	// installs the filters with shared (predefined) parameters
	initialize(t)
	if err := startServers(); err != nil {
		t.Fatal(err)
	}
	defer stopServers()

	// each node sends one random (not decryptable) message
	for i := 0; i < NumNodes; i++ {
		if err := sendMsg(false, i); err != nil {
			t.Error(err)
		}
	}

	// node #0 sends one expected (decryptable) message
	if err := sendMsg(true, 0); err != nil {
		t.Error(err)
	}

	// check if each node have received and decrypted exactly one message
	if err := checkPropagation(true); err != nil {
		t.Error(err)
	}

	const iterations = 200
	const sleep = 50 * time.Millisecond
	// check if Status message was correctly decoded
	if err := try(iterations, sleep, checkBloomFilterExchange); err != nil {
		t.Error(err)
	}
	if err := checkPowExchange(); err != nil {
		t.Error(err)
	}

	// send new pow and bloom exchange messages
	resetParams()

	// node #1 sends one expected (decryptable) message
	if err := sendMsg(true, 1); err != nil {
		t.Error(err)
	}

	// check if each node (except node #0) have received and decrypted exactly one message
	if err := checkPropagation(false); err != nil {
		t.Error(err)
	}

	// check if corresponding protocol-level messages were correctly decoded
	if err := try(iterations, sleep, checkPowExchangeForNodeZero); err != nil {
		t.Error(err)
	}
	if err := try(iterations, sleep, checkBloomFilterExchange); err != nil {
		t.Error(err)
	}
}

func resetParams() {
	// change pow only for node zero
	masterPow = 7777777.0
	nodes[0].shh.SetMinimumPoW(context.Background(), masterPow)

	// change bloom for all nodes
	masterBloomFilter = TopicToBloom(sharedTopic)
	for i := 0; i < NumNodes; i++ {
		nodes[i].shh.SetBloomFilter(context.Background(), masterBloomFilter)
	}

	round++
}

func initBloom(t *testing.T) {
	masterBloomFilter = make([]byte, bloomFilterSize)
	_, err := mrand.Read(masterBloomFilter)
	if err != nil {
		t.Fatalf("rand failed: %s.", err)
	}

	msgBloom := TopicToBloom(sharedTopic)
	masterBloomFilter = addBloom(masterBloomFilter, msgBloom)
	for i := 0; i < 32; i++ {
		masterBloomFilter[i] = 0xFF
	}

	if !bloomFilterMatch(masterBloomFilter, msgBloom) {
		t.Fatalf("bloom mismatch on initBloom.")
	}
}

func initialize(t *testing.T) {
	initBloom(t)

	var err error
	ip := net.IPv4(127, 0, 0, 1)
	port0 := 30403

	for i := 0; i < NumNodes; i++ {
		var node TestNode
		b := make([]byte, bloomFilterSize)
		copy(b, masterBloomFilter)
		node.shh = New(&DefaultConfig)
		node.shh.SetMinimumPoW(context.Background(), masterPow)
		node.shh.SetBloomFilter(context.Background(), b)
		if !bytes.Equal(node.shh.BloomFilter(), masterBloomFilter) {
			t.Fatalf("bloom mismatch on init.")
		}
		node.shh.Start(nil)
		topics := make([]TopicType, 0)
		topics = append(topics, sharedTopic)
		f := Filter{KeySym: sharedKey}
		f.Topics = [][]byte{topics[0][:]}
		node.filerID, err = node.shh.Subscribe(&f)
		if err != nil {
			t.Fatalf("failed to install the filter: %s.", err)
		}
		node.id, err = crypto.HexToECDSA(keys[i])
		if err != nil {
			t.Fatalf("failed convert the key: %s.", keys[i])
		}
		port := port0 + i
		addr := fmt.Sprintf(":%d", port) // e.g. ":30303"
		name := common.MakeName("whisper-go", "2.0")
		var peers []*discover.Node
		if i > 0 {
			peerNodeID := nodes[i-1].id
			peerPort := uint16(port - 1)
			peerNode := discover.PubkeyID(&peerNodeID.PublicKey)
			peer := discover.NewNode(peerNode, ip, peerPort, peerPort)
			peers = append(peers, peer)
		}

		node.server = &p2p.Server{
			Config: p2p.Config{
				PrivateKey:     node.id,
				MaxPeers:       NumNodes/2 + 1,
				Name:           name,
				Protocols:      node.shh.Protocols(),
				ListenAddr:     addr,
				NAT:            nat.Any(),
				BootstrapNodes: peers,
				StaticNodes:    peers,
				TrustedNodes:   peers,
			},
		}

		nodes[i] = &node
	}
}
func startServers() error {
	resp := make(chan error, NumNodes)
	for i := 0; i < NumNodes; i++ {
		go func(i int) {
			resp <- nodes[i].server.Start()
		}(i)
	}
	done := make(chan error)
	go func() {
		defer close(done)
		var cnt int
		for err := range resp {
			if err != nil {
				done <- err
				return
			}
			cnt++
			if cnt == NumNodes {
				return
			}
		}

	}()
	select {
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout waiting to start servers")
	case err := <-done:
		return err
	}
}

func stopServers() {
	for i := 0; i < NumNodes; i++ {
		n := nodes[i]
		if n != nil {
			n.shh.Unsubscribe(n.filerID)
			n.shh.Stop()
			n.server.Stop()
		}
	}
}

func checkPropagation(includingNodeZero bool) error {
	prevTime = time.Now()
	// (cycle * iterations) should not exceed 50 seconds, since TTL=50
	const cycle = 200 // time in milliseconds
	const iterations = 250

	first := 0
	if !includingNodeZero {
		first = 1
	}

	for j := 0; j < iterations; j++ {
		for i := first; i < NumNodes; i++ {
			f := nodes[i].shh.GetFilter(nodes[i].filerID)
			if f == nil {
				return fmt.Errorf("failed to get filterId %s from node %d, round %d", nodes[i].filerID, i, round)
			}

			mail := f.Retrieve()
			if err := validateMail(i, mail); err != nil {
				return err
			}

			if isTestComplete() {
				checkTestStatus()
				return nil
			}
		}

		checkTestStatus()
		time.Sleep(cycle * time.Millisecond)
	}

	if !includingNodeZero {
		f := nodes[0].shh.GetFilter(nodes[0].filerID)
		if f != nil {
			return fmt.Errorf("node zero received a message with low PoW")
		}
	}

	return fmt.Errorf("test was not complete (%d round): timeout %d seconds. nodes=%v", round, iterations*cycle/1000, nodes)
}

func validateMail(index int, mail []*ReceivedMessage) error {
	var cnt int
	for _, m := range mail {
		if bytes.Equal(m.Payload, expectedMessage) {
			cnt++
		}
	}

	if cnt == 0 {
		// no messages received yet: nothing is wrong
		return nil
	}
	if cnt > 1 {
		return fmt.Errorf("node %d received %d.", index, cnt)
	}

	if cnt == 1 {
		result.mutex.Lock()
		defer result.mutex.Unlock()
		result.counter[index] += cnt
		if result.counter[index] > 1 {
			return fmt.Errorf("node %d accumulated %d.", index, result.counter[index])
		}
	}
	return nil
}

func checkTestStatus() {
	var cnt int
	var arr [NumNodes]int

	for i := 0; i < NumNodes; i++ {
		arr[i] = nodes[i].server.PeerCount()
		envelopes := nodes[i].shh.Envelopes()
		if len(envelopes) >= NumNodes {
			cnt++
		}
	}

	if debugMode {
		if cntPrev != cnt {
			fmt.Printf(" %v \t number of nodes that have received all msgs: %d, number of peers per node: %v \n",
				time.Since(prevTime), cnt, arr)
			prevTime = time.Now()
			cntPrev = cnt
		}
	}
}

func isTestComplete() bool {
	result.mutex.RLock()
	defer result.mutex.RUnlock()

	for i := 0; i < NumNodes; i++ {
		if result.counter[i] < 1 {
			return false
		}
	}

	for i := 0; i < NumNodes; i++ {
		envelopes := nodes[i].shh.Envelopes()
		if len(envelopes) < NumNodes+1 {
			return false
		}
	}

	return true
}

func sendMsg(expected bool, id int) error {
	opt := MessageParams{KeySym: sharedKey, Topic: sharedTopic, Payload: expectedMessage, PoW: 0.00000001, WorkTime: 1}
	if !expected {
		opt.KeySym = wrongKey
		opt.Topic = wrongTopic
		opt.Payload = unexpectedMessage
		opt.Payload[0] = byte(id)
	}

	msg, err := NewSentMessage(&opt)
	if err != nil {
		return fmt.Errorf("failed to create new message with seed %d: %s", seed, err)
	}
	envelope, err := msg.Wrap(&opt)
	if err != nil {
		return fmt.Errorf("failed to seal message: %s", err)
	}

	err = nodes[id].shh.Send(envelope)
	if err != nil {
		return fmt.Errorf("failed to send message: %s", err)
	}
	return nil
}

func TestPeerBasic(t *testing.T) {
	InitSingleTest()

	params, err := generateMessageParams()
	if err != nil {
		t.Fatalf("failed generateMessageParams with seed %d.", seed)
	}

	params.PoW = 0.001
	msg, err := NewSentMessage(params)
	if err != nil {
		t.Fatalf("failed to create new message with seed %d: %s.", seed, err)
	}
	env, err := msg.Wrap(params)
	if err != nil {
		t.Fatalf("failed Wrap with seed %d.", seed)
	}

	p := newPeer(nil, nil, nil)
	p.mark(env)
	if !p.marked(env) {
		t.Fatalf("failed mark with seed %d.", seed)
	}
}

func try(iterations int, sleep time.Duration, fn func() error) (err error) {
	for j := 0; j < iterations; j++ {
		if err = fn(); err != nil {
			if j == iterations-1 {
				return err
			}
			time.Sleep(sleep)
			continue
		}
		return nil
	}
	return
}

func checkPowExchangeForNodeZero() error {
	cnt := 0
	for i, node := range nodes {
		for _, peer := range node.shh.getPeers() {
			fmt.Println(peer.peer.ID().String())
			if peer.peer.ID() == discover.PubkeyID(&nodes[0].id.PublicKey) {
				cnt++
				peer.mu.RLock()
				pow := peer.powRequirement
				peer.mu.RUnlock()
				if pow != masterPow {
					return fmt.Errorf("node %d: failed to set the new pow requirement for node zero", i)
				}
			}
		}
	}
	if cnt == 0 {
		return fmt.Errorf("looking for node zero: no matching peers found")
	}
	return nil
}

func checkPowExchange() error {
	for i, node := range nodes {
		for _, peer := range node.shh.getPeers() {
			if peer.peer.ID() != discover.PubkeyID(&nodes[0].id.PublicKey) {
				peer.mu.RLock()
				pow := peer.powRequirement
				peer.mu.RUnlock()
				if pow != masterPow {
					return fmt.Errorf("node %d: failed to exchange pow requirement in round %d; expected %f, got %f",
						i, round, masterPow, pow)
				}
			}
		}
	}
	return nil
}

func checkBloomFilterExchange() error {
	for i, node := range nodes {
		for _, peer := range node.shh.getPeers() {
			peer.mu.RLock()
			bf := peer.bloomFilter
			peer.mu.RUnlock()
			if !bytes.Equal(bf, masterBloomFilter) {
				return fmt.Errorf("node %d: failed to exchange bloom filter requirement in round %d. \n%x expected \n%x got",
					i, round, masterBloomFilter, bf)
			}
		}
	}

	return nil
}
