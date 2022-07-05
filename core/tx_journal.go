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

package core

import (
	"errors"
	"io"
	"os"

	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/log"
	"github.com/gochain/rlp"
)

// errNoActiveJournal is returned if a transaction is attempted to be inserted
// into the journal, but no such file is currently open.
var errNoActiveJournal = errors.New("no active journal")

// devNull is a WriteCloser that just discards anything written into it. Its
// goal is to allow the transaction journal to write into a fake journal when
// loading transactions on startup without printing warnings due to no file
// being read for write.
type devNull struct{}

func (*devNull) Write(p []byte) (n int, err error) { return len(p), nil }
func (*devNull) Close() error                      { return nil }

// txJournal is a rotating log of transactions with the aim of storing locally
// created transactions to allow non-executed ones to survive node restarts.
type txJournal struct {
	path   string         // Filesystem path to store the transactions at
	writer io.WriteCloser // Output stream to write new transactions into
}

// newTxJournal creates a new transaction journal to
func newTxJournal(path string) *txJournal {
	return &txJournal{
		path: path,
	}
}

// load parses a transaction journal dump from disk, loading its contents into
// the specified pool.
func (journal *txJournal) load(add func(types.Transactions) []error) error {
	const batchSize = 1000
	// Skip the parsing if the journal file doesn't exist at all
	if _, err := os.Stat(journal.path); os.IsNotExist(err) {
		return nil
	}
	// Open the journal for loading any past transactions
	input, err := os.Open(journal.path)
	if err != nil {
		return err
	}
	defer input.Close()

	// Temporarily discard any journal additions (don't double add on load)
	journal.writer = new(devNull)
	defer func() { journal.writer = nil }()

	// Inject all transactions from the journal into the pool
	stream := rlp.NewStream(input, 0)
	defer rlp.Discard(stream)
	total, dropped := 0, 0

	var failure error
	var batch types.Transactions
	addBatch := func() {
		if errs := add(batch); len(errs) > 0 {
			dropped += len(errs)
			log.Debug("Failed to add journaled transactions", "errs", len(errs))
		}
		batch = batch[:0]
	}
	for {
		// Parse the next transaction and terminate on error
		tx := new(types.Transaction)
		if err := stream.Decode(tx); err != nil {
			if err != io.EOF {
				failure = err
			}
			break
		}
		// New transaction parsed, queue up for later, import if threshold is reached
		total++
		batch = append(batch, tx)
		if len(batch) >= batchSize {
			addBatch()
		}
	}
	if len(batch) > 0 {
		addBatch()
	}
	log.Info("Loaded local transaction journal", "transactions", total, "dropped", dropped)

	// Verify input closes without error.
	if err := input.Close(); err != nil {
		return err
	}

	return failure
}

// insert adds the specified transaction to the local disk journal.
func (journal *txJournal) insert(tx *types.Transaction) error {
	if journal.writer == nil {
		return errNoActiveJournal
	}
	if err := rlp.Encode(journal.writer, tx); err != nil {
		return err
	}
	return nil
}

// rotate regenerates the transaction journal based on the current contents of
// the transaction pool.
func (journal *txJournal) rotate(acts int, all types.Transactions) error {
	// Close the current journal (if any is open)
	if journal.writer != nil {
		if err := journal.writer.Close(); err != nil {
			return err
		}
		journal.writer = nil
	}
	// Generate a new journal with the contents of the current pool
	replacement, err := os.OpenFile(journal.path+".new", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	for _, tx := range all {
		if err = rlp.Encode(replacement, tx); err != nil {
			_ = replacement.Close()
			return err
		}
	}
	if err := replacement.Close(); err != nil {
		return err
	}

	// Replace the live journal with the newly generated one
	if err = os.Rename(journal.path+".new", journal.path); err != nil {
		return err
	}
	sink, err := os.OpenFile(journal.path, os.O_WRONLY|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	journal.writer = sink
	log.Info("Regenerated local transaction journal", "transactions", len(all), "accounts", acts)

	return nil
}

// close flushes the transaction journal contents to disk and closes the file.
func (journal *txJournal) close() error {
	var err error

	if journal.writer != nil {
		err = journal.writer.Close()
		journal.writer = nil
	}
	return err
}
