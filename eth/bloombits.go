// Copyright 2017 The go-ethereum Authors
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

package eth

import (
	"time"

	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/common/bitutil"
	"github.com/gochain/gochain/v3/core"
	"github.com/gochain/gochain/v3/core/bloombits"
	"github.com/gochain/gochain/v3/core/rawdb"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/log"
	"github.com/gochain/gochain/v3/params"
)

const (
	// bloomServiceThreads is the number of goroutines used globally by an GoChain
	// instance to service bloombits lookups for all running filters.
	bloomServiceThreads = 16

	// bloomFilterThreads is the number of goroutines used locally per filter to
	// multiplex requests onto the global servicing goroutines.
	bloomFilterThreads = 3

	// bloomRetrievalBatch is the maximum number of bloom bit retrievals to service
	// in a single batch.
	bloomRetrievalBatch = 16

	// bloomRetrievalWait is the maximum time to wait for enough bloom bit requests
	// to accumulate request an entire batch (avoiding hysteresis).
	bloomRetrievalWait = time.Duration(0)
)

// startBloomHandlers starts a batch of goroutines to accept bloom bit database
// retrievals from possibly a range of filters and serving the data to satisfy.
func (gc *GoChain) startBloomHandlers() {
	for i := 0; i < bloomServiceThreads; i++ {
		go func() {
			for {
				select {
				case <-gc.shutdownChan:
					return

				case request := <-gc.bloomRequests:
					task := <-request
					task.Bitsets = make([][]byte, len(task.Sections))
					for i, section := range task.Sections {
						head := rawdb.ReadCanonicalHash(gc.chainDb, (section+1)*params.BloomBitsBlocks-1)
						compVector := rawdb.ReadBloomBits(gc.chainDb.GlobalTable(), task.Bit, section, head)
						if blob, err := bitutil.DecompressBytes(compVector, int(params.BloomBitsBlocks)/8); err == nil {
							task.Bitsets[i] = blob
						} else {
							task.Error = err
						}
					}
					request <- task
				}
			}
		}()
	}
}

const (
	// bloomConfirms is the number of confirmation blocks before a bloom section is
	// considered probably final and its rotated bits are calculated.
	bloomConfirms = 256

	// bloomThrottling is the time to wait between processing two consecutive index
	// sections. It's useful during chain upgrades to prevent disk overload.
	bloomThrottling = 100 * time.Millisecond
)

// BloomIndexer implements a core.ChainIndexer, building up a rotated bloom bits index
// for the GoChain header bloom filters, permitting blazing fast filtering.
type BloomIndexer struct {
	size uint64 // section size to generate bloombits for

	db  common.Database      // database instance to write index data and metadata into
	gen *bloombits.Generator // generator to rotate the bloom bits crating the bloom index

	section uint64      // Section is the section number being processed currently
	head    common.Hash // Head is the hash of the last header processed
}

// NewBloomIndexer returns a chain indexer that generates bloom bits data for the
// canonical chain for fast logs filtering.
func NewBloomIndexer(db common.Database, size uint64) *core.ChainIndexer {
	backend := &BloomIndexer{
		db:   db,
		size: size,
	}
	table := common.NewTablePrefixer(db.GlobalTable(), string(rawdb.BloomBitsIndexPrefix))

	return core.NewChainIndexer(db, table, backend, size, bloomConfirms, bloomThrottling, "bloombits")
}

// Reset implements core.ChainIndexerBackend, starting a new bloombits index
// section.
func (b *BloomIndexer) Reset(section uint64, lastSectionHead common.Hash) error {
	gen, err := bloombits.NewGenerator(uint(b.size))
	b.gen, b.section, b.head = gen, section, common.Hash{}
	return err
}

// Process implements core.ChainIndexerBackend, adding a new header's bloom into
// the index.
func (b *BloomIndexer) Process(header *types.Header) {
	if err := b.gen.AddBloom(uint(header.Number.Uint64()-b.section*b.size), header.Bloom); err != nil {
		log.Error("Cannot add bloom to indexer")
	}
	b.head = header.Hash()
}

// Commit implements core.ChainIndexerBackend, finalizing the bloom section and
// writing it out into the database.
func (b *BloomIndexer) Commit() error {
	batch := b.db.GlobalTable().NewBatch()

	for i := 0; i < types.BloomBitLength; i++ {
		bits, err := b.gen.Bitset(uint(i))
		if err != nil {
			return err
		}
		rawdb.WriteBloomBits(batch, uint(i), b.section, b.head, bitutil.CompressBytes(bits))
	}
	rawdb.Must("write bloom bits batch", batch.Write)
	return nil
}
