package clique

import (
	"context"
	"math/big"

	"github.com/gochain-io/gochain/v3/consensus"
	"github.com/gochain-io/gochain/v3/core/state"
	"github.com/gochain-io/gochain/v3/core/types"
)

// Block reward in wei for successfully sealing a block.
var BlockReward = big.NewInt(7e+18)

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
