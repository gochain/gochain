package clique

import (
	"context"
	"errors"
	"time"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/common/hexutil"
	"github.com/gochain-io/gochain/consensus"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/params"
	"github.com/gochain-io/gochain/rpc"
)

// fakeEngine is a fake clique consensus.Engine for testing only.
type fakeEngine struct {
	real  *Clique
	Full  bool
	Delay time.Duration
	Fail  uint64
}

// NewFaker returns a new fake consensus.Engine.
func NewFaker() consensus.Engine {
	return &fakeEngine{
		real: &Clique{
			config: params.DefaultCliqueConfig(),
		},
	}
}

// NewFaker returns a new fake consensus.Engine which sleeps for delay during header verification.
func NewFakeDelayer(delay time.Duration) consensus.Engine {
	return &fakeEngine{
		real: &Clique{
			config: params.DefaultCliqueConfig(),
		},
		Delay: delay,
	}
}

// NewFakeFailer returns a new fake consensus.Engine which fails header verification at the given block.
func NewFakeFailer(fail uint64) consensus.Engine {
	return &fakeEngine{
		real: &Clique{
			config: params.DefaultCliqueConfig(),
		},
		Fail: fail,
	}
}

// NewFullFaker returns a new fake consensus.Engine which skips header verification entirely.
func NewFullFaker() consensus.Engine {
	return &fakeEngine{
		real: &Clique{
			config: params.DefaultCliqueConfig(),
		},
		Full: true,
	}
}

func (e *fakeEngine) APIs(chain consensus.ChainReader) []rpc.API {
	return e.real.APIs(chain)
}

func (e *fakeEngine) Authorize(signer common.Address, fn consensus.SignerFn) {
	e.real.Authorize(signer, fn)
}

func (e *fakeEngine) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

func (e *fakeEngine) VerifyHeader(ctx context.Context, chain consensus.ChainReader, header *types.Header) error {
	if e.Full {
		return nil
	}
	// Sanity check.
	number := header.Number.Uint64()
	if chain.GetHeader(header.Hash(), number) != nil {
		return nil
	}
	if number > 0 {
		parent := chain.GetHeader(header.ParentHash, number-1)
		if parent == nil {
			return consensus.ErrUnknownAncestor
		}
	}
	// Subset of real method - skip cascading field and seal verification.
	if err := e.real.verifyHeader(ctx, chain, header, nil); err != nil {
		return err
	}
	return e.delayOrFail(ctx, chain, header, nil)
}

func (e *fakeEngine) VerifyHeaders(ctx context.Context, chain consensus.ChainReader, headers []*types.Header) (chan<- struct{}, <-chan error) {
	if e.Full || len(headers) == 0 {
		return e.real.verifyHeaders(ctx, chain, headers, nil)
	}
	// Subset of real method - skip cascading field and seal verification.
	return e.real.verifyHeaders(ctx, chain, headers, []verifyFn{e.real.verifyHeader, e.delayOrFail})
}

func (e *fakeEngine) delayOrFail(_ context.Context, _ consensus.ChainReader, header *types.Header, _ []*types.Header) error {
	time.Sleep(e.Delay)
	if e.Fail > 0 && e.Fail == header.Number.Uint64() {
		return errors.New("fake failure")
	}
	return nil
}

func (e *fakeEngine) Prepare(ctx context.Context, chain consensus.ChainReader, header *types.Header) error {
	header.Extra = ExtraEnsureVanity(header.Extra)
	if header.Difficulty == nil {
		header.Difficulty = header.Coinbase.Big()
		if header.Difficulty.Uint64() == 0 {
			header.Difficulty.SetUint64(1)
		}
	}
	return nil
}

func (e *fakeEngine) Finalize(ctx context.Context, chain consensus.ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction,
	receipts []*types.Receipt, block bool) *types.Block {
	return e.real.Finalize(ctx, chain, header, state, txs, receipts, block)
}

func (e *fakeEngine) Seal(ctx context.Context, chain consensus.ChainReader, block *types.Block, stop <-chan struct{}) (*types.Block, *time.Time, error) {
	header := block.Header()
	header.Nonce, header.MixDigest = types.BlockNonce{}, common.Hash{}
	header.Signer = hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")
	return block.WithSeal(header), nil, nil
}

func (e *fakeEngine) SealHash(header *types.Header) common.Hash {
	return e.real.SealHash(header)
}
