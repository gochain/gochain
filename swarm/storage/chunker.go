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
The distributed storage implemented in this package requires fix sized chunks of content.

Chunker is the interface to a component that is responsible for disassembling and assembling larger data.

TreeChunker implements a Chunker based on a tree structure defined as follows:

1 each node in the tree including the root and other branching nodes are stored as a chunk.

2 branching nodes encode data contents that includes the size of the dataslice covered by its entire subtree under the node as well as the hash keys of all its children :
data_{i} := size(subtree_{i}) || key_{j} || key_{j+1} .... || key_{j+n-1}

3 Leaf nodes encode an actual subslice of the input data.

4 if data size is not more than maximum chunksize, the data is stored in a single chunk
  key = hash(int64(size) + data)

5 if data size is more than chunksize*branches^l, but no more than chunksize*
  branches^(l+1), the data vector is split into slices of chunksize*
  branches^l length (except the last one).
  key = hash(int64(size) + key(slice0) + key(slice1) + ...)

 The underlying hash function is configurable
*/

/*
Tree chunker is a concrete implementation of data chunking.
This chunker works in a simple way, it builds a tree out of the document so that each node either represents a chunk of real data or a chunk of data representing an branching non-leaf node of the tree. In particular each such non-leaf chunk will represent is a concatenation of the hash of its respective children. This scheme simultaneously guarantees data integrity as well as self addressing. Abstract nodes are transparent since their represented size component is strictly greater than their maximum data size, since they encode a subtree.

If all is well it is possible to implement this by simply composing readers so that no extra allocation or buffering is necessary for the data splitting and joining. This means that in principle there can be direct IO between : memory, file system, network socket (bzz peers storage request is read from the socket). In practice there may be need for several stages of internal buffering.
The hashing itself does use extra copies and allocation though, since it does need it.
*/

var (
	errAppendOppNotSupported = errors.New("Append operation not supported")
)

type TreeChunker struct {
	branches int64
	hashFunc SwarmHasher
	// calculated
	hashSize    int64        // self.hashFunc.New().Size()
	chunkSize   int64        // hashSize* branches
	workerCount int64        // the number of worker routines used
	workerLock  sync.RWMutex // lock for the worker count
}

func NewTreeChunker(params *ChunkerParams) *TreeChunker {
	t := &TreeChunker{}
	t.hashFunc = MakeHashFunc(params.Hash)
	t.branches = params.Branches
	t.hashSize = int64(t.hashFunc().Size())
	t.chunkSize = t.hashSize * t.branches
	t.workerCount = 0
	return t
}

// String() for pretty printing
func (c *Chunk) String() string {
	return fmt.Sprintf("Key: %v TreeSize: %v Chunksize: %v", c.Key.Log(), c.Size, len(c.SData))
}

type hashJob struct {
	key   Key
	chunk []byte
	size  int64
	done  func()
}

func (t *TreeChunker) incrementWorkerCount() {
	t.workerLock.Lock()
	defer t.workerLock.Unlock()
	t.workerCount += 1
}

func (t *TreeChunker) getWorkerCount() int64 {
	t.workerLock.RLock()
	defer t.workerLock.RUnlock()
	return t.workerCount
}

func (t *TreeChunker) decrementWorkerCount() {
	t.workerLock.Lock()
	defer t.workerLock.Unlock()
	t.workerCount -= 1
}

func (t *TreeChunker) Split(data io.Reader, size int64, chunkC chan *Chunk, swg *sync.WaitGroup) (Key, error) {
	if t.chunkSize <= 0 {
		panic("chunker must be initialised")
	}

	jobC := make(chan *hashJob, 2*ChunkProcessors)
	var rootHash sync.WaitGroup
	quitC := make(chan bool)
	defer close(quitC)

	t.incrementWorkerCount()
	if swg != nil {
		swg.Add(1)
	}
	go t.hashWorker(jobC, chunkC, quitC, swg)

	depth := 0
	treeSize := t.chunkSize

	// takes lowest depth such that chunksize*HashCount^(depth+1) > size
	// power series, will find the order of magnitude of the data size in base hashCount or numbers of levels of branching in the resulting tree.
	for ; treeSize < size; treeSize *= t.branches {
		depth++
	}

	key := make([]byte, t.hashFunc().Size())
	rootHash.Add(1)
	err := t.split(depth, treeSize/t.branches, key, data, size, jobC, chunkC, quitC, rootHash.Done, swg)
	close(jobC)
	if err != nil {
		return nil, err
	}

	done := make(chan struct{})
	go func() {
		rootHash.Wait()
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

	return key, nil
}

func (t *TreeChunker) split(depth int, treeSize int64, key Key, data io.Reader, size int64, jobC chan *hashJob, chunkC chan *Chunk, quitC chan bool, hashDone func(), swg *sync.WaitGroup) error {
	for depth > 0 && size < treeSize {
		treeSize /= t.branches
		depth--
	}

	if depth == 0 {
		// leaf nodes -> content chunks
		chunkData := make([]byte, size+8)
		binary.LittleEndian.PutUint64(chunkData[0:8], uint64(size))
		var readBytes int64
		for readBytes < size {
			n, err := data.Read(chunkData[8+readBytes:])
			readBytes += int64(n)
			if err != nil && !(err == io.EOF && readBytes == size) {
				return err
			}
		}
		select {
		case jobC <- &hashJob{key, chunkData, size, hashDone}:
		case <-quitC:
		}
		return nil
	}
	// dept > 0
	// intermediate chunk containing child nodes hashes
	branchCnt := (size + treeSize - 1) / treeSize

	var chunk = make([]byte, branchCnt*t.hashSize+8)
	var pos, i int64

	binary.LittleEndian.PutUint64(chunk[0:8], uint64(size))

	var childHashes sync.WaitGroup
	var secSize int64
	for i < branchCnt {
		// the last item can have shorter data
		if size-pos < treeSize {
			secSize = size - pos
		} else {
			secSize = treeSize
		}
		// the hash of that data
		subTreeKey := chunk[8+i*t.hashSize : 8+(i+1)*t.hashSize]

		childHashes.Add(1)
		err := t.split(depth-1, treeSize/t.branches, subTreeKey, data, secSize, jobC, chunkC, quitC, childHashes.Done, swg)
		if err != nil {
			return err
		}

		i++
		pos += treeSize
	}
	childHashes.Wait()

	worker := t.getWorkerCount()
	if int64(len(jobC)) > worker && worker < ChunkProcessors {
		t.incrementWorkerCount()
		if swg != nil {
			swg.Add(1)
		}
		go t.hashWorker(jobC, chunkC, quitC, swg)
	}
	select {
	case jobC <- &hashJob{key, chunk, size, hashDone}:
	case <-quitC:
	}
	return nil
}

func (t *TreeChunker) hashWorker(jobC <-chan *hashJob, chunkC chan *Chunk, quitC chan bool, swg *sync.WaitGroup) {
	defer t.decrementWorkerCount()
	if swg != nil {
		swg.Done()
	}

	hasher := t.hashFunc()
	for {
		select {
		case job, ok := <-jobC:
			if !ok {
				return
			}
			// now we got the hashes in the chunk, then hash the chunks
			t.hashChunk(hasher, job, chunkC, swg)
		case <-quitC:
			return
		}
	}
}

// The treeChunkers own Hash hashes together
// - the size (of the subtree encoded in the Chunk)
// - the Chunk, ie. the contents read from the input reader
func (t *TreeChunker) hashChunk(hasher SwarmHash, job *hashJob, chunkC chan *Chunk, swg *sync.WaitGroup) {
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

func (t *TreeChunker) Append(key Key, data io.Reader, chunkC chan *Chunk, swg *sync.WaitGroup) (Key, error) {
	return nil, errAppendOppNotSupported
}

// LazyChunkReader implements LazySectionReader
type LazyChunkReader struct {
	key       Key         // root key
	chunkC    chan *Chunk // chunk channel to send retrieve requests on
	chunk     *Chunk      // size of the entire subtree
	off       int64       // offset
	chunkSize int64       // inherit from chunker
	branches  int64       // inherit from chunker
	hashSize  int64       // inherit from chunker
}

// implements the Joiner interface
func (t *TreeChunker) Join(key Key, chunkC chan *Chunk) LazySectionReader {
	return &LazyChunkReader{
		key:       key,
		chunkC:    chunkC,
		chunkSize: t.chunkSize,
		branches:  t.branches,
		hashSize:  t.hashSize,
	}
}

// Size is meant to be called on the LazySectionReader
func (l *LazyChunkReader) Size(quitC chan bool) (n int64, err error) {
	if l.chunk != nil {
		return l.chunk.Size, nil
	}
	chunk := retrieve(l.key, l.chunkC, quitC)
	if chunk == nil {
		select {
		case <-quitC:
			return 0, errors.New("aborted")
		default:
			return 0, fmt.Errorf("root chunk not found for %v", l.key.Hex())
		}
	}
	l.chunk = chunk
	return chunk.Size, nil
}

// read at can be called numerous times
// concurrent reads are allowed
// Size() needs to be called synchronously on the LazyChunkReader first
func (l *LazyChunkReader) ReadAt(b []byte, off int64) (read int, err error) {
	// this is correct, a swarm doc cannot be zero length, so no EOF is expected
	if len(b) == 0 {
		return 0, nil
	}
	quitC := make(chan bool)
	size, err := l.Size(quitC)
	if err != nil {
		return 0, err
	}

	errC := make(chan error)

	// }
	var treeSize int64
	var depth int
	// calculate depth and max treeSize
	treeSize = l.chunkSize
	for ; treeSize < size; treeSize *= l.branches {
		depth++
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go l.join(b, off, off+int64(len(b)), depth, treeSize/l.branches, l.chunk, &wg, errC, quitC)
	go func() {
		wg.Wait()
		close(errC)
	}()

	err = <-errC
	if err != nil {
		close(quitC)

		return 0, err
	}
	if off+int64(len(b)) >= size {
		return len(b), io.EOF
	}
	return len(b), nil
}

func (l *LazyChunkReader) join(b []byte, off int64, eoff int64, depth int, treeSize int64, chunk *Chunk, parentWg *sync.WaitGroup, errC chan error, quitC chan bool) {
	defer parentWg.Done()

	// find appropriate block level
	for chunk.Size < treeSize && depth > 0 {
		treeSize /= l.branches
		depth--
	}

	// leaf chunk found
	if depth == 0 {
		extra := 8 + eoff - int64(len(chunk.SData))
		if extra > 0 {
			eoff -= extra
		}
		copy(b, chunk.SData[8+off:8+eoff])
		return // simply give back the chunks reader for content chunks
	}

	// subtree
	start := off / treeSize
	end := (eoff + treeSize - 1) / treeSize

	wg := &sync.WaitGroup{}
	defer wg.Wait()

	for i := start; i < end; i++ {
		soff := i * treeSize
		roff := soff
		seoff := soff + treeSize

		if soff < off {
			soff = off
		}
		if seoff > eoff {
			seoff = eoff
		}
		if depth > 1 {
			wg.Wait()
		}
		wg.Add(1)
		go func(j int64) {
			childKey := chunk.SData[8+j*l.hashSize : 8+(j+1)*l.hashSize]
			chunk := retrieve(childKey, l.chunkC, quitC)
			if chunk == nil {
				select {
				case errC <- fmt.Errorf("chunk %v-%v not found", off, off+treeSize):
				case <-quitC:
				}
				return
			}
			if soff < off {
				soff = off
			}
			l.join(b[soff-off:seoff-off], soff-roff, seoff-roff, depth-1, treeSize/l.branches, chunk, wg, errC, quitC)
		}(i)
	} //for
}

// the helper method submits chunks for a key to a oueue (DPA) and
// block until they time out or arrive
// abort if quitC is readable
func retrieve(key Key, chunkC chan *Chunk, quitC chan bool) *Chunk {
	chunk := &Chunk{
		Key: key,
		C:   make(chan bool), // close channel to signal data delivery
	}
	// submit chunk for retrieval
	select {
	case chunkC <- chunk: // submit retrieval request, someone should be listening on the other side (or we will time out globally)
	case <-quitC:
		return nil
	}
	// waiting for the chunk retrieval
	select { // chunk.Size = int64(binary.LittleEndian.Uint64(chunk.SData[0:8]))

	case <-quitC:
		// this is how we control process leakage (quitC is closed once join is finished (after timeout))
		return nil
	case <-chunk.C: // bells are ringing, data have been delivered
	}
	if len(chunk.SData) == 0 {
		return nil // chunk.Size = int64(binary.LittleEndian.Uint64(chunk.SData[0:8]))

	}
	return chunk
}

// Read keeps a cursor so cannot be called simulateously, see ReadAt
func (l *LazyChunkReader) Read(b []byte) (read int, err error) {
	read, err = l.ReadAt(b, l.off)

	l.off += int64(read)
	return
}

// completely analogous to standard SectionReader implementation
var errWhence = errors.New("Seek: invalid whence")
var errOffset = errors.New("Seek: invalid offset")

func (l *LazyChunkReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	default:
		return 0, errWhence
	case 0:
		offset += 0
	case 1:
		offset += l.off
	case 2:
		if l.chunk == nil { //seek from the end requires rootchunk for size. call Size first
			_, err := l.Size(nil)
			if err != nil {
				return 0, fmt.Errorf("can't get size: %v", err)
			}
		}
		offset += l.chunk.Size
	}

	if offset < 0 {
		return 0, errOffset
	}
	l.off = offset
	return offset, nil
}
