package perfutils

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"
)

type section int

const (
	ApplyMessage = iota
	ApplyTransaction
	CliqueSeal
	CliqueSnapshot
	CliqueVerifySeal
	CreateBloom
	Finalize
	GetLogs
	IntermediateRoot
	NewEVMContext
	NewEVMPool
	NewReceipt
	Process
	SignerSender
	StatedbFinalize
	StatedbPrepare
	TxAsMessage
)

var sectionNames = [...]string{
	ApplyMessage:     "ApplyMessage",
	ApplyTransaction: "ApplyTransaction",
	CliqueSeal:       "Clique.Seal",
	CliqueSnapshot:   "Clique.snapshot",
	CliqueVerifySeal: "Clique.verifySeal",
	CreateBloom:      "CreateBloom",
	Finalize:         "Finalize",
	GetLogs:          "GetLogs",
	IntermediateRoot: "IntermediateRoot",
	NewEVMContext:    "NewEVMContext",
	NewEVMPool:       "NewEVMPool",
	NewReceipt:       "NewReceipt",
	Process:          "Process",
	StatedbFinalize:  "statedb.Finalize",
	StatedbPrepare:   "statedb.Prepare",
	SignerSender:     "signer.Sender",
	TxAsMessage:      "tx.AsMessage",
}

func (s section) String() string {
	return sectionNames[s]
}

func NewPerfTimer() PerfTimer {
	return &PerfTimerNormal{}
}

// PerfTimer is a way to time pieces of code, in particular ones that happen many times,
// then get the metrics for it.
type PerfTimer interface {
	Start(section) PerfRun
	Print() string
	Fprint(w io.Writer)
}

// PerfTimerNormal great name huh?
type PerfTimerNormal struct {
	// Sections map[string]*PerfSectionNormal
	Sections sync.Map
}

func (pt *PerfTimerNormal) Start(s section) PerfRun {
	// ps := pt.Sections[sectionName]
	var ps *PerfSectionNormal
	psl, _ := pt.Sections.LoadOrStore(s, &PerfSectionNormal{s: s})
	ps = psl.(*PerfSectionNormal)
	return &PerfRunNormal{ps: ps, startTime: time.Now()}
}
func (pt *PerfTimerNormal) Print() string {
	var s strings.Builder
	fmt.Fprintln(&s)
	pt.Fprint(&s)
	return s.String()
}
func (pt *PerfTimerNormal) Fprint(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 8, 1, '\t', 0)
	fmt.Fprintln(tw, "Section\tCount\tTotal Dur\tAvg Dur")
	var prfs perfs
	pt.Sections.Range(func(k, v interface{}) bool {
		prfs.keys = append(prfs.keys, k.(section))
		prfs.vals = append(prfs.vals, v.(*PerfSectionNormal))
		return true
	})
	sort.Sort(&prfs)
	for i := range prfs.keys {
		ps := prfs.vals[i]
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", prfs.keys[i], ps.count, ps.totalDuration, ps.totalDuration/time.Duration(ps.count))
	}
	tw.Flush()
}

type perfs struct {
	keys []section
	vals []*PerfSectionNormal
}

func (p *perfs) Len() int {
	return len(p.keys)
}

func (p *perfs) Less(i, j int) bool {
	return p.keys[i] < p.keys[j]
}

func (p *perfs) Swap(i, j int) {
	p.keys[i], p.keys[j] = p.keys[j], p.keys[i]
	p.vals[i], p.vals[j] = p.vals[j], p.vals[i]
}

type PerfSection interface {
	Name() string
	TotalDuration() time.Duration
	Count() int64
}

type PerfSectionNormal struct {
	s             section
	totalDuration time.Duration
	count         int64
}

func (ps *PerfSectionNormal) update(dur time.Duration) {
	atomic.AddInt64((*int64)(&ps.totalDuration), int64(dur))
	atomic.AddInt64(&ps.count, 1)
}

func (ps *PerfSectionNormal) Name() string {
	return ps.s.String()
}

func (ps *PerfSectionNormal) Count() int64 {
	return atomic.LoadInt64(&ps.count)
}
func (ps *PerfSectionNormal) TotalDuration() time.Duration {
	return time.Duration(atomic.LoadInt64((*int64)(&ps.totalDuration)))
}

// PerfRun keep time for each particular run
type PerfRun interface {
	Stop()
}
type PerfRunNormal struct {
	ps        *PerfSectionNormal
	startTime time.Time
}

func (pr *PerfRunNormal) Stop() {
	pr.ps.update(time.Since(pr.startTime))
}

type contextKey int

const contextKeyPerfTimer contextKey = iota

var defaultPerfTimer = &NoopTimer{
	section: &NoopSection{},
}

type NoopTimer struct {
	section *NoopSection
}

type NoopSection struct {
}

func (ps *NoopSection) Name() string {
	return "NONAME"
}

func (ps *NoopSection) Stop() {}

func (ps *NoopSection) Count() int64 {
	return 0
}
func (ps *NoopSection) TotalDuration() time.Duration {
	return 0
}

func (t *NoopTimer) Start(s section) PerfRun {
	return t.section
}
func (t *NoopTimer) Print() string { return "" }

func (t *NoopTimer) Fprint(w io.Writer) {}

func GetTimer(ctx context.Context) PerfTimer {
	perfTimer, ok := ctx.Value(contextKeyPerfTimer).(PerfTimer)
	if ok {
		return perfTimer
	}
	return defaultPerfTimer
}

func WithTimer(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKeyPerfTimer, NewPerfTimer())
}
