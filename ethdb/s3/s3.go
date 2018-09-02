package s3

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/log"
	"github.com/minio/minio-go"
)

// ConfigureDB updates db to archive to S3 if S3 configuration enabled.
func ConfigureDB(db *ethdb.DB, config ethdb.Config) error {
	if config.Endpoint == "" || config.Bucket == "" {
		return nil
	}

	c := NewClient()
	c.Endpoint = config.Endpoint
	c.Bucket = config.Bucket
	c.AccessKeyID = config.AccessKeyID
	c.SecretAccessKey = config.SecretAccessKey
	if err := c.Open(); err != nil {
		log.Error("Cannot open S3 client", "err", err)
		return err
	}

	db.SegmentOpener = NewSegmentOpener(c)
	db.SegmentCompactor = NewSegmentCompactor(c)

	return nil
}

// Client represents a client to an S3 compatible bucket.
type Client struct {
	client *minio.Client

	// Connection information for S3-compatible bucket.
	// Must be set before calling Open().
	Endpoint string
	Bucket   string

	// Authentication for S3-compatible bucket.
	// Must be set before calling Open().
	AccessKeyID     string
	SecretAccessKey string
}

// NewClient returns a new instance of Client.
func NewClient() *Client {
	return &Client{}
}

func (c *Client) Open() (err error) {
	// Create minio client.
	ssl := !strings.HasPrefix(c.Endpoint, "127.0.0.1:")
	if c.client, err = minio.New(c.Endpoint, c.AccessKeyID, c.SecretAccessKey, ssl); err != nil {
		log.Error("Cannot create minio client", "err", err)
		return err
	}

	// Verify bucket exists.
	if ok, err := c.client.BucketExists(c.Bucket); err != nil {
		log.Error("Cannot verify bucket", "err", err)
		return err
	} else if !ok {
		return fmt.Errorf("ethdb/s3: bucket does not exist: %s", c.Bucket)
	}
	return nil
}

// ListObjectKeys returns a list of all object keys with a given prefix.
func (c *Client) ListObjectKeys(prefix string) ([]string, error) {
	log.Info("List s3 keys", "prefix", prefix)

	var keys []string
	for info := range c.client.ListObjects(c.Bucket, prefix, true, nil) {
		if info.Err != nil {
			return nil, info.Err
		}
		keys = append(keys, info.Key)
	}
	return keys, nil
}

// FGetObject fetches the object at key and writes it to path.
func (c *Client) FGetObject(ctx context.Context, key, path string) error {
	return c.client.FGetObjectWithContext(ctx, c.Bucket, key, path, minio.GetObjectOptions{})
}

// PutObject writes an object to a key.
func (c *Client) PutObject(ctx context.Context, key string, value []byte) (n int64, err error) {
	return c.client.PutObjectWithContext(ctx, c.Bucket, key, bytes.NewReader(value), int64(len(value)), minio.PutObjectOptions{})
}

// FPutObject writes an object to key from a file at path.
func (c *Client) FPutObject(ctx context.Context, key, path string) (n int64, err error) {
	return c.client.FPutObjectWithContext(ctx, c.Bucket, key, path, minio.PutObjectOptions{})
}

// RemoveObject removes an object by key.
func (c *Client) RemoveObject(ctx context.Context, key string) error {
	return c.client.RemoveObject(c.Bucket, key)
}

// Segment represents an ethdb.FileSegment stored in S3.
type Segment struct {
	mu      sync.RWMutex
	client  *Client
	segment *ethdb.FileSegment
	table   string // table name
	name    string // segment name
	path    string // local path
}

// NewSegment returns a new instance of Segment.
func NewSegment(client *Client, table, name, path string) *Segment {
	return &Segment{
		client: client,
		table:  table,
		name:   name,
		path:   path,
	}
}

// Name returns the name of the segment.
func (s *Segment) Name() string { return s.name }

// Path returns the local path of the segment.
func (s *Segment) Path() string { return s.path }

// Close closes the underlying file segment.
func (s *Segment) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.segment != nil {
		return s.segment.Close()
	}
	return nil
}

// Purge closes the underlying file segment and removes the on-disk file.
func (s *Segment) Purge() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.segment == nil {
		return nil
	}

	if err := s.segment.Close(); err != nil {
		return err
	} else if err := os.Remove(s.segment.Path()); err != nil {
		return err
	}
	s.segment = nil

	return nil
}

// ensureFileSegment instantiates the underlying file segment from the local disk.
// If the segment does not exist locally on disk then it is fetched from S3.
func (s *Segment) ensureFileSegment(ctx context.Context) error {
	// Exit if underlying segment exists.
	if s.segment != nil {
		return nil
	}

	// Fetch segment if it doesn't exist on disk.
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		log.Info("Fetch segment from s3", "key", SegmentKey(s.table, s.name))
		if err := s.client.FGetObject(ctx, SegmentKey(s.table, s.name), s.path); err != nil {
			log.Error("Cannot fetch segment from s3", "key", SegmentKey(s.table, s.name), "err", err)
			return err
		}
	} else if err != nil {
		return err
	}

	// Open file segment on the local file.
	s.segment = ethdb.NewFileSegment(s.name, s.path)
	if err := s.segment.Open(); err != nil {
		return err
	}

	return nil
}

// Has returns true if the key exists.
func (s *Segment) Has(key []byte) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ensureFileSegment(context.TODO()); err != nil {
		return false, err
	}
	return s.segment.Has(key)
}

// Get returns the value of the given key.
func (s *Segment) Get(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ensureFileSegment(context.TODO()); err != nil {
		return nil, err
	}
	return s.segment.Get(key)
}

// SegmentKey returns the key used for the segment on S3.
func SegmentKey(table, name string) string {
	return path.Join(table, name)
}

// Ensure implementation fulfills interface.
var _ ethdb.SegmentOpener = (*SegmentOpener)(nil)

// SegmentOpener opens segments as a s3.Segments.
type SegmentOpener struct {
	Client *Client
}

// NewSegmentOpener returns a new instance of SegmentOpener.
func NewSegmentOpener(client *Client) *SegmentOpener {
	return &SegmentOpener{Client: client}
}

// ListSegmentNames returns a list of segment names for a table.
func (o *SegmentOpener) ListSegmentNames(path, table string) ([]string, error) {
	// Fetch local keys.
	localKeys, err := ethdb.NewFileSegmentOpener().ListSegmentNames(path, table)
	if err != nil {
		return nil, err
	}

	// Fetch remote keys.
	remoteKeys, err := o.Client.ListObjectKeys(table)
	if err != nil {
		return nil, err
	}
	for i, key := range remoteKeys {
		remoteKeys[i] = strings.TrimPrefix(key, table+"/")
	}

	// Merge key sets.
	m := make(map[string]struct{})
	for _, k := range localKeys {
		m[k] = struct{}{}
	}
	for _, k := range remoteKeys {
		m[k] = struct{}{}
	}

	// Convert to slice.
	a := make([]string, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)

	return a, nil
}

// OpenSegment returns creates and opens a reference to a remote immutable segment.
func (o *SegmentOpener) OpenSegment(table, name, path string) (ethdb.Segment, error) {
	return NewSegment(o.Client, table, name, path), nil
}

// Ensure implementation fulfills interface.
var _ ethdb.SegmentCompactor = (*SegmentCompactor)(nil)

// SegmentCompactor wraps ethdb.FileSegmentCompactor and uploads to S3 after compaction.
type SegmentCompactor struct {
	Client *Client
}

// NewSegmentCompactor returns a new instance of SegmentCompactor.
func NewSegmentCompactor(client *Client) *SegmentCompactor {
	return &SegmentCompactor{Client: client}
}

// CompactSegment compacts s into a FileSegement and uploads it to S3.
func (c *SegmentCompactor) CompactSegment(ctx context.Context, table string, s *ethdb.LDBSegment) (ethdb.Segment, error) {
	fsc := ethdb.NewFileSegmentCompactor()

	tmpPath := s.Path() + ".tmp"
	if err := fsc.CompactSegmentTo(ctx, s, tmpPath); err != nil {
		return nil, err
	}

	if _, err := c.Client.FPutObject(ctx, SegmentKey(table, s.Name()), tmpPath); err != nil {
		return nil, err
	}

	// Close and remove both segments.
	if err := s.Close(); err != nil {
		return nil, err
	} else if err := os.RemoveAll(s.Path()); err != nil {
		return nil, err
	} else if err := os.Remove(tmpPath); err != nil {
		return nil, err
	}

	return NewSegment(c.Client, table, s.Name(), s.Path()), nil
}
