package contracts

import (
	"fmt"
	"math/big"
	"os"

	"github.com/gochain/gochain/v4/accounts/abi"
	"github.com/gochain/gochain/v4/common"
	"github.com/gochain/gochain/v4/core"
	"github.com/gochain/gochain/v4/log"
)

// To execute a contract function internally
func callContract(gc *core.BlockChain, contractAddress common.Address, functionName string, args ...interface{}) ([]byte, error) {
	// 1. Access the Blockchain and State
	header := gc.CurrentHeader()
	statedb, err := gc.StateAt(header.Root)
	if err != nil {
		return nil, fmt.Errorf("failed to access state: %w", err)
	}

	// 2. Load the Contract (ABI is crucial here)
	// TODO: Do this on node startup
	abiFile, err := os.Open("../../contracts/gasPrice.abi")
	if err != nil {
		log.Error("Failed to open gasPrice.abi", "err", err)
		return nil, fmt.Errorf("failed to open gasPrice.abi: %w", err)
	}

	abi2, err := abi.JSON(abiFile)
	if err != nil {
		log.Error("Failed to parse gasPrice.abi", "err", err)
		return nil, fmt.Errorf("failed to parse gasPrice.abi: %w", err)
	}
	fmt.Println(abi2)

	// 3. Prepare the Input Data
	// Encode the function call
	data, err := abi2.Pack(functionName, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack arguments: %w", err)
	}

	// Create a message for the EVM
	fromAddress := common.Address{} // For read-only calls, the from address doesn't matter
	msg := vm.NewMessage(
		fromAddress,
		&contractAddress,
		0,             // Nonce (not used for read-only calls)
		big.NewInt(0), // Amount
		0,             // Gas Limit (adjust as needed)
		big.NewInt(0), // Gas Price (not used for read-only calls)
		data,
		false, // No precompiles
	)

	// 4. Execute the Call
	// Create a new EVM
	vmenv := vm.NewEVM(vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} }, // Replace with actual block hash retrieval if needed
		Coinbase:    common.Address{},                                  // Replace with actual coinbase address if needed
		GasLimit:    0,                                                 // Gas Limit (not used for read-only calls)
		BlockNumber: new(big.Int).SetUint64(header.Number.Uint64()),
		Time:        new(big.Int).SetUint64(header.Time),
		Difficulty:  header.Difficulty,
	}, vm.TxContext{
		Origin:   fromAddress, // Replace with your account address
		GasPrice: big.NewInt(0),
	}, statedb, gc.Config())

	// Execute the call
	result, err := vmenv.Call(msg, &contractAddress, nil)
	if err != nil {
		return nil, fmt.Errorf("vm call error: %w", err)
	}

	return result, nil
}
