package ethdb

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/log"
)

// Table represents key/value storage for a particular data type.
// Contains zero or more segments that are separated by partitioner.
type Table struct {
	mu          sync.RWMutex
	active      string                 // active segment name
	ldbSegments map[string]*LDBSegment // writable segments
	segments    *SegmentSet            // all segments

	Name        string
	Path        string
	Partitioner Partitioner

	MinMutableSegmentCount int

	// Maximum number of segments that can be opened at once.
	MaxOpenSegmentCount int

	SegmentOpener    SegmentOpener
	SegmentCompactor SegmentCompactor
}

// NewTable returns a new instance of Table.
func NewTable(name, path string, partitioner Partitioner) *Table {
	return &Table{
		Name:        name,
		Path:        path,
		Partitioner: partitioner,

		MinMutableSegmentCount: DefaultMinMutableSegmentCount,
		MaxOpenSegmentCount:    DefaultMaxOpenSegmentCount,

		SegmentOpener:    NewFileSegmentOpener(),
		SegmentCompactor: NewFileSegmentCompactor(),
	}
}

// Open initializes the table and all existing segments.
func (t *Table) Open() error {
	t.ldbSegments = make(map[string]*LDBSegment)
	t.segments = NewSegmentSet(t.MaxOpenSegmentCount)

	if err := os.MkdirAll(t.Path, 0777); err != nil {
		return err
	}

	names, err := t.SegmentOpener.ListSegmentNames(t.Path, t.Name)
	if err != nil {
		return err
	}

	for _, name := range names {
		path := filepath.Join(t.Path, name)

		// Determine the segment file type.
		typ, err := SegmentFileType(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		// Open appropriate segment type.
		switch typ {
		case SegmentLDB1:
			ldbSegment := NewLDBSegment(name, path)
			if err := ldbSegment.Open(); err != nil {
				t.Close()
				return err
			}
			t.ldbSegments[name] = ldbSegment

		default:
			segment, err := t.SegmentOpener.OpenSegment(t.Name, name, path)
			if err == ErrSegmentTypeUnknown {
				log.Info("unknown segment type, skipping", "filename", name)
				continue
			} else if err != nil {
				return err
			}
			t.segments.Add(segment)
		}

		// Set as active if it has the highest lexicographical name.
		if name > t.active {
			t.active = name
		}
	}
	return nil
}

// Close closes all segments within the table.
func (t *Table) Close() error {
	for _, segment := range t.ldbSegments {
		if err := segment.Close(); err != nil {
			return err
		}
	}
	if t.segments != nil {
		for _, segment := range t.segments.Slice() {
			if err := segment.Close(); err != nil {
				return err
			}
			t.segments.Remove(segment.Name())
		}
	}
	return nil
}

// ActiveSegmentName the name of the current active segment.
func (t *Table) ActiveSegmentName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active
}

// ActiveSegment returns the active segment.
func (t *Table) ActiveSegment() MutableSegment {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ldbSegments[t.active]
}

// SegmentPath returns the path of the named segment.
func (t *Table) SegmentPath(name string) string {
	return filepath.Join(t.Path, name)
}

// AcquireSegment returns a segment by name. Returns nil if segment does not exist.
// Must call ReleaseSegment when finished with the segment.
func (t *Table) AcquireSegment(name string) (Segment, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if s := t.ldbSegments[name]; s != nil {
		return s, nil
	}
	return t.segments.Acquire(name)
}

// ReleaseSegment releases a given segment.
func (t *Table) ReleaseSegment(s Segment) {
	switch s.(type) {
	case *LDBSegment:
		return
	default:
		t.segments.Release()
	}
}

// SegmentNames a sorted list of all segments names.
func (t *Table) SegmentNames() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	a := make([]string, 0, len(t.ldbSegments)+t.segments.Len())
	for _, s := range t.ldbSegments {
		a = append(a, s.Name())
	}
	for _, s := range t.segments.Slice() {
		a = append(a, s.Name())
	}
	sort.Strings(a)
	return a
}

// SegmentsSlice returns a sorted slice of all segments.
func (t *Table) SegmentSlice() []Segment {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.segmentSlice()
}

func (t *Table) segmentSlice() []Segment {
	a := make([]Segment, 0, len(t.ldbSegments)+t.segments.Len())
	for _, s := range t.ldbSegments {
		a = append(a, s)
	}
	for _, s := range t.segments.Slice() {
		a = append(a, s)
	}
	SortSegments(a)
	return a
}

func (t *Table) ldbSegmentSlice() []*LDBSegment {
	a := make([]*LDBSegment, 0, len(t.ldbSegments))
	for _, s := range t.ldbSegments {
		a = append(a, s)
	}
	sort.Slice(a, func(i, j int) bool { return a[i].Name() < a[j].Name() })
	return a
}

// CreateSegmentIfNotExists returns a mutable segment by name.
// Creates a new segment if it does not exist.
func (t *Table) CreateSegmentIfNotExists(name string) (MutableSegment, error) {
	t.mu.RLock()
	if s := t.ldbSegments[name]; s != nil {
		t.mu.RUnlock()
		return s, nil
	}
	t.mu.RUnlock()

	t.mu.Lock()
	defer t.mu.Unlock()

	// Recheck under write lock.
	if s := t.ldbSegments[name]; s != nil {
		return s, nil
	}

	// Check if immutable already.
	if t.segments.Contains(name) {
		return nil, ErrImmutableSegment
	}

	// Ensure segment name can become active.
	if name < t.active {
		log.Error("cannot non-active create segment", "name", name, "active", t.active)
		return nil, ErrImmutableSegment
	}

	// Create new mutable segment.
	ldbSegment := NewLDBSegment(name, t.SegmentPath(name))
	if err := ldbSegment.Open(); err != nil {
		return nil, err
	}
	t.ldbSegments[name] = ldbSegment

	// Set as active segment.
	t.active = name

	// Compact under lock.
	// TODO(benbjohnson): Run compaction in background if too slow.
	if err := t.compact(context.TODO()); err != nil {
		return nil, err
	}

	return ldbSegment, nil
}

// Has returns true if key exists in the table.
func (t *Table) Has(key []byte) (bool, error) {
	name := t.Partitioner.Partition(key)
	s, err := t.AcquireSegment(name)
	if err != nil {
		return false, err
	} else if s == nil {
		return false, ErrKeyNotFound
	}
	defer t.ReleaseSegment(s)
	return s.Has(key)
}

// Get returns the value associated with key.
func (t *Table) Get(key []byte) ([]byte, error) {
	name := t.Partitioner.Partition(key)
	s, err := t.AcquireSegment(name)
	if err != nil {
		return nil, err
	} else if s == nil {
		return nil, nil
	}
	defer t.ReleaseSegment(s)
	return s.Get(key)
}

// Put associates a value with key.
func (t *Table) Put(key, value []byte) error {
	// Ignore if value is the same.
	if v, err := t.Get(key); err != nil && err != ErrKeyNotFound {
		return err
	} else if bytes.Equal(v, value) {
		return nil
	}

	s, err := t.CreateSegmentIfNotExists(t.Partitioner.Partition(key))
	if err != nil {
		return err
	}
	return s.Put(key, value)
}

// Delete removes key from the database.
func (t *Table) Delete(key []byte) error {
	s, err := t.AcquireSegment(t.Partitioner.Partition(key))
	if err != nil {
		return err
	} else if s == nil {
		return nil
	}
	defer t.ReleaseSegment(s)

	switch s := s.(type) {
	case MutableSegment:
		return s.Delete(key)
	default:
		return ErrImmutableSegment
	}
}

func (t *Table) NewBatch() common.Batch {
	return &tableBatch{table: t, batches: make(map[string]*ldbSegmentBatch)}
}

// Compact converts LDB segments into immutable file segments.
func (t *Table) Compact(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.compact(ctx)
}

func (t *Table) compact(ctx context.Context) error {
	// Retrieve LDB segments. Exit if too few mutable segments.
	ldbSegmentSlice := t.ldbSegmentSlice()
	if len(ldbSegmentSlice) < t.MinMutableSegmentCount {
		return nil
	}

	for _, ldbSegment := range ldbSegmentSlice[:len(ldbSegmentSlice)-t.MinMutableSegmentCount] {

		startTime := time.Now()

		newSegment, err := t.SegmentCompactor.CompactSegment(ctx, t.Name, ldbSegment)
		if err != nil {
			return err
		}
		t.segments.Add(newSegment)
		delete(t.ldbSegments, ldbSegment.Name())

		log.Info("Compacted segment", "table", t.Name, "name", ldbSegment.Name(), "elapsed", time.Since(startTime))
	}
	return nil
}

type tableBatch struct {
	table   *Table
	batches map[string]*ldbSegmentBatch
	size    int
}

func (b *tableBatch) Put(key, value []byte) error {
	// Ignore if value is the same.
	if v, err := b.table.Get(key); err != nil && err != ErrKeyNotFound {
		return err
	} else if bytes.Equal(v, value) {
		return nil
	}

	name := b.table.Partitioner.Partition(key)
	segment, err := b.table.CreateSegmentIfNotExists(name)
	if err != nil {
		log.Error("tableBatch.Put: error", "table", b.table.Name, "segment", name, "key", fmt.Sprintf("%x", key))
		return err
	}

	ldbSegment, ok := segment.(*LDBSegment)
	if !ok {
		log.Error("cannot insert into compacted segment", "name", name, "key", fmt.Sprintf("%x", key))
		return ErrImmutableSegment
	}

	sb := b.batches[name]
	if sb == nil {
		sb = ldbSegment.newBatch()
		b.batches[name] = sb
	}
	if err := sb.Put(key, value); err != nil {
		return err
	}
	b.size += len(value)
	return nil
}

func (b *tableBatch) Write() error {
	for _, sb := range b.batches {
		if err := sb.Write(); err != nil {
			return err
		}
	}
	return nil
}

func (b *tableBatch) ValueSize() int {
	return b.size
}

func (b *tableBatch) Reset() {
	for _, sb := range b.batches {
		sb.Reset()
	}
	b.size = 0
}
