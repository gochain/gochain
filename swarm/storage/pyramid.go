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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

/*
   The main idea of a pyramid chunker is to process the input data without knowing the entire size apriori.
   For this to be achieved, the chunker tree is built from the ground up until the data is exhausted.
   This opens up new aveneus such as easy append and other sort of modifications to the tree thereby avoiding
   duplication of data chunks.


   Below is an example of a two level chunks tree. The leaf chunks are called data chunks and all the above
   chunks are called tree chunks. The tree chunk above data chunks is level 0 and so on until it reaches
   the root tree chunk.



                                            T10                                        <- Tree chunk lvl1
                                            |
                  __________________________|_____________________________
                 /                  |                   |                \
                /                   |                   \                 \
            __T00__             ___T01__           ___T02__           ___T03__         <- Tree chunks lvl 0
           / /     \           / /      \         / /      \         / /      \
          / /       \         / /        \       / /       \        / /        \
         D1 D2 ... D128	     D1 D2 ... D128     D1 D2 ... D128     D1 D2 ... D128      <-  Data Chunks


    The split function continuously read the data and creates data chunks and send them to storage.
    When certain no of data chunks are created (defaultBranches), a signal is sent to create a tree
    entry. When the level 0 tree entries reaches certain threshold (defaultBranches), another signal
    is sent to a tree entry one level up.. and so on... until only the data is exhausted AND only one
    tree entry is present in certain level. The key of tree entry is given out as the rootKey of the file.

*/

var (
	errLoadingTreeRootChunk = errors.New("LoadTree Error: Could not load root chunk")
	errLoadingTreeChunk     = errors.New("LoadTree Error: Could not load chunk")
)

const (
	ChunkProcessors       = 8
	DefaultBranches int64 = 128
	splitTimeout          = time.Minute * 5
)

var timeoutErr = fmt.Errorf("timed out after %s", splitTimeout)

const (
	DataChunk = 0
	TreeChunk = 1
)

type ChunkerParams struct {
	Branches int64
	Hash     string
}

func NewChunkerParams() *ChunkerParams {
	return &ChunkerParams{
		Branches: DefaultBranches,
		Hash:     SHA3Hash,
	}
}

// Entry to create a tree node
type TreeEntry struct {
	level         int
	branchCount   int64
	subtreeSize   uint64
	chunk         []byte
	key           []byte
	index         int  // used in append to indicate the index of existing tree entry
	updatePending bool // indicates if the entry is loaded from existing tree
}

func NewTreeEntry(pyramid *PyramidChunker) *TreeEntry {
	return &TreeEntry{
		level:         0,
		branchCount:   0,
		subtreeSize:   0,
		chunk:         make([]byte, pyramid.chunkSize+8),
		key:           make([]byte, pyramid.hashSize),
		index:         0,
		updatePending: false,
	}
}

// Used by the hash processor to create a data/tree chunk and send to storage
type chunkJob struct {
	key       Key
	chunk     []byte
	size      int64
	done      func()
	chunkType int // used to identify the tree related chunks for debugging
	chunkLvl  int // leaf-1 is level 0 and goes upwards until it reaches root
}

type PyramidChunker struct {
	hashFunc    SwarmHasher
	chunkSize   int64
	hashSize    int64
	branches    int64
	workerCount int64
	workerLock  sync.RWMutex
}

func NewPyramidChunker(params *ChunkerParams) *PyramidChunker {
	p := &PyramidChunker{}
	p.hashFunc = MakeHashFunc(params.Hash)
	p.branches = params.Branches
	p.hashSize = int64(p.hashFunc().Size())
	p.chunkSize = p.hashSize * p.branches
	p.workerCount = 0
	return p
}

func (p *PyramidChunker) Join(key Key, chunkC chan *Chunk) LazySectionReader {
	return &LazyChunkReader{
		key:       key,
		chunkC:    chunkC,
		chunkSize: p.chunkSize,
		branches:  p.branches,
		hashSize:  p.hashSize,
	}
}

func (p *PyramidChunker) incrementWorkerCount() {
	p.workerLock.Lock()
	defer p.workerLock.Unlock()
	p.workerCount += 1
}

func (p *PyramidChunker) getWorkerCount() int64 {
	p.workerLock.Lock()
	defer p.workerLock.Unlock()
	return p.workerCount
}

func (p *PyramidChunker) decrementWorkerCount() {
	p.workerLock.Lock()
	defer p.workerLock.Unlock()
	p.workerCount -= 1
}

func (p *PyramidChunker) Split(data io.Reader, size int64, chunkC chan *Chunk, swg *sync.WaitGroup) (Key, error) {
	rootKey := make([]byte, p.hashSize)
	chunkLevel := make([][]*TreeEntry, p.branches)
	quitC := make(chan bool)
	defer close(quitC)

	var wg sync.WaitGroup
	wg.Add(1)
	go p.prepareChunks(false, chunkLevel, data, rootKey, quitC, wg.Done, chunkC, swg)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		if swg != nil {
			swg.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(splitTimeout):
		return nil, timeoutErr
	}
	return rootKey, nil

}

func (p *PyramidChunker) Append(key Key, data io.Reader, chunkC chan *Chunk, swg *sync.WaitGroup) (Key, error) {
	rootKey := make([]byte, p.hashSize)
	chunkLevel := make([][]*TreeEntry, p.branches)
	quitC := make(chan bool)
	defer close(quitC)

	// Load the right most unfinished tree chunks in every level
	p.loadTree(chunkLevel, key, chunkC, quitC)

	var wg sync.WaitGroup
	wg.Add(1)
	go p.prepareChunks(true, chunkLevel, data, rootKey, quitC, wg.Done, chunkC, swg)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		if swg != nil {
			swg.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(splitTimeout):
		return nil, timeoutErr
	}
	return rootKey, nil

}

func (p *PyramidChunker) processor(id int64, jobC <-chan *chunkJob, chunkC chan *Chunk, quitC chan bool, swg *sync.WaitGroup) {
	defer p.decrementWorkerCount()
	if swg != nil {
		defer swg.Done()
	}

	hasher := p.hashFunc()
	for {
		select {

		case job, ok := <-jobC:
			if !ok {
				return
			}
			p.processChunk(id, hasher, job, chunkC, swg)
		case <-quitC:
			return
		}
	}
}

func (p *PyramidChunker) processChunk(id int64, hasher SwarmHash, job *chunkJob, chunkC chan *Chunk, swg *sync.WaitGroup) {
	defer job.done()
	hasher.ResetWithLength(job.chunk[:8]) // 8 bytes of length
	hasher.Write(job.chunk[8:])           // minus 8 []byte length
	h := hasher.Sum(nil)

	// report hash of this chunk one level up (keys corresponds to the proper subslice of the parent chunk)
	copy(job.key, h)

	// send off new chunk to storage
	if chunkC != nil {
		if swg != nil {
			swg.Add(1)
		}
		chunkC <- &Chunk{Key: h, SData: job.chunk, Size: job.size, wg: swg}
	}
}

func (p *PyramidChunker) loadTree(chunkLevel [][]*TreeEntry, key Key, chunkC chan *Chunk, quitC chan bool) error {
	// Get the root chunk to get the total size
	chunk := retrieve(key, chunkC, quitC)
	if chunk == nil {
		return errLoadingTreeRootChunk
	}

	//if data size is less than a chunk... add a parent with update as pending
	if chunk.Size <= p.chunkSize {
		newEntry := &TreeEntry{
			level:         0,
			branchCount:   1,
			subtreeSize:   uint64(chunk.Size),
			chunk:         make([]byte, p.chunkSize+8),
			key:           make([]byte, p.hashSize),
			index:         0,
			updatePending: true,
		}
		copy(newEntry.chunk[8:], chunk.Key)
		chunkLevel[0] = append(chunkLevel[0], newEntry)
		return nil
	}

	var treeSize int64
	var depth int
	treeSize = p.chunkSize
	for ; treeSize < chunk.Size; treeSize *= p.branches {
		depth++
	}

	// Add the root chunk entry
	branchCount := int64(len(chunk.SData)-8) / p.hashSize
	newEntry := &TreeEntry{
		level:         depth - 1,
		branchCount:   branchCount,
		subtreeSize:   uint64(chunk.Size),
		chunk:         chunk.SData,
		key:           key,
		index:         0,
		updatePending: true,
	}
	chunkLevel[depth-1] = append(chunkLevel[depth-1], newEntry)

	// Add the rest of the tree
	for lvl := depth - 1; lvl >= 1; lvl-- {

		//TODO(jmozah): instead of loading finished branches and then trim in the end,
		//avoid loading them in the first place
		for _, ent := range chunkLevel[lvl] {
			branchCount = int64(len(ent.chunk)-8) / p.hashSize
			for i := int64(0); i < branchCount; i++ {
				key := ent.chunk[8+(i*p.hashSize) : 8+((i+1)*p.hashSize)]
				newChunk := retrieve(key, chunkC, quitC)
				if newChunk == nil {
					return errLoadingTreeChunk
				}
				bewBranchCount := int64(len(newChunk.SData)-8) / p.hashSize
				newEntry := &TreeEntry{
					level:         lvl - 1,
					branchCount:   bewBranchCount,
					subtreeSize:   uint64(newChunk.Size),
					chunk:         newChunk.SData,
					key:           key,
					index:         0,
					updatePending: true,
				}
				chunkLevel[lvl-1] = append(chunkLevel[lvl-1], newEntry)

			}

			// We need to get only the right most unfinished branch.. so trim all finished branches
			if int64(len(chunkLevel[lvl-1])) >= p.branches {
				chunkLevel[lvl-1] = nil
			}
		}
	}

	return nil
}

func (p *PyramidChunker) prepareChunks(isAppend bool, chunkLevel [][]*TreeEntry, data io.Reader, rootKey []byte, quitC chan bool, done func(), chunkC chan *Chunk, swg *sync.WaitGroup) {
	defer done()
	jobC := make(chan *chunkJob, 2*ChunkProcessors)
	defer close(jobC)

	chunkWG := &sync.WaitGroup{}
	totalDataSize := 0

	p.incrementWorkerCount()
	if swg != nil {
		swg.Add(1)
	}
	go p.processor(p.workerCount, jobC, chunkC, quitC, swg)

	parent := NewTreeEntry(p)
	var unFinishedChunk *Chunk

	if isAppend && len(chunkLevel[0]) != 0 {
		lastIndex := len(chunkLevel[0]) - 1
		ent := chunkLevel[0][lastIndex]

		if ent.branchCount < p.branches {
			parent = &TreeEntry{
				level:         0,
				branchCount:   ent.branchCount,
				subtreeSize:   ent.subtreeSize,
				chunk:         ent.chunk,
				key:           ent.key,
				index:         lastIndex,
				updatePending: true,
			}

			lastBranch := parent.branchCount - 1
			lastKey := parent.chunk[8+lastBranch*p.hashSize : 8+(lastBranch+1)*p.hashSize]

			unFinishedChunk = retrieve(lastKey, chunkC, quitC)
			if unFinishedChunk.Size < p.chunkSize {

				parent.subtreeSize = parent.subtreeSize - uint64(unFinishedChunk.Size)
				parent.branchCount = parent.branchCount - 1
			} else {
				unFinishedChunk = nil
			}
		}
	}

	for index := 0; ; index++ {
		var n int
		var err error
		chunkData := make([]byte, p.chunkSize+8)
		if unFinishedChunk != nil {
			copy(chunkData, unFinishedChunk.SData)
			n, err = data.Read(chunkData[8+unFinishedChunk.Size:])
			n += int(unFinishedChunk.Size)
			unFinishedChunk = nil
		} else {
			n, err = data.Read(chunkData[8:])
		}

		totalDataSize += n
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				if parent.branchCount == 1 {
					// Data is exactly one chunk.. pick the last chunk key as root
					chunkWG.Wait()
					lastChunksKey := parent.chunk[8 : 8+p.hashSize]
					copy(rootKey, lastChunksKey)
					break
				}
			} else {
				close(quitC)
				break
			}
		}

		// Data ended in chunk boundary.. just signal to start bulding tree
		if n == 0 {
			p.buildTree(isAppend, chunkLevel, parent, chunkWG, jobC, quitC, true, rootKey)
			break
		} else {

			pkey := p.enqueueDataChunk(chunkData, uint64(n), parent, chunkWG, jobC, quitC)

			// update tree related parent data structures
			parent.subtreeSize += uint64(n)
			parent.branchCount++

			// Data got exhausted... signal to send any parent tree related chunks
			if int64(n) < p.chunkSize {

				// only one data chunk .. so dont add any parent chunk
				if parent.branchCount <= 1 {
					chunkWG.Wait()
					copy(rootKey, pkey)
					break
				}

				p.buildTree(isAppend, chunkLevel, parent, chunkWG, jobC, quitC, true, rootKey)
				break
			}

			if parent.branchCount == p.branches {
				p.buildTree(isAppend, chunkLevel, parent, chunkWG, jobC, quitC, false, rootKey)
				parent = NewTreeEntry(p)
			}

		}

		workers := p.getWorkerCount()
		if int64(len(jobC)) > workers && workers < ChunkProcessors {
			p.incrementWorkerCount()
			if swg != nil {
				swg.Add(1)
			}
			go p.processor(p.workerCount, jobC, chunkC, quitC, swg)
		}
	}
}

func (p *PyramidChunker) buildTree(isAppend bool, chunkLevel [][]*TreeEntry, ent *TreeEntry, chunkWG *sync.WaitGroup, jobC chan *chunkJob, quitC chan bool, last bool, rootKey []byte) {
	chunkWG.Wait()
	p.enqueueTreeChunk(chunkLevel, ent, chunkWG, jobC, quitC, last)

	compress := false
	endLvl := p.branches
	for lvl := int64(0); lvl < p.branches; lvl++ {
		lvlCount := int64(len(chunkLevel[lvl]))
		if lvlCount >= p.branches {
			endLvl = lvl + 1
			compress = true
			break
		}
	}

	if !compress && !last {
		return
	}

	// Wait for all the keys to be processed before compressing the tree
	chunkWG.Wait()

	for lvl := int64(ent.level); lvl < endLvl; lvl++ {

		lvlCount := int64(len(chunkLevel[lvl]))
		if lvlCount == 1 && last {
			copy(rootKey, chunkLevel[lvl][0].key)
			return
		}

		for startCount := int64(0); startCount < lvlCount; startCount += p.branches {

			endCount := startCount + p.branches
			if endCount > lvlCount {
				endCount = lvlCount
			}

			var nextLvlCount int64
			var tempEntry *TreeEntry
			if len(chunkLevel[lvl+1]) > 0 {
				nextLvlCount = int64(len(chunkLevel[lvl+1]) - 1)
				tempEntry = chunkLevel[lvl+1][nextLvlCount]
			}
			if isAppend && tempEntry != nil && tempEntry.updatePending {
				updateEntry := &TreeEntry{
					level:         int(lvl + 1),
					branchCount:   0,
					subtreeSize:   0,
					chunk:         make([]byte, p.chunkSize+8),
					key:           make([]byte, p.hashSize),
					index:         int(nextLvlCount),
					updatePending: true,
				}
				for index := int64(0); index < lvlCount; index++ {
					updateEntry.branchCount++
					updateEntry.subtreeSize += chunkLevel[lvl][index].subtreeSize
					copy(updateEntry.chunk[8+(index*p.hashSize):8+((index+1)*p.hashSize)], chunkLevel[lvl][index].key[:p.hashSize])
				}

				p.enqueueTreeChunk(chunkLevel, updateEntry, chunkWG, jobC, quitC, last)

			} else {

				noOfBranches := endCount - startCount
				newEntry := &TreeEntry{
					level:         int(lvl + 1),
					branchCount:   noOfBranches,
					subtreeSize:   0,
					chunk:         make([]byte, (noOfBranches*p.hashSize)+8),
					key:           make([]byte, p.hashSize),
					index:         int(nextLvlCount),
					updatePending: false,
				}

				index := int64(0)
				for i := startCount; i < endCount; i++ {
					entry := chunkLevel[lvl][i]
					newEntry.subtreeSize += entry.subtreeSize
					copy(newEntry.chunk[8+(index*p.hashSize):8+((index+1)*p.hashSize)], entry.key[:p.hashSize])
					index++
				}

				p.enqueueTreeChunk(chunkLevel, newEntry, chunkWG, jobC, quitC, last)

			}

		}

		if !isAppend {
			chunkWG.Wait()
			if compress {
				chunkLevel[lvl] = nil
			}
		}
	}

}

func (p *PyramidChunker) enqueueTreeChunk(chunkLevel [][]*TreeEntry, ent *TreeEntry, chunkWG *sync.WaitGroup, jobC chan *chunkJob, quitC chan bool, last bool) {
	if ent != nil {

		// wait for data chunks to get over before processing the tree chunk
		if last {
			chunkWG.Wait()
		}

		binary.LittleEndian.PutUint64(ent.chunk[:8], ent.subtreeSize)
		ent.key = make([]byte, p.hashSize)
		chunkWG.Add(1)
		select {
		case jobC <- &chunkJob{ent.key, ent.chunk[:ent.branchCount*p.hashSize+8], int64(ent.subtreeSize), chunkWG.Done, TreeChunk, 0}:
		case <-quitC:
		}

		// Update or append based on weather it is a new entry or being reused
		if ent.updatePending {
			chunkWG.Wait()
			chunkLevel[ent.level][ent.index] = ent
		} else {
			chunkLevel[ent.level] = append(chunkLevel[ent.level], ent)
		}

	}
}

func (p *PyramidChunker) enqueueDataChunk(chunkData []byte, size uint64, parent *TreeEntry, chunkWG *sync.WaitGroup, jobC chan *chunkJob, quitC chan bool) Key {
	binary.LittleEndian.PutUint64(chunkData[:8], size)
	pkey := parent.chunk[8+parent.branchCount*p.hashSize : 8+(parent.branchCount+1)*p.hashSize]

	chunkWG.Add(1)
	select {
	case jobC <- &chunkJob{pkey, chunkData[:size+8], int64(size), chunkWG.Done, DataChunk, -1}:
	case <-quitC:
	}

	return pkey

}
