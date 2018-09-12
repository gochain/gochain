package clique

import (
	"context"

	"github.com/gochain-io/gochain/consensus"
	"github.com/gochain-io/gochain/core"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
)

// Finalize implements consensus.Engine, ensuring no uncles are set, but this does give rewards.
func (c *Clique) Finalize(ctx context.Context, chain consensus.ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, receipts []*types.Receipt, block bool) *types.Block {
	// Reward the signer.
	state.AddBalance(header.Coinbase, core.BlockReward)

	header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))
	header.UncleHash = types.CalcUncleHash(nil)

	if block {
		// Assemble and return the final block for sealing
		return types.NewBlock(header, txs, nil, receipts)
	}
	return nil
}
