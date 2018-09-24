package log

import (
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/go-stack/stack"
)

// Handler defines where and how log records are written.
// A Logger prints its log records by writing to a Handler.
// Handlers are composable, providing you great flexibility in combining
// them to achieve the logging structure that suits your applications.
type Handler interface {
	Log(r *Record) error
	// IsLogging returns true if global logging for level is enabled.
	IsLogging(Lvl) bool
}

// FuncHandler returns a Handler that logs records with the given
// functions.
func FuncHandler(log func(r *Record) error, isLogging func(Lvl) bool) Handler {
	return &funcHandler{
		log:       log,
		isLogging: isLogging,
	}
}

type funcHandler struct {
	log       func(r *Record) error
	isLogging func(Lvl) bool
}

func (h funcHandler) Log(r *Record) error {
	return h.log(r)
}

func (h funcHandler) IsLogging(level Lvl) bool {
	return h.isLogging(level)
}

// StreamHandler writes log records to an io.Writer
// with the given format. StreamHandler can be used
// to easily begin writing log records to other
// outputs.
//
// StreamHandler wraps itself with LazyHandler and SyncHandler
// to evaluate Lazy objects and perform safe concurrent writes.
func StreamHandler(wr io.Writer, fmtr Format) Handler {
	h := FuncHandler(func(r *Record) error {
		_, err := wr.Write(fmtr.Format(r))
		return err
	}, func(Lvl) bool { return true })
	return LazyHandler(SyncHandler(h))
}

// SyncHandler can be wrapped around a handler to guarantee that
// only a single Log operation can proceed at a time. It's necessary
// for thread-safe concurrent writes.
func SyncHandler(h Handler) Handler {
	var mu sync.Mutex
	return FuncHandler(func(r *Record) error {
		mu.Lock()
		err := h.Log(r)
		mu.Unlock()
		return err
	}, h.IsLogging)
}

// FileHandler returns a handler which writes log records to the give file
// using the given format. If the path
// already exists, FileHandler will append to the given file. If it does not,
// FileHandler will create the file with mode 0644.
func FileHandler(path string, fmtr Format) (Handler, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	return closingHandler{f, StreamHandler(f, fmtr)}, nil
}

// countingWriter wraps a WriteCloser object in order to count the written bytes.
type countingWriter struct {
	w     io.WriteCloser // the wrapped object
	count uint           // number of bytes written
}

// Write increments the byte counter by the number of bytes written.
// Implements the WriteCloser interface.
func (w *countingWriter) Write(p []byte) (n int, err error) {
	n, err = w.w.Write(p)
	w.count += uint(n)
	return n, err
}

// Close implements the WriteCloser interface.
func (w *countingWriter) Close() error {
	return w.w.Close()
}

// prepFile opens the log file at the given path, and cuts off the invalid part
// from the end, because the previous execution could have been finished by interruption.
// Assumes that every line ended by '\n' contains a valid log record.
func prepFile(path string) (*countingWriter, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Seek(-1, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 1)
	var cut int64
	for {
		if _, err := f.Read(buf); err != nil {
			return nil, err
		}
		if buf[0] == '\n' {
			break
		}
		if _, err = f.Seek(-2, io.SeekCurrent); err != nil {
			return nil, err
		}
		cut++
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	ns := fi.Size() - cut
	if err = f.Truncate(ns); err != nil {
		return nil, err
	}
	return &countingWriter{w: f, count: uint(ns)}, nil
}

// RotatingFileHandler returns a handler which writes log records to file chunks
// at the given path. When a file's size reaches the limit, the handler creates
// a new file named after the timestamp of the first log record it will contain.
func RotatingFileHandler(path string, limit uint, formatter Format) (Handler, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	files, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`\.log$`)
	last := len(files) - 1
	for last >= 0 && (!files[last].Mode().IsRegular() || !re.MatchString(files[last].Name())) {
		last--
	}
	var counter *countingWriter
	if last >= 0 && files[last].Size() < int64(limit) {
		// Open the last file, and continue to write into it until it's size reaches the limit.
		if counter, err = prepFile(filepath.Join(path, files[last].Name())); err != nil {
			return nil, err
		}
	}
	if counter == nil {
		counter = new(countingWriter)
	}
	h := StreamHandler(counter, formatter)

	return FuncHandler(func(r *Record) error {
		if counter.count > limit {
			counter.Close()
			counter.w = nil
		}
		if counter.w == nil {
			f, err := os.OpenFile(
				filepath.Join(path, fmt.Sprintf("%s.log", strings.Replace(r.Time.Format("060102150405.00"), ".", "", 1))),
				os.O_CREATE|os.O_APPEND|os.O_WRONLY,
				0600,
			)
			if err != nil {
				return err
			}
			counter.w = f
			counter.count = 0
		}
		return h.Log(r)
	}, h.IsLogging), nil
}

// NetHandler opens a socket to the given address and writes records
// over the connection.
func NetHandler(network, addr string, fmtr Format) (Handler, error) {
	conn, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}

	return closingHandler{conn, StreamHandler(conn, fmtr)}, nil
}

// XXX: closingHandler is essentially unused at the moment
// it's meant for a future time when the Handler interface supports
// a possible Close() operation
type closingHandler struct {
	io.WriteCloser
	Handler
}

func (h *closingHandler) Close() error {
	return h.WriteCloser.Close()
}

// CallerFileHandler returns a Handler that adds the line number and file of
// the calling function to the context with key "caller".
func CallerFileHandler(h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		r.Ctx = append(r.Ctx, "caller", fmt.Sprint(r.Call))
		return h.Log(r)
	}, h.IsLogging)
}

// FilterHandler returns a Handler that only writes records to the
// wrapped Handler if the given function evaluates true. For example,
// to only log records where the 'err' key is not nil:
//
//    logger.SetHandler(FilterHandler(func(r *Record) bool {
//        for i := 0; i < len(r.Ctx); i += 2 {
//            if r.Ctx[i] == "err" {
//                return r.Ctx[i+1] != nil
//            }
//        }
//        return false
//    }, h))
//
func FilterHandler(fn func(r *Record) bool, h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		if fn(r) {
			return h.Log(r)
		}
		return nil
	}, h.IsLogging)
}

// LvlFilterHandler returns a Handler that only writes
// records which are less than the given verbosity
// level to the wrapped Handler. For example, to only
// log Error/Crit records:
//
//     log.LvlFilterHandler(log.LvlError, log.StdoutHandler)
//
func LvlFilterHandler(maxLvl Lvl, h Handler) Handler {
	return FilterHandler(func(r *Record) (pass bool) {
		return r.Lvl <= maxLvl
	}, h)
}

// MultiHandler dispatches any write to each of its handlers.
// This is useful for writing different types of log information
// to different locations. For example, to log to a file and
// standard error:
//
//     log.MultiHandler(
//         log.Must.FileHandler("/var/log/app.log", log.LogfmtFormat()),
//         log.StderrHandler)
//
func MultiHandler(hs ...Handler) Handler {
	return FuncHandler(func(r *Record) error {
		for _, h := range hs {
			// what to do about failures?
			h.Log(r)
		}
		return nil
	}, func(lvl Lvl) bool {
		for _, h := range hs {
			if h.IsLogging(lvl) {
				return true
			}
		}
		return false
	})
}

// LazyHandler writes all values to the wrapped handler after evaluating
// any lazy functions in the record's context. It is already wrapped
// around StreamHandler and SyslogHandler in this library, you'll only need
// it if you write your own Handler.
func LazyHandler(h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		// go through the values (odd indices) and reassign
		// the values of any lazy fn to the result of its execution
		hadErr := false
		for i := 1; i < len(r.Ctx); i += 2 {
			lz, ok := r.Ctx[i].(Lazy)
			if ok {
				v, err := evaluateLazy(lz)
				if err != nil {
					hadErr = true
					r.Ctx[i] = err
				} else {
					if cs, ok := v.(stack.CallStack); ok {
						v = cs.TrimBelow(r.Call).TrimRuntime()
					}
					r.Ctx[i] = v
				}
			}
		}

		if hadErr {
			r.Ctx = append(r.Ctx, errorKey, "bad lazy")
		}

		return h.Log(r)
	}, h.IsLogging)
}

func evaluateLazy(lz Lazy) (interface{}, error) {
	t := reflect.TypeOf(lz.Fn)

	if t.Kind() != reflect.Func {
		return nil, fmt.Errorf("INVALID_LAZY, not func: %+v", lz.Fn)
	}

	if t.NumIn() > 0 {
		return nil, fmt.Errorf("INVALID_LAZY, func takes args: %+v", lz.Fn)
	}

	if t.NumOut() == 0 {
		return nil, fmt.Errorf("INVALID_LAZY, no func return val: %+v", lz.Fn)
	}

	value := reflect.ValueOf(lz.Fn)
	results := value.Call([]reflect.Value{})
	if len(results) == 1 {
		return results[0].Interface(), nil
	} else {
		values := make([]interface{}, len(results))
		for i, v := range results {
			values[i] = v.Interface()
		}
		return values, nil
	}
}

// DiscardHandler reports success for all writes but does nothing.
// It is useful for dynamically disabling logging at runtime via
// a Logger's SetHandler method.
func DiscardHandler() Handler {
	return FuncHandler(func(r *Record) error {
		return nil
	}, func(Lvl) bool { return false })
}

// The Must object provides the following Handler creation functions
// which instead of returning an error parameter only return a Handler
// and panic on failure: FileHandler, NetHandler, SyslogHandler, SyslogNetHandler
var Must muster

func must(h Handler, err error) Handler {
	if err != nil {
		panic(err)
	}
	return h
}

type muster struct{}

func (m muster) FileHandler(path string, fmtr Format) Handler {
	return must(FileHandler(path, fmtr))
}

func (m muster) NetHandler(network, addr string, fmtr Format) Handler {
	return must(NetHandler(network, addr, fmtr))
}
