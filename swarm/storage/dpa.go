// Copyright 2016 The go-ethereum Authors
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

package storage

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gochain-io/gochain/v3/log"
)

/*
DPA provides the client API entrypoints Store and Retrieve to store and retrieve
It can store anything that has a byte slice representation, so files or serialised objects etc.

Storage: DPA calls the Chunker to segment the input datastream of any size to a merkle hashed tree of chunks. The key of the root block is returned to the client.

Retrieval: given the key of the root block, the DPA retrieves the block chunks and reconstructs the original data and passes it back as a lazy reader. A lazy reader is a reader with on-demand delayed processing, i.e. the chunks needed to reconstruct a large file are only fetched and processed if that particular part of the document is actually read.

As the chunker produces chunks, DPA dispatches them to its own chunk store
implementation for storage or retrieval.
*/

const (
	storeChanCapacity           = 100
	retrieveChanCapacity        = 100
	singletonSwarmDbCapacity    = 50000
	singletonSwarmCacheCapacity = 500
	maxStoreProcesses           = 8
	maxRetrieveProcesses        = 8
)

var (
	notFound = errors.New("not found")
)

type DPA struct {
	ChunkStore
	storeC    chan *Chunk
	retrieveC chan *Chunk
	Chunker   Chunker

	lock    sync.Mutex
	running bool
	quitC   chan bool
}

// for testing locally
func NewLocalDPA(datadir string) (*DPA, error) {

	hash := MakeHashFunc("SHA256")

	dbStore, err := NewDbStore(datadir, hash, singletonSwarmDbCapacity, 0)
	if err != nil {
		return nil, err
	}

	return NewDPA(&LocalStore{
		NewMemStore(dbStore, singletonSwarmCacheCapacity),
		dbStore,
	}, NewChunkerParams()), nil
}

func NewDPA(store ChunkStore, params *ChunkerParams) *DPA {
	chunker := NewTreeChunker(params)
	return &DPA{
		Chunker:    chunker,
		ChunkStore: store,
	}
}

// Public API. Main entry point for document retrieval directly. Used by the
// FS-aware API and httpaccess
// Chunk retrieval blocks on netStore requests with a timeout so reader will
// report error if retrieval of chunks within requested range time out.
func (d *DPA) Retrieve(key Key) LazySectionReader {
	return d.Chunker.Join(key, d.retrieveC)
}

// Public API. Main entry point for document storage directly. Used by the
// FS-aware API and httpaccess
func (d *DPA) Store(data io.Reader, size int64, swg *sync.WaitGroup) (key Key, err error) {
	return d.Chunker.Split(data, size, d.storeC, swg)
}

func (d *DPA) Start() {
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.running {
		return
	}
	d.running = true
	d.retrieveC = make(chan *Chunk, retrieveChanCapacity)
	d.storeC = make(chan *Chunk, storeChanCapacity)
	d.quitC = make(chan bool)
	d.storeLoop()
	d.retrieveLoop()
}

func (d *DPA) Stop() {
	d.lock.Lock()
	defer d.lock.Unlock()
	if !d.running {
		return
	}
	d.running = false
	close(d.quitC)
}

// retrieveLoop dispatches the parallel chunk retrieval requests received on the
// retrieve channel to its ChunkStore  (NetStore or LocalStore)
func (d *DPA) retrieveLoop() {
	for i := 0; i < maxRetrieveProcesses; i++ {
		go d.retrieveWorker()
	}
	log.Trace(fmt.Sprintf("dpa: retrieve loop spawning %v workers", maxRetrieveProcesses))
}

func (d *DPA) retrieveWorker() {
	for chunk := range d.retrieveC {
		log.Trace(fmt.Sprintf("dpa: retrieve loop : chunk %v", chunk.Key.Log()))
		storedChunk, err := d.Get(chunk.Key)
		if err == notFound {
			log.Trace(fmt.Sprintf("chunk %v not found", chunk.Key.Log()))
		} else if err != nil {
			log.Trace(fmt.Sprintf("error retrieving chunk %v: %v", chunk.Key.Log(), err))
		} else {
			chunk.SData = storedChunk.SData
			chunk.Size = storedChunk.Size
		}
		close(chunk.C)

		select {
		case <-d.quitC:
			return
		default:
		}
	}
}

// storeLoop dispatches the parallel chunk store request processors
// received on the store channel to its ChunkStore (NetStore or LocalStore)
func (d *DPA) storeLoop() {
	for i := 0; i < maxStoreProcesses; i++ {
		go d.storeWorker()
	}
	log.Trace(fmt.Sprintf("dpa: store spawning %v workers", maxStoreProcesses))
}

func (d *DPA) storeWorker() {
	for chunk := range d.storeC {
		d.Put(chunk)
		if chunk.wg != nil {
			log.Trace(fmt.Sprintf("dpa: store processor %v", chunk.Key.Log()))
			chunk.wg.Done()
		}
		select {
		case <-d.quitC:
			return
		default:
		}
	}
}

// DpaChunkStore implements the ChunkStore interface,
// this chunk access layer assumed 2 chunk stores
// local storage eg. LocalStore and network storage eg., NetStore
// access by calling network is blocking with a timeout

type dpaChunkStore struct {
	n          int
	localStore ChunkStore
	netStore   ChunkStore
}

func NewDpaChunkStore(localStore, netStore ChunkStore) *dpaChunkStore {
	return &dpaChunkStore{0, localStore, netStore}
}

// Get is the entrypoint for local retrieve requests
// waits for response or times out
func (d *dpaChunkStore) Get(key Key) (chunk *Chunk, err error) {
	chunk, err = d.netStore.Get(key)
	// timeout := time.Now().Add(searchTimeout)
	if chunk.SData != nil {
		log.Trace(fmt.Sprintf("DPA.Get: %v found locally, %d bytes", key.Log(), len(chunk.SData)))
		return
	}
	// TODO: use d.timer time.Timer and reset with defer disableTimer
	timer := time.After(searchTimeout)
	select {
	case <-timer:
		log.Trace(fmt.Sprintf("DPA.Get: %v request time out ", key.Log()))
		err = notFound
	case <-chunk.Req.C:
		log.Trace(fmt.Sprintf("DPA.Get: %v retrieved, %d bytes (%p)", key.Log(), len(chunk.SData), chunk))
	}
	return
}

// Put is the entrypoint for local store requests coming from storeLoop
func (d *dpaChunkStore) Put(entry *Chunk) {
	chunk, err := d.localStore.Get(entry.Key)
	if err != nil {
		log.Trace(fmt.Sprintf("DPA.Put: %v new chunk. call netStore.Put", entry.Key.Log()))
		chunk = entry
	} else if chunk.SData == nil {
		log.Trace(fmt.Sprintf("DPA.Put: %v request entry found", entry.Key.Log()))
		chunk.SData = entry.SData
		chunk.Size = entry.Size
	} else {
		log.Trace(fmt.Sprintf("DPA.Put: %v chunk already known", entry.Key.Log()))
		return
	}
	// from this point on the storage logic is the same with network storage requests
	log.Trace(fmt.Sprintf("DPA.Put %v: %v", d.n, chunk.Key.Log()))
	d.n++
	d.netStore.Put(chunk)
}

// Close chunk store
func (d *dpaChunkStore) Close() {}
