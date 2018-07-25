package ethdb_test

import (
	"testing"
	"time"

	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/ethdb/mock"
)

func TestSegmentSet_AcquireRelease(t *testing.T) {
	segment0 := &purgeableSegment{Segment: &mock.Segment{
		NameFunc: func() string { return "0000" },
	}}
	segment1 := &purgeableSegment{Segment: &mock.Segment{
		NameFunc: func() string { return "0001" },
	}}
	segment2 := &purgeableSegment{Segment: &mock.Segment{
		NameFunc: func() string { return "0002" },
	}}

	ss := ethdb.NewSegmentSet(2)
	ss.Add(segment0)
	ss.Add(segment1)
	ss.Add(segment2)

	// Ensure segments are not yet open.
	if segment0.opened {
		t.Fatal("expected unopen(0)")
	} else if segment1.opened {
		t.Fatal("expected unopen(1)")
	} else if segment2.opened {
		t.Fatal("expected unopen(2)")
	}

	// Acquire two segments.
	if s, err := ss.Acquire("0000"); err != nil {
		t.Fatal(err)
	} else if s != segment0 {
		t.Fatal("unexpected segment(0)")
	}

	if s, err := ss.Acquire("0001"); err != nil {
		t.Fatal(err)
	} else if s != segment1 {
		t.Fatal("unexpected segment(1)")
	}

	// Ensure only first two segments are open.
	if !segment0.opened {
		t.Fatal("expected open(0)")
	} else if !segment1.opened {
		t.Fatal("expected open(1)")
	} else if segment2.opened {
		t.Fatal("expected unopen(2)")
	}

	// Attempt to acquire in separate goroutine. Should block.
	acquired := make(chan struct{})
	go func() {
		if s, err := ss.Acquire("0002"); err != nil {
			t.Fatal(err)
		} else if s != segment2 {
			t.Fatal("unexpected segment(2)")
		}
		close(acquired)
	}()

	// Wait momentarily for goroutine to attempt acquisition.
	time.Sleep(500 * time.Millisecond)

	// Ensure second segment not acquired yet.
	select {
	case <-acquired:
		t.Fatal("unexpected acquisition(2)")
	default:
	}

	// Release one of the segments.
	ss.Release() // #0

	// Wait for acquisition to complete.
	<-acquired

	// Ensure only least recently used segment is purged.
	if !segment0.purged {
		t.Fatal("expected purge(0)")
	} else if segment1.purged {
		t.Fatal("unexpected purge(1)")
	} else if segment2.purged {
		t.Fatal("expected purge(2)")
	}

	ss.Release() // #1
	ss.Release() // #2
}

type purgeableSegment struct {
	ethdb.Segment
	opened bool
	purged bool
}

func (s *purgeableSegment) Open() error {
	s.opened = true
	return nil
}

func (s *purgeableSegment) Purge() error {
	s.purged = true
	return nil
}
