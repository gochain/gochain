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

package core

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/gochain-io/gochain/common/hexutil"
	"github.com/gochain-io/gochain/consensus/clique"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/params"
)

// Tests that simple header verification works, for both good and bad blocks.
func TestHeaderVerification(t *testing.T) {
	ctx := context.Background()

	// Create a simple chain to verify
	var (
		testdb = ethdb.NewMemDatabase()
		gspec  = &Genesis{
			Config: params.TestChainConfig,
			Signer: hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
		}
		genesis   = gspec.MustCommit(testdb)
		blocks, _ = GenerateChain(ctx, params.TestChainConfig, genesis, clique.NewFaker(), testdb, 8, nil)
	)
	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	// Run the header checker for blocks one-by-one, checking for both valid and invalid nonces
	chain, _ := NewBlockChain(ctx, testdb, nil, params.TestChainConfig, clique.NewFaker(), vm.Config{})
	defer chain.Stop()

	for i := 0; i < len(blocks); i++ {
		for j, valid := range []bool{true, false} {
			var results <-chan error

			if valid {
				engine := clique.NewFaker()
				_, results = engine.VerifyHeaders(ctx, chain, []*types.Header{headers[i]})
			} else {
				engine := clique.NewFakeFailer(headers[i].Number.Uint64())
				_, results = engine.VerifyHeaders(ctx, chain, []*types.Header{headers[i]})
			}
			// Wait for the verification result
			select {
			case err := <-results:
				if err == nil && !valid {
					t.Errorf("test %d.%d: expected error", i, j)
				} else if err != nil && valid {
					t.Errorf("test %d.%d: unexpected error: %v", i, j, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("test %d.%d: verification timeout", i, j)
			}
			// Make sure no more data is returned
			select {
			case result, ok := <-results:
				if ok {
					t.Fatalf("test %d.%d: unexpected result returned: %v", i, j, result)
				}
			case <-time.After(25 * time.Millisecond):
			}
		}
		chain.InsertChain(ctx, blocks[i:i+1])
	}
}

// Tests that concurrent header verification works, for both good and bad blocks.
func TestHeaderConcurrentVerification2(t *testing.T)  { testHeaderConcurrentVerification(t, 2) }
func TestHeaderConcurrentVerification8(t *testing.T)  { testHeaderConcurrentVerification(t, 8) }
func TestHeaderConcurrentVerification32(t *testing.T) { testHeaderConcurrentVerification(t, 32) }

func testHeaderConcurrentVerification(t *testing.T, threads int) {
	ctx := context.Background()
	// Create a simple chain to verify
	var (
		testdb = ethdb.NewMemDatabase()
		gspec  = &Genesis{
			Config: params.TestChainConfig,
			Signer: hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
		}
		genesis   = gspec.MustCommit(testdb)
		blocks, _ = GenerateChain(ctx, params.TestChainConfig, genesis, clique.NewFaker(), testdb, 8, nil)
	)
	headers := make([]*types.Header, len(blocks))
	seals := make([]bool, len(blocks))

	for i, block := range blocks {
		headers[i] = block.Header()
		seals[i] = true
	}
	// Set the number of threads to verify on
	old := runtime.GOMAXPROCS(threads)
	defer runtime.GOMAXPROCS(old)

	// Run the header checker for the entire block chain at once both for a valid and
	// also an invalid chain (enough if one arbitrary block is invalid).
	for i, valid := range []bool{true, false} {
		var results <-chan error

		if valid {
			chain, _ := NewBlockChain(ctx, testdb, nil, params.TestChainConfig, clique.NewFaker(), vm.Config{})
			_, results = chain.engine.VerifyHeaders(ctx, chain, headers)
			chain.Stop()
		} else {
			chain, _ := NewBlockChain(ctx, testdb, nil, params.TestChainConfig, clique.NewFakeFailer(uint64(len(headers)-1)), vm.Config{})
			_, results = chain.engine.VerifyHeaders(ctx, chain, headers)
			chain.Stop()
		}
		// Wait for all the verification results
		errs := make(map[int]error)
		for j := 0; j < len(blocks); j++ {
			select {
			case result := <-results:
				errs[j] = result

			case <-time.After(time.Second):
				t.Fatalf("test %d.%d: verification timeout", i, j)
			}
		}
		// Check nonce check validity
		for j := 0; j < len(blocks); j++ {
			expValid := valid || (j < len(blocks)-2) // We chose the last-but-one nonce in the chain to fail
			if expValid && errs[j] != nil {
				t.Errorf("test %d.%d: unexpected error: %v", i, j, errs[j])
			}
			if !expValid {
				if errs[j] == nil {
					t.Errorf("test %d.%d: expected error", i, j)
				}
				// A few blocks after the first error may pass verification due to concurrent
				// workers. We don't care about those in this test, just that the correct block
				// errors out.
				break
			}
		}
		// Make sure no more data is returned
		select {
		case result, ok := <-results:
			if ok {
				t.Fatalf("test %d: unexpected result returned: %v", i, result)
			}
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Tests that aborting a header validation indeed prevents further checks from being
// run, as well as checks that no left-over goroutines are leaked.
func TestHeaderConcurrentAbortion2(t *testing.T)  { testHeaderConcurrentAbortion(t, 2) }
func TestHeaderConcurrentAbortion8(t *testing.T)  { testHeaderConcurrentAbortion(t, 8) }
func TestHeaderConcurrentAbortion32(t *testing.T) { testHeaderConcurrentAbortion(t, 32) }

func testHeaderConcurrentAbortion(t *testing.T, threads int) {
	ctx := context.Background()
	// Create a simple chain to verify
	var (
		testdb    = ethdb.NewMemDatabase()
		gspec     = &Genesis{Config: params.TestChainConfig}
		genesis   = gspec.MustCommit(testdb)
		blocks, _ = GenerateChain(ctx, params.TestChainConfig, genesis, clique.NewFaker(), testdb, 1024, nil)
	)
	headers := make([]*types.Header, len(blocks))
	seals := make([]bool, len(blocks))

	for i, block := range blocks {
		headers[i] = block.Header()
		seals[i] = true
	}
	// Set the number of threads to verify on
	old := runtime.GOMAXPROCS(threads)
	defer runtime.GOMAXPROCS(old)

	// Start the verifications and immediately abort
	chain, _ := NewBlockChain(ctx, testdb, nil, params.TestChainConfig, clique.NewFakeDelayer(time.Millisecond), vm.Config{})
	defer chain.Stop()

	abort, results := chain.engine.VerifyHeaders(ctx, chain, headers)
	close(abort)

	// Deplete the results channel
	verified := 0
	for depleted := false; !depleted; {
		select {
		case result, ok := <-results:
			if !ok {
				depleted = true
				break
			}
			if result != nil {
				t.Errorf("header %d: validation failed: %v", verified, result)
			}
			verified++
		case <-time.After(50 * time.Millisecond):
			depleted = true
		}
	}
	// Check that abortion was honored by not processing too many POWs
	if verified > 2*threads {
		t.Errorf("verification count too large: have %d, want below %d", verified, 2*threads)
	}
}
