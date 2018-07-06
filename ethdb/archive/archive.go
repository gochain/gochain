package archive

/*
import (
	"bytes"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/minio/minio-go"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/syndtr/goleveldb/leveldb"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/metrics"
)

const (
	// DefaultArchiveAge is the default distance from head at which data is archived.
	DefaultArchiveAge uint64 = 100000
	// DefaultArchivePeriod is how often the archive process runs.
	DefaultArchivePeriod = 5 * time.Minute
)

type Config struct {
	Endpoint   string        `toml:",omitempty"` // S3 compatible endpoint.
	Bucket     string        `toml:",omitempty"` // Bucket name. Must already exist.
	ID, Secret string        `toml:",omitempty"` // Credentials.
	Age        uint64        `toml:",omitempty"` // Optional. Distance from head before archiving.
	Period     time.Duration `toml:",omitempty"` // Optional. How often to run the archive process.
}

// Archive manages an archive of data in an S3 compatible bucket.
type Archive struct {
	client *minio.Client
	bucket string
	age    uint64
	period time.Duration

	// Meters for measuring archive request counts and latencies.
	getTimer gometrics.Timer
	putTimer gometrics.Timer
	hasTimer gometrics.Timer
	delTimer gometrics.Timer
}

// NewArchive returns a new Archive backed by an S3 compatible bucket.
// The bucket must already exist.
func NewArchive(config Config) (*Archive, error) {
	client, err := minio.New(config.Endpoint, config.ID, config.Secret, true)
	if err != nil {
		return nil, err
	}
	if ok, err := client.BucketExists(config.Bucket); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("bucket does not exist: %s", config.Bucket)
	}
	age := DefaultArchiveAge
	if config.Age != 0 {
		age = config.Age
	}
	period := DefaultArchivePeriod
	if config.Period != 0 {
		period = config.Period
	}
	return &Archive{client: client, bucket: config.Bucket, age: age, period: period}, nil
}

func (a *Archive) Put(key string, value []byte) (int64, error) {
	if a.putTimer != nil {
		defer a.putTimer.UpdateSince(time.Now())
	}
	return a.client.PutObject(a.bucket, key, bytes.NewReader(value), int64(len(value)), minio.PutObjectOptions{})
}

func (a *Archive) Get(key string) ([]byte, error) {
	if a.getTimer != nil {
		defer a.getTimer.UpdateSince(time.Now())
	}
	o, err := a.client.GetObject(a.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return ioutil.ReadAll(o)
}

func (a *Archive) Has(key string) (bool, error) {
	if a.hasTimer != nil {
		defer a.hasTimer.UpdateSince(time.Now())
	}
	_, err := a.client.StatObject(a.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		switch er := err.(type) {
		case minio.ErrorResponse:
			if er.Code == "NoSuchKey" {
				return false, nil
			}
		}
		return false, err
	}
	return true, nil
}

func (a *Archive) Delete(key string) error {
	if a.delTimer != nil {
		defer a.delTimer.UpdateSince(time.Now())
	}
	return a.client.RemoveObject(a.bucket, key)
}

func (a *Archive) Meter(prefix string) {
	if !metrics.Enabled {
		return
	}
	a.getTimer = metrics.NewTimer(prefix + "get")
	a.putTimer = metrics.NewTimer(prefix + "put")
	a.hasTimer = metrics.NewTimer(prefix + "has")
	a.delTimer = metrics.NewTimer(prefix + "del")
}

func prefixDir(prefix byte) string {
	switch prefix {
	case 'h':
		return "header"
	case 'b':
		return "body"
	case 'r':
		return "receipts"
	}
	return string(prefix)
}

func archiveKey(prefix byte, num uint64, hash common.Hash) string {
	return fmt.Sprintf("%s/%d-%s", prefixDir(prefix), num, hash.Hex())
}

// DB extends an LDBDatabase with support for archiving entries to a Archive.
type DB struct {
	*ethdb.DB
	archive *Archive

	done chan struct{} // Closed to signal sweep() to stop.
	loop chan struct{} // Closed by sweep() when complete.
}

// NewDB returns a new DB, backed by a Archive.
// Start() must be called to begin the background archival process.
func NewDB(db *ethdb.DB, archive *Archive) *DB {
	a := &DB{
		DB:      db,
		archive: archive,
		done:    make(chan struct{}),
		loop:    make(chan struct{})}
	return a
}

// Start launches the background goroutine to periodically sweeps for data to archive.
// It must only be called once.
func (db *DB) Start(limitFn func(byte) uint64) {
	go func() {
		defer close(db.loop)
		t := time.NewTicker(db.archive.period)
		defer t.Stop()
		for {
			select {
			case <-db.done:
				return
			case <-t.C:
				db.sweep(limitFn)
			}
		}
	}()
}

func (db *DB) Close() (err error) {
	close(db.done)
	err = db.DB.Close()
	<-db.loop
	return nil
}

func (db *DB) Get(key []byte) ([]byte, error) {
	val, err := db.DB.Get(key)
	if err != nil {
		if err == leveldb.ErrNotFound {
			ok, prefix, num, hash := core.DBArchiveKey(key)
			if ok {
				arKey := archiveKey(prefix, num, hash)
				val, err := db.archive.Get(arKey)
				if err != nil {
					return nil, err
				}
				_ = db.DB.Put(key, val)
				return val, nil
			}
		}
		return nil, err
	}
	return val, nil
}

func (db *DB) Has(key []byte) (bool, error) {
	if ok, err := db.DB.Has(key); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	ok, prefix, num, hash := core.DBArchiveKey(key)
	if ok {
		arKey := archiveKey(prefix, num, hash)
		return db.archive.Has(arKey)
	}
	return false, nil
}

func (db *DB) Delete(key []byte) error {
	ok, prefix, num, hash := core.DBArchiveKey(key)
	if ok {
		arKey := archiveKey(prefix, num, hash)
		if err := db.archive.Delete(arKey); err != nil {
			return err
		}
	}
	return db.DB.Delete(key)
}

// sweep archives data older than latest - DefaultArchiveAge.
func (db *DB) sweep(latest func(byte) uint64) {
	log.Info("Archive sweep started")
	type ret struct {
		entries    int
		totalBytes int64
	}
	var total ret
	retCnt := len(core.DBArchivePrefixes)
	retCh := make(chan ret, retCnt)
	for _, prefix := range core.DBArchivePrefixes {
		limit := latest(prefix) - db.archive.age
		if limit < 0 {
			retCnt--
			log.Info("Archive skipped", "type", prefixDir(prefix))
			continue
		}
		go func(prefix byte, limit uint64) {
			e, b := db.sweepPrefix(prefix, limit)
			retCh <- ret{entries: e, totalBytes: b}
		}(prefix, limit)
	}
wait:
	for {
		select {
		case <-db.done:
			log.Info("Archive sweep cancelled")
			return
		case cnt := <-retCh:
			total.entries += cnt.entries
			total.totalBytes += cnt.totalBytes
			retCnt--
			if retCnt == 0 {
				close(retCh)
				break wait
			}
		}
	}
	log.Info("Archive sweep finished", "count", total.entries, "size", common.StorageSize(total.totalBytes))
}

const sweepUpdateFreq = 1000

// sweepPrefix archives keys with the given prefix, which are older than limit.
func (db *DB) sweepPrefix(prefix byte, limit uint64) (int, int64) {
	log.Info("Archive worker started", "type", prefixDir(prefix), "limit", limit)
	var entries int
	var totalBytes int64

	// TODO: Archive via immutable segments.
		i := db.DB.NewIterator(util.BytesPrefix([]byte{prefix}), nil)
		for i.Next() {
			select {
			case <-db.done:
				break
			default:
			}
			key := i.Key()
			if len(key) < 9 {
				continue
			}
			ok, _, num, hash := core.DBArchiveKey(key)
			if !ok {
				continue
			}
			arKey := archiveKey(prefix, num, hash)
			if num < limit {
				if n, err := db.archive.Put(arKey, i.Value()); err != nil {
					log.Info("Archive entry failed", "key", arKey, "err", err)
				} else {
					entries++
					totalBytes += n
					if entries%sweepUpdateFreq == 0 {
						log.Info("Archive worker status update", "type", prefixDir(prefix), "limit", limit, "count", entries, "size", common.StorageSize(totalBytes))
					}
					if err := db.LDBDatabase.Delete(key); err != nil {
						// Note but move on. DB still consistent, and future run will clean up.
						log.Info("Archive entry successful, but failed local deletion", "key", arKey, "err", err)
					}
				}
			} else {
				// Everything left is more recent, so we're done.
				break
			}
		}
		i.Release()
		err := i.Error()
		if err != nil {
			log.Warn("Archive worker failed", "type", prefixDir(prefix), "limit", limit, "err", err)
		}

	return entries, totalBytes
}
*/
