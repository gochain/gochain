package clique

import (
	"math/big"

	"github.com/gochain/gochain/v3/consensus"
	"github.com/gochain/gochain/v3/core/state"
	"github.com/gochain/gochain/v3/core/types"
)

// BlockReward is the reward in wei distributed each block.
var BlockReward = big.NewInt(7e+18)

// Finalize implements consensus.Engine, ensuring no uncles are set, but this does give rewards.
func (_ *Clique) Finalize(chain consensus.ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, receipts []*types.Receipt, block bool) *types.Block {
	cfg := chain.Config()
	signerReward := BlockReward
	if cfg.IsHafthor(header.Number) {
		// Split the reward for staking.
		signerReward = new(big.Int).Rsh(BlockReward, 1)            // half
		stakeReward := new(big.Int).Sub(BlockReward, signerReward) // difference so that total is exactly BlockReward
		// Reward the stakers.
		state.AddBalance(cfg.HafthorStakeAddress, stakeReward)
	}
	// Reward the signer.
	state.AddBalance(header.Coinbase, signerReward)

	header.Root = state.IntermediateRoot(cfg.IsEIP158(header.Number))
	header.UncleHash = types.CalcUncleHash(nil)

	if block {
		// Assemble and return the final block for sealing
		return types.NewBlock(header, txs, nil, receipts)
	}
	return nil
}
