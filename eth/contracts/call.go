package contracts

import (
	"fmt"
	"math/big"
	"os"

	"github.com/gochain/gochain/v4/accounts/abi"
	"github.com/gochain/gochain/v4/common"
	"github.com/gochain/gochain/v4/core"
	"github.com/gochain/gochain/v4/core/types"
	"github.com/gochain/gochain/v4/core/vm"
	"github.com/gochain/gochain/v4/log"
)

type ContarctData struct {
	abi     abi.ABI
	address common.Address
}

type Caller struct {
	address common.Address
}

var contracts map[string]ContarctData = make(map[string]ContarctData)
var viewCaller Caller = Caller{
	address: common.Address{},
}

func (c Caller) Address() common.Address {
	return c.address
}

func InitContract(contract string, address string) error {
	abiFName := fmt.Sprintf("%s.abi", contract)
	abiFile, err := os.Open(abiFName)
	if err != nil {
		log.Error("Failed to open contract abi", "fname", abiFName, "err", err)
		return fmt.Errorf("failed to open contact %s", contract)
	}
	abiData, err := abi.JSON(abiFile)
	if err != nil {
		log.Error("Failed to parse contract abi", "fname", abiFName, "err", err)
		return fmt.Errorf("failed to parse contact %s", contract)
	}
	contracts[contract] = ContarctData{
		abi:     abiData,
		address: common.HexToAddress(address),
	}
	return nil
}

func CallViewContract(gc *core.BlockChain, contract string, function string, args ...interface{}) ([]interface{}, error) {
	header := gc.CurrentHeader()
	statedb, err := gc.StateAt(header.Root)
	if err != nil {
		return nil, fmt.Errorf("failed to access state: %w", err)
	}

	contractData := contracts[contract]
	data, err := contractData.abi.Pack(function, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack arguments: %w", err)
	}

	msg := types.NewMessage(
		viewCaller.Address(),
		&contractData.address,
		0,
		big.NewInt(0),
		1000,
		big.NewInt(1000),
		data,
		false,
	)

	evmContext := core.NewEVMContext(msg, header, gc, nil)
	evm := vm.NewEVM(evmContext, statedb, gc.Config(), vm.Config{})

	result, _, err := evm.StaticCall(viewCaller, contractData.address, data, 1000)
	if err != nil {
		return nil, fmt.Errorf("vm call error: %w", err)
	}
	unpackedResult, err := contractData.abi.Unpack(function, result)
	if err != nil {
		return nil, fmt.Errorf("can't unpack: %w", err)
	}

	return unpackedResult, nil
}
