package clique

import (
	"context"
	"math/big"

	"github.com/gochain-io/gochain/consensus"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
)

var (
	// Block reward in wei for successfully sealing a block.
	BlockReward = big.NewInt(7e+18)
)

// Finalize implements consensus.Engine, ensuring no uncles are set, but this does give rewards.
func (c *Clique) Finalize(ctx context.Context, chain consensus.ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, receipts []*types.Receipt, block bool) *types.Block {
	// Reward the signer.
	state.AddBalance(header.Coinbase, BlockReward)

	header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))
	header.UncleHash = types.CalcUncleHash(nil)

	if block {
		// Assemble and return the final block for sealing
		return types.NewBlock(header, txs, nil, receipts)
	}
	return nil
}

// Some weird constants to avoid constant memory allocs for them.
var (
	big8  = big.NewInt(8)
	big32 = big.NewInt(32)
)
