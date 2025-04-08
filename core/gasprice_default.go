package core

import (
	"context"
	"math/big"

	"github.com/gochain/gochain/v4/params"
)

var Default = new(big.Int).SetUint64(2 * params.Shannon)

type DefaultPricer struct {
}

func (d *DefaultPricer) GasPrice(ctx context.Context) (*big.Int, error) {
	return Default, nil
}

func (d *DefaultPricer) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return Default, nil
}
