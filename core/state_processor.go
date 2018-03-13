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
	"runtime"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/consensus"
	"github.com/gochain-io/gochain/consensus/misc"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/crypto"
	"github.com/gochain-io/gochain/params"
)

// StateProcessor is a basic Processor, which takes care of transitioning
// state from one point to another.
//
// StateProcessor implements Processor.
type StateProcessor struct {
	config     *params.ChainConfig // Chain configuration options
	bc         *BlockChain         // Canonical block chain
	engine     consensus.Engine    // Consensus engine used for block rewards
	parWorkers int                 // Number of workers to spawn for parallel tasks.
}

// NewStateProcessor initialises a new StateProcessor.
func NewStateProcessor(config *params.ChainConfig, bc *BlockChain, engine consensus.Engine) *StateProcessor {
	return &StateProcessor{
		config:     config,
		bc:         bc,
		engine:     engine,
		parWorkers: runtime.GOMAXPROCS(0) - 1,
	}
}

// Process processes the state changes according to the Ethereum rules by running
// the transaction messages using the statedb and applying any rewards to both
// the processor (coinbase) and any included uncles.
//
// Process returns the receipts and logs accumulated during the process and
// returns the amount of gas that was used in the process. If any of the
// transactions failed to execute due to insufficient gas it will return an error.
func (p *StateProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config) (types.Receipts, []*types.Log, uint64, error) {
	var (
		txs      = block.Transactions()
		receipts = make(types.Receipts, len(txs))
		usedGas  = new(uint64)
		header   = block.Header()
		allLogs  []*types.Log
		gp       = new(GasPool).AddGas(block.GasLimit())
	)
	// Mutate the the block and state according to any hard-fork specs
	if p.config.DAOForkSupport && p.config.DAOForkBlock != nil && p.config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}
	signer := types.MakeSigner(p.config, header.Number)

	for s := 0; s < p.parWorkers; s++ {
		go func(start int) {
			for i := start; i < len(txs); i += p.parWorkers {
				types.Sender(signer, txs[i])
			}
		}(s)
	}

	intPool := vm.NewIntPool()
	// Iterate over and process the individual transactions
	for i, tx := range txs {
		ps := perfTimer.Start("statedb.Prepare")
		statedb.Prepare(tx.Hash(), block.Hash(), i)
		ps.Stop()
		ps = perfTimer.Start("ApplyTransaction")
		receipt, _, err := ApplyTransaction(p.config, p.bc, nil, gp, statedb, header, tx, usedGas, cfg, intPool, signer)
		if err != nil {
			return nil, nil, 0, err
		}
		ps.Stop()
		receipts[i] = receipt
		allLogs = append(allLogs, receipt.Logs...)
	}
	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	_ = p.engine.Finalize(p.bc, header, statedb, block.Transactions(), block.Uncles(), receipts, false)
	perfTimer.Print()

	return receipts, allLogs, *usedGas, nil
}

// ApplyTransaction attempts to apply a transaction to the given state database
// and uses the input parameters for its environment. It returns the receipt
// for the transaction, gas used and an error if the transaction failed,
// indicating the block was invalid.
func ApplyTransaction(config *params.ChainConfig, bc *BlockChain, author *common.Address, gp *GasPool, statedb *state.StateDB, header *types.Header, tx *types.Transaction, usedGas *uint64, cfg vm.Config, intPool *vm.IntPool, signer types.Signer) (*types.Receipt, uint64, error) {
	ps := perfTimer.Start("tx.AsMessage")
	msg, err := tx.AsMessage(signer)
	if err != nil {
		return nil, 0, err
	}
	ps.Stop()
	// Create a new context to be used in the EVM environment
	ps = perfTimer.Start("NewEVMContext")
	context := NewEVMContext(msg, header, bc, author)
	ps.Stop()
	// Create a new environment which holds all relevant information
	// about the transaction and calling mechanisms.
	ps = perfTimer.Start("NewEVMPool")
	vmenv := vm.NewEVMPool(context, statedb, config, cfg, intPool)
	ps.Stop()
	// Apply the transaction to the current state (included in the env)
	_, gas, failed, err := ApplyMessage(vmenv, msg, gp)
	if err != nil {
		return nil, 0, err
	}
	// Update the state with pending changes
	var root []byte
	if config.IsByzantium(header.Number) {
		ps = perfTimer.Start("Finalize")
		statedb.Finalise(true)
		ps.Stop()
	} else {
		ps = perfTimer.Start("IntermediateRoot")
		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
		ps.Stop()
	}
	*usedGas += gas

	// Create a new receipt for the transaction, storing the intermediate root and gas used by the tx
	// based on the eip phase, we're passing wether the root touch-delete accounts.
	ps = perfTimer.Start("NewReceipt")
	receipt := types.NewReceipt(root, failed, *usedGas)
	receipt.TxHash = tx.Hash()
	ps.Stop()
	receipt.GasUsed = gas
	// if the transaction created a contract, store the creation address in the receipt.
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}
	// Set the receipt logs and create a bloom for filtering
	ps = perfTimer.Start("GetLogs")
	receipt.Logs = statedb.GetLogs(tx.Hash())
	ps.Stop()
	ps = perfTimer.Start("CreateBloom")
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
	ps.Stop()

	return receipt, gas, err
}
