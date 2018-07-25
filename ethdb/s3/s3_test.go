// +build integration

package s3_test

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/ethdb/s3"
)

var (
	endpoint        = flag.String("endpoint", "", "s3 endpoint")
	bucket          = flag.String("bucket", "", "s3 bucket")
	accessKeyID     = flag.String("access-key-id", "", "access key id")
	secretAccessKey = flag.String("secret-access-key", "", "secret access key")
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func TestSegmentCompactor(t *testing.T) {
	dir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Open test segment.
	table := fmt.Sprintf("gochain-s3-%x", rand.Intn(65536))
	ldb := ethdb.NewLDBSegment("1234", filepath.Join(dir, "1234"))
	if err := ldb.Open(); err != nil {
		t.Fatal(err)
	}
	defer ldb.Close()

	// Write to segment.
	if err := ldb.Put([]byte("foo"), []byte("bar")); err != nil {
		t.Fatal(err)
	} else if err := ldb.Put([]byte("baz"), []byte("bat")); err != nil {
		t.Fatal(err)
	}

	// Compact and upload segment.
	client := MustOpenClient()
	sc := s3.NewSegmentCompactor(client)
	segment, err := sc.CompactSegment(context.Background(), table, ldb)
	if err != nil {
		t.Fatal(err)
	}

	// Verify keys are accessible.
	if v, err := segment.Get([]byte("baz")); err != nil {
		t.Fatal(err)
	} else if string(v) != "bat" {
		t.Fatalf("unexpected value: %q", string(v))
	}

	// Purge local data & retry key.
	if err := segment.(*s3.Segment).Purge(); err != nil {
		t.Fatal(err)
	}
	if v, err := segment.Get([]byte("baz")); err != nil {
		t.Fatal(err)
	} else if string(v) != "bat" {
		t.Fatalf("unexpected value: %q", string(v))
	}

	// Remove object from bucket.
	if err := client.RemoveObject(context.Background(), s3.SegmentKey(table, ldb.Name())); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentOpener_ListSegmentKeys(t *testing.T) {
	dir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write objects to bucket.
	table := fmt.Sprintf("gochain-s3-%x", rand.Intn(65536))
	client := MustOpenClient()
	if _, err := client.PutObject(context.Background(), s3.SegmentKey(table, "0000"), []byte("foo")); err != nil {
		t.Fatal(err)
	} else if _, err := client.PutObject(context.Background(), s3.SegmentKey(table, "0001"), []byte("bar")); err != nil {
		t.Fatal(err)
	}

	// List names.
	so := s3.NewSegmentOpener(client)
	if names, err := so.ListSegmentNames("", table); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(names, []string{"0000", "0001"}) {
		t.Fatalf("unexpected names: %#v", names)
	}

	// Remove objects from bucket.
	if err := client.RemoveObject(context.Background(), s3.SegmentKey(table, "0000")); err != nil {
		t.Fatal(err)
	} else if err := client.RemoveObject(context.Background(), s3.SegmentKey(table, "0001")); err != nil {
		t.Fatal(err)
	}
}

// NewClient returns a client with the test flags set.
func NewClient() *s3.Client {
	c := s3.NewClient()
	c.Endpoint = *endpoint
	c.Bucket = *bucket
	c.AccessKeyID = *accessKeyID
	c.SecretAccessKey = *secretAccessKey
	return c
}

// MustOpenClient opens client using test flag arguments.
func MustOpenClient() *s3.Client {
	c := NewClient()
	if err := c.Open(); err != nil {
		panic(err)
	}
	return c
}
