package clique

import (
	"math/big"

	"github.com/gochain-io/gochain/consensus"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/params"
)

// Finalize implements consensus.Engine, ensuring no uncles are set, but this does give rewards.
func (c *Clique) Finalize(chain consensus.ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt) (*types.Block, error) {
	uncles = nil // TODO: or do we want uncles?
	accumulateRewards(chain.Config(), state, header, uncles)
	header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))
	header.UncleHash = types.CalcUncleHash(uncles)

	// Assemble and return the final block for sealing
	return types.NewBlock(header, txs, uncles, receipts), nil
}

// // Finalize implements consensus.Engine, accumulating the block and uncle rewards,
// // setting the final state and assembling the block.
// func (ethash *Ethash) Finalize(chain consensus.ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt) (*types.Block, error) {
// 	// Accumulate any block and uncle rewards and commit the final state root
// 	accumulateRewards(chain.Config(), state, header, uncles)
// 	header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))

// 	// Header seems complete, assemble into a block and return
// 	return types.NewBlock(header, txs, uncles, receipts), nil
// }

// Some weird constants to avoid constant memory allocs for them.
var (
	big8  = big.NewInt(8)
	big32 = big.NewInt(32)
)
var (
	// GochainBlockReward Block reward in wei for successfully sealing a block
	GochainBlockReward = big.NewInt(1e+18)
)

// AccumulateRewards credits the coinbase of the given block with the mining
// reward. The total reward consists of the static block reward and rewards for
// included uncles. The coinbase of each uncle block is also rewarded.
func accumulateRewards(config *params.ChainConfig, state *state.StateDB, header *types.Header, uncles []*types.Header) {
	// Select the correct block reward based on chain progression
	blockReward := GochainBlockReward
	// Accumulate the rewards for the miner and any included uncles
	reward := new(big.Int).Set(blockReward)
	r := new(big.Int)
	if uncles != nil {
		for _, uncle := range uncles {
			r.Add(uncle.Number, big8)
			r.Sub(r, header.Number)
			r.Mul(r, blockReward)
			r.Div(r, big8)
			state.AddBalance(uncle.Coinbase, r)

			r.Div(blockReward, big32)
			reward.Add(reward, r)
		}
	}
	log.Info("Issuing", "reward:", reward.String())
	state.AddBalance(header.Coinbase, reward)
}
