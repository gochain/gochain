// Copyright 2017 The go-ethereum Authors
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

package clique

import (
	"context"

	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/consensus"
	"github.com/gochain/gochain/v5/core/types"
	"github.com/gochain/gochain/v5/rpc"
)

// API is a user facing RPC API to allow controlling the signer and voting
// mechanisms of the proof-of-authority scheme.
type API struct {
	chain  consensus.ChainReader
	clique *Clique
}

// GetSnapshot retrieves the state snapshot at a given block.
func (api *API) GetSnapshot(ctx context.Context, number *rpc.BlockNumber) (*Snapshot, error) {
	// Retrieve the requested block number (or current if none requested)
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	// Ensure we have an actually valid block and return its snapshot
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.clique.snapshot(api.chain, header.Number.Uint64(), header.Hash(), nil)
}

// GetSnapshotAtHash retrieves the state snapshot at a given block.
func (api *API) GetSnapshotAtHash(ctx context.Context, hash common.Hash) (*Snapshot, error) {
	header := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.clique.snapshot(api.chain, header.Number.Uint64(), header.Hash(), nil)
}

// GetSigners retrieves the list of authorized signers at the specified block.
func (api *API) GetSigners(ctx context.Context, number *rpc.BlockNumber) ([]common.Address, error) {
	// Retrieve the requested block number (or current if none requested)
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	// Ensure we have an actually valid block and return the signers from its snapshot
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.clique.snapshot(api.chain, header.Number.Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	return snap.signers(), nil
}

// GetVoters retrieves the list of authorized voters at the specified block.
func (api *API) GetVoters(ctx context.Context, number *rpc.BlockNumber) ([]common.Address, error) {
	// Retrieve the requested block number (or current if none requested)
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	// Ensure we have an actually valid block and return the signers from its snapshot
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.clique.snapshot(api.chain, header.Number.Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	return snap.voters(), nil
}

// GetSignersAtHash retrieves the state snapshot at a given block.
func (api *API) GetSignersAtHash(ctx context.Context, hash common.Hash) ([]common.Address, error) {
	header := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.clique.snapshot(api.chain, header.Number.Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	return snap.signers(), nil
}

// GetVotersAtHash retrieves the list of authorized voters at the specified block hash.
func (api *API) GetVotersAtHash(ctx context.Context, hash common.Hash) ([]common.Address, error) {
	header := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.clique.snapshot(api.chain, header.Number.Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	return snap.voters(), nil
}

// Status is the status of a clique node's signing activity over the latest epoch (or last 64 blocks).
type Status struct {
	InturnPercent float64                `json:"inturnPercent"`
	SigningStatus map[common.Address]int `json:"sealerActivity"`
	NumBlocks     uint64                 `json:"numBlocks"`
	Signers       []common.Address       `json:"signers"`
	Stakers       []common.Address       `json:"stakers"`
}

// Status returns the status of the clique consensus over the latest epoch (or last 64 blocks).
func (api *API) Status(ctx context.Context) (*Status, error) {
	var (
		numBlocks = uint64(64)
		header    = api.chain.CurrentHeader()
		optimals  = 0
	)
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.clique.snapshot(api.chain, header.Number.Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	var (
		signers = snap.signers()
		end     = header.Number.Uint64()
		start   = uint64(1)
	)
	if end == 0 {
		numBlocks = 0
	} else if numBlocks > end {
		numBlocks = end
		start = 1
	} else {
		start = end - numBlocks + 1
	}
	signStatus := make(map[common.Address]int)
	for _, s := range signers {
		signStatus[s] = 0
	}
	var processedBlocks uint64
	for n := start; n <= end; n++ {
		h := api.chain.GetHeaderByNumber(n)
		if h == nil {
			continue
		}
		processedBlocks++
		if h.Difficulty.Uint64() == uint64(len(signers)) {
			optimals++
		}
		sealer, err := api.clique.Author(h)
		if err != nil {
			return nil, err
		}
		signStatus[sealer]++
	}
	var inturnPercent float64
	if processedBlocks > 0 {
		inturnPercent = float64(100*optimals) / float64(processedBlocks)
	}
	return &Status{
		InturnPercent: inturnPercent,
		SigningStatus: signStatus,
		NumBlocks:     processedBlocks,
		Signers:       signers,
		Stakers:       snap.voters(),
	}, nil
}

// Proposals returns the current proposals the node tries to uphold and vote on.
func (api *API) Proposals() map[common.Address]propose {
	api.clique.lock.RLock()
	defer api.clique.lock.RUnlock()

	proposals := make(map[common.Address]propose)
	for address, propose := range api.clique.proposals {
		proposals[address] = propose
	}
	return proposals
}

// Propose injects a new authorization proposal that the signer will attempt to
// push through.
func (api *API) Propose(address common.Address, auth bool) {
	api.clique.lock.Lock()
	defer api.clique.lock.Unlock()

	api.clique.proposals[address] = propose{Authorize: auth, VoterElection: false}
}

// Propose injects a new authorization proposal that the signer will attempt to
// push through.
func (api *API) ProposeVoter(address common.Address, auth bool) {
	api.clique.lock.Lock()
	defer api.clique.lock.Unlock()

	api.clique.proposals[address] = propose{Authorize: auth, VoterElection: true}
}

// Discard drops a currently running proposal, stopping the signer from casting
// further votes (either for or against).
func (api *API) Discard(address common.Address) {
	api.clique.lock.Lock()
	defer api.clique.lock.Unlock()

	delete(api.clique.proposals, address)
}

