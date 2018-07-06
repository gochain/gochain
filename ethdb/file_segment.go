package ethdb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/cespare/xxhash"
	"github.com/edsrzf/mmap-go"
	"github.com/gochain-io/gochain/log"
)

var (
	ErrKeyNotFound          = errors.New("ethdb: key not found")
	ErrFileSegmentImmutable = errors.New("ethdb: file segment immutable")
)

const (
	// FileSegmentMagic is the magic number at the beginning of the file segment.
	FileSegmentMagic = "ETH1"

	// FileSegmentChecksumSize is the size of the checksum, in bytes.
	FileSegmentChecksumSize = 8

	// FileSegmentIndexOffsetSize is the size of the index offset, in bytes.
	FileSegmentIndexOffsetSize = 8

	// FileSegmentIndexCountSize is the size of the index element count, in bytes.
	FileSegmentIndexCountSize = 8

	// FileSegmentIndexCapacitySize is the size of the index capacity, in bytes.
	FileSegmentIndexCapacitySize = 8

	// FileSegmentHeaderSize is the total size of the fixed length FileSegment header.
	FileSegmentHeaderSize = len(FileSegmentMagic) + FileSegmentChecksumSize + FileSegmentIndexOffsetSize + FileSegmentIndexCountSize + FileSegmentIndexCapacitySize
)

// Ensure implementation implements interface.
var _ Segment = (*FileSegment)(nil)

// FileSegment represents an immutable key/value file segment for a table.
type FileSegment struct {
	name string // segment name
	path string // on-disk path
	data []byte // memory-mapped data
}

// NewFileSegment returns a new instance of FileSegment.
func NewFileSegment(name, path string) *FileSegment {
	return &FileSegment{
		name: name,
		path: path,
	}
}

// Open opens and initializes the file segment.
func (s *FileSegment) Open() error {
	file, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Memory-map data.
	data, err := mmap.Map(file, mmap.RDONLY, 0)
	if err != nil {
		return err
	}
	s.data = []byte(data)

	// Ensure header information is valid.
	if len(data) < FileSegmentHeaderSize {
		s.Close()
		return errors.New("ethdb: file header too short")
	} else if string(data[:len(FileSegmentMagic)]) != FileSegmentMagic {
		s.Close()
		return errors.New("ethdb: invalid ethdb file")
	}
	return nil
}

// Close closes the file and its mmap.
func (s *FileSegment) Close() error {
	if s.data != nil {
		if err := (*mmap.MMap)(&s.data).Unmap(); err != nil {
			return err
		}
		s.data = nil
	}
	return nil
}

// Name returns the name of the segment.
func (s *FileSegment) Name() string { return s.name }

// Path returns the path of the segment.
func (s *FileSegment) Path() string { return s.path }

// Size returns the size of the underlying data file.
func (s *FileSegment) Size() int {
	return len(s.data)
}

// Len returns the number of keys in the file.
func (s *FileSegment) Len() int {
	if s.data == nil {
		return 0
	}
	data := s.data[len(FileSegmentMagic)+FileSegmentChecksumSize+FileSegmentIndexOffsetSize:]
	return int(binary.BigEndian.Uint64(data[:FileSegmentIndexCountSize]))
}

// index returns the byte slice containing the index.
func (s *FileSegment) index() []byte {
	if s.data == nil {
		return nil
	}
	return s.data[s.indexOffset():]
}

// indexOffset returns the file offset where the index starts.
func (s *FileSegment) indexOffset() int64 {
	if s.data == nil {
		return -1
	}
	return int64(binary.BigEndian.Uint64(s.data[len(FileSegmentMagic)+FileSegmentChecksumSize:]))
}

// capacity returns the capacity of the index.
func (s *FileSegment) capacity() int {
	if s.data == nil {
		return 0
	}
	data := s.data[len(FileSegmentMagic)+FileSegmentChecksumSize+FileSegmentIndexOffsetSize+FileSegmentIndexCountSize:]
	return int(binary.BigEndian.Uint64(data[:FileSegmentIndexCapacitySize]))
}

// Has returns true if the key exists.
func (s *FileSegment) Has(key []byte) (bool, error) {
	koff, _ := s.offset(key)
	return koff != 0, nil
}

// Get returns the value of the given key.
func (s *FileSegment) Get(key []byte) ([]byte, error) {
	_, voff := s.offset(key)
	if voff == 0 {
		return nil, ErrKeyNotFound
	}

	// Read value.
	data := s.data[voff:]
	n, sz := binary.Uvarint(data)
	return data[sz : sz+int(n) : sz+int(n)], nil
}

// Put returns an error. FileSegment is immutable.
func (s *FileSegment) Put(key, value []byte) error {
	log.Error("ethdb.FileSegment.Put() immutable")
	return ErrFileSegmentImmutable
}

// Delete returns an error. FileSegment is immutable.
func (s *FileSegment) Delete(key []byte) error {
	log.Error("ethdb.FileSegment.Put() immutable")
	return ErrFileSegmentImmutable
}

// Iterator returns an iterator for iterating over all key/value pairs.
func (s *FileSegment) Iterator() *FileSegmentIterator {
	return &FileSegmentIterator{
		data:   s.data[:s.indexOffset()],
		offset: int64(FileSegmentHeaderSize),
	}
}

// offset returns the offset of key & value. Returns 0 if key does not exist.
func (s *FileSegment) offset(key []byte) (koff, voff int64) {
	capacity := uint64(s.capacity())
	if capacity == 0 {
		return 0, 0
	}
	mask := capacity - 1

	idx := s.index()
	hash := hashKey(key)
	pos := hash & mask

	for d := uint64(0); ; d++ {
		// Exit if empty slot found.
		offset := int64(binary.BigEndian.Uint64(idx[pos*8:]))
		if offset == 0 {
			return 0, 0
		}

		// Read current key & compute hash.
		data := s.data[offset:]
		n, sz := binary.Uvarint(data)
		curr := data[sz : sz+int(n)]
		currHash := hashKey(curr)

		// Exit if distance exceeds current slot or key matches.
		if d > dist(currHash, pos, capacity, mask) {
			return 0, 0
		} else if currHash == hash && bytes.Equal(curr, key) {
			return offset, offset + int64(sz) + int64(n)
		}
		pos = (pos + 1) & mask
	}
}

// FileSegmentIterator returns an error for sequentially iterating over a FileSegment's key/value pairs.
type FileSegmentIterator struct {
	data   []byte
	offset int64

	key   []byte
	value []byte
}

// Key returns the current key. Must be called after Next().
func (itr *FileSegmentIterator) Key() []byte { return itr.key }

// Value returns the current key. Must be called after Next().
func (itr *FileSegmentIterator) Value() []byte { return itr.value }

// Next reads the next key/value pair into the buffer.
func (itr *FileSegmentIterator) Next() bool {
	if itr.offset >= int64(len(itr.data)) {
		return false
	}

	// Read key.
	n, sz := binary.Uvarint(itr.data[itr.offset:])
	itr.key = itr.data[itr.offset+int64(sz) : itr.offset+int64(sz+int(n))]
	itr.offset += int64(sz + int(n))

	// Read value.
	n, sz = binary.Uvarint(itr.data[itr.offset:])
	itr.value = itr.data[itr.offset+int64(sz) : itr.offset+int64(sz+int(n))]
	itr.offset += int64(sz + int(n))

	return true
}

// FileSegmentEncoder represents a encoder for building a ethdb.FileSegment.
type FileSegmentEncoder struct {
	f       *os.File
	flushed bool

	offset  int64
	offsets []int64

	// Filename of file segment to encode.
	Path string
}

func NewFileSegmentEncoder(path string) *FileSegmentEncoder {
	return &FileSegmentEncoder{
		Path: path,
	}
}

// Open opens and initializes the output file segment.
func (enc *FileSegmentEncoder) Open() (err error) {
	if enc.f != nil {
		return errors.New("ethdb: file already open")
	}
	if enc.f, err = os.OpenFile(enc.Path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666); err != nil {
		return err
	}

	// Write magic & leave space for checksum & index offset.
	if _, err := enc.f.Write([]byte(FileSegmentMagic)); err != nil {
		enc.Close()
		return err
	} else if _, err := enc.f.Write(make([]byte, FileSegmentHeaderSize-len(FileSegmentMagic))); err != nil {
		enc.Close()
		return err
	}
	enc.offset = int64(FileSegmentHeaderSize)

	return nil
}

// Close closes the file handle. File must be flushed before calling close.
func (enc *FileSegmentEncoder) Close() error {
	if enc.f != nil {
		if err := enc.f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Flush finalizes the file segment and appends a hashmap & trailer.
func (enc *FileSegmentEncoder) Flush() error {
	if enc.flushed {
		return errors.New("ethdb: file index already flushed")
	}
	enc.flushed = true

	if err := enc.writeIndex(); err != nil {
		return fmt.Errorf("ethdb: cannot write index: %s", err)
	} else if err := enc.writeChecksum(); err != nil {
		return fmt.Errorf("ethdb: cannot write checksum: %s", err)
	} else if err := enc.f.Sync(); err != nil {
		return err
	}
	return nil
}

// EncodeKeyValue writes framed key & value byte slices to the file and records their offset.
func (enc *FileSegmentEncoder) EncodeKeyValue(key, value []byte) error {
	buf := make([]byte, binary.MaxVarintLen64)
	offset := enc.offset

	// Write key len + data.
	n := binary.PutUvarint(buf, uint64(len(key)))
	if err := enc.write(buf[:n]); err != nil {
		return err
	} else if err := enc.write(key); err != nil {
		return err
	}

	// Write value len + data.
	n = binary.PutUvarint(buf, uint64(len(value)))
	if err := enc.write(buf[:n]); err != nil {
		return err
	} else if err := enc.write(value); err != nil {
		return err
	}

	enc.offsets = append(enc.offsets, offset)
	return nil
}

func (enc *FileSegmentEncoder) write(b []byte) error {
	n, err := enc.f.Write(b)
	enc.offset += int64(n)
	return err
}

func (enc *FileSegmentEncoder) writeIndex() error {
	// Save offset to the start of the index.
	indexOffset := enc.offset

	// Open separate handler to reasd on-disk data.
	f, err := os.Open(enc.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Build index in-memory.
	idx := newFileSegmentEncoderIndex(f, len(enc.offsets))
	for _, offset := range enc.offsets {
		if err := idx.insert(offset); err != nil {
			return err
		}
	}

	// Encode index to writer.
	if _, err := idx.WriteTo(enc.f); err != nil {
		return err
	}

	// Write length, capacity & index offset to the header.
	hdr := make([]byte, FileSegmentIndexOffsetSize+FileSegmentIndexCountSize+FileSegmentIndexCapacitySize)
	binary.BigEndian.PutUint64(hdr[0:8], uint64(indexOffset))
	binary.BigEndian.PutUint64(hdr[8:16], uint64(len(enc.offsets)))
	binary.BigEndian.PutUint64(hdr[16:24], uint64(idx.capacity()))
	if _, err := enc.f.Seek(int64(len(FileSegmentMagic)+FileSegmentChecksumSize), io.SeekStart); err != nil {
		return err
	} else if _, err := enc.f.Write(hdr); err != nil {
		return err
	} else if err := enc.f.Sync(); err != nil {
		return err
	}
	return nil
}

func (enc *FileSegmentEncoder) writeChecksum() error {
	// Open read-only handler to compute checksum.
	f, err := os.Open(enc.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Compute checksum for all data after checksum.
	h := xxhash.New()
	if _, err := f.Seek(int64(len(FileSegmentMagic)+FileSegmentChecksumSize), io.SeekStart); err != nil {
		return err
	} else if _, err := io.Copy(h, f); err != nil {
		return err
	}

	// Write checksum to the header.
	buf := make([]byte, FileSegmentChecksumSize)
	binary.BigEndian.PutUint64(buf, h.Sum64())
	if _, err := enc.f.Seek(int64(len(FileSegmentMagic)), io.SeekStart); err != nil {
		return err
	} else if _, err := enc.f.Write(buf); err != nil {
		return err
	} else if err := enc.f.Sync(); err != nil {
		return err
	}
	return nil
}

// fileSegmentEncoderIndex represents a fixed-length RHH-based hash map.
// The map does not support insertion of duplicate keys.
//
// https://cs.uwaterloo.ca/research/tr/1986/CS-86-14.pdf
type fileSegmentEncoderIndex struct {
	src   io.ReadSeeker
	r     *bufio.Reader
	mask  uint64
	elems []int64
}

// newFileSegmentEncoderIndex returns a new instance of fileSegmentEncoderIndex.
func newFileSegmentEncoderIndex(src io.ReadSeeker, n int) *fileSegmentEncoderIndex {
	idx := &fileSegmentEncoderIndex{
		src: src,
		r:   bufio.NewReader(src),
	}

	// Determine maximum capacity by padding length and finding next power of 2.
	const loadFactor = 90
	capacity := pow2(uint64((n * 100) / loadFactor))

	idx.elems = make([]int64, capacity)
	idx.mask = uint64(capacity - 1)

	return idx
}

// WriteTo writes the index to w. Implements io.WriterTo.
func (idx *fileSegmentEncoderIndex) WriteTo(w io.Writer) (n int64, err error) {
	buf := make([]byte, 8)
	for _, elem := range idx.elems {
		binary.BigEndian.PutUint64(buf, uint64(elem))

		nn, err := w.Write(buf)
		if n += int64(nn); err != nil {
			return n, err
		}
	}
	return n, nil
}

// capacity returns the computed capacity based on the initial count.
func (idx *fileSegmentEncoderIndex) capacity() int {
	return len(idx.elems)
}

// insert writes the element at the given offset to the index.
func (idx *fileSegmentEncoderIndex) insert(offset int64) error {
	key, err := idx.readAt(offset)
	if err != nil {
		return err
	}
	pos := hashKey(key) & idx.mask
	capacity := uint64(len(idx.elems))

	var d uint64
	for {
		// Exit empty slot exists.
		if idx.elems[pos] == 0 {
			idx.elems[pos] = offset
			return nil
		}

		// Read key at current position.
		curr, err := idx.readAt(idx.elems[pos])
		if err != nil {
			return err
		}

		// Return an error if a duplicate key exists.
		if bytes.Equal(curr, key) {
			return errors.New("ethdb: duplicate key written to file segment")
		}

		// Swap if current element has a lower probe distance.
		tmp := dist(hashKey(curr), pos, capacity, idx.mask)
		if tmp < d {
			offset, idx.elems[pos], d = idx.elems[pos], offset, tmp
		}

		// Move position forward.
		pos = (pos + 1) & idx.mask
		d++
	}
}

func dist(hash, i, capacity, mask uint64) uint64 {
	return ((i + capacity) - (hash & mask)) & mask
}

// readAt reads the key at the given offset.
func (idx *fileSegmentEncoderIndex) readAt(offset int64) ([]byte, error) {
	idx.r.Reset(idx.src)
	if _, err := idx.src.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}

	// Read key length.
	n, err := binary.ReadUvarint(idx.r)
	if err != nil {
		return nil, err
	}

	// Read key.
	key := make([]byte, n)
	if _, err := io.ReadFull(idx.r, key); err != nil {
		return nil, err
	}
	return key, nil
}

func hashKey(key []byte) uint64 {
	h := xxhash.Sum64(key)
	if h == 0 {
		h = 1
	}
	return h
}

func pow2(v uint64) uint64 {
	for i := uint64(2); i < 1<<62; i *= 2 {
		if i >= v {
			return i
		}
	}
	panic("unreachable")
}

func hexdump(b []byte) { os.Stderr.Write([]byte(hex.Dump(b))) }
