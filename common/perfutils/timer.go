package perfutils

import (
	"context"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

func NewPerfTimer() PerfTimer {
	return &PerfTimerNormal{
		// Sections: map[string]*PerfSectionNormal{},

	}
}

// PerfTimer is a way to time pieces of code, in particular ones that happen many times,
// then get the metrics for it.
type PerfTimer interface {
	Start(sectionName string) PerfSection
	Print()
	Fprint(w io.Writer)
}

// PerfTimerNormal great name huh?
type PerfTimerNormal struct {
	// Sections map[string]*PerfSectionNormal
	Sections sync.Map
}

func (pt *PerfTimerNormal) Start(sectionName string) PerfSection {
	// ps := pt.Sections[sectionName]
	var ps *PerfSectionNormal
	psl, _ := pt.Sections.Load(sectionName)
	if psl == nil {
		ps = &PerfSectionNormal{
			name: sectionName,
		}
		// pt.Sections[sectionName] = ps
		pt.Sections.Store(sectionName, ps)
	} else {
		ps = psl.(*PerfSectionNormal)
	}
	ps.startTime = time.Now()
	return ps
}
func (pt *PerfTimerNormal) Print() {
	pt.Fprint(os.Stdout)
}
func (pt *PerfTimerNormal) Fprint(w io.Writer) {
	// fmt.Fprint(w, pt.Sections)
	// totalDuration := time.Duration(0) // doesn't make sense unless we have subsections or something
	// for k, v := range pt.Sections {
	pt.Sections.Range(func(k, v interface{}) bool {
		w.Write([]byte(k.(string)))
		w.Write([]byte(": "))
		w.Write([]byte(strconv.FormatInt(v.(*PerfSectionNormal).count, 10)))
		w.Write([]byte(" times, "))
		w.Write([]byte(v.(*PerfSectionNormal).totalDuration.String()))
		w.Write([]byte("\n"))
		// totalDuration += v.TotalDuration
		return true
	})
	// w.Write([]byte("TOTAL Duration: "))
	// w.Write([]byte(totalDuration.String()))
	// w.Write([]byte("\n"))
}

type PerfSection interface {
	Name() string
	TotalDuration() time.Duration
	Count() int64
	Stop()
}

type PerfSectionNormal struct {
	name          string
	totalDuration time.Duration
	count         int64
	startTime     time.Time
}

func (ps *PerfSectionNormal) Name() string {
	return ps.name
}

func (ps *PerfSectionNormal) Stop() {
	ps.totalDuration += time.Since(ps.startTime)
	ps.count += 1
}

func (ps *PerfSectionNormal) Count() int64 {
	return ps.count
}
func (ps *PerfSectionNormal) TotalDuration() time.Duration {
	return ps.totalDuration
}

type contextKey string

var (
	contextKeyPerfTimer = contextKey("perf-timer")
	defaultPerfTimer    = &NoopTimer{
		section: &NoopSection{},
	}
)

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

func (t *NoopTimer) Start(sectionName string) PerfSection {
	return t.section
}
func (t *NoopTimer) Print() {}

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
