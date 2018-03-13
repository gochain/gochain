package perfutils

import (
	"io"
	"os"
	"strconv"
	"time"
)

func NewPerfTimer() *PerfTimer {
	return &PerfTimer{
		Sections: map[string]*PerfSection{},
	}
}

// PerfTimer is a way to time pieces of code, in particular ones that happen many times,
// then get the metrics for it.
type PerfTimer struct {
	Sections map[string]*PerfSection
}

func (pt *PerfTimer) Start(sectionName string) *PerfSection {
	ps := pt.Sections[sectionName]
	if ps == nil {
		ps = &PerfSection{
			Name: sectionName,
		}
		pt.Sections[sectionName] = ps
	}
	ps.startTime = time.Now()
	return ps
}
func (pt *PerfTimer) Print() {
	pt.Fprint(os.Stdout)
}
func (pt *PerfTimer) Fprint(w io.Writer) {
	// fmt.Fprint(w, pt.Sections)
	// totalDuration := time.Duration(0) // doesn't make sense unless we have subsections or something
	for k, v := range pt.Sections {
		w.Write([]byte(k))
		w.Write([]byte(": "))
		w.Write([]byte(strconv.FormatInt(v.Count, 10)))
		w.Write([]byte(" times, "))
		w.Write([]byte(v.TotalDuration.String()))
		w.Write([]byte("\n"))
		// totalDuration += v.TotalDuration
	}
	// w.Write([]byte("TOTAL Duration: "))
	// w.Write([]byte(totalDuration.String()))
	// w.Write([]byte("\n"))
}

type PerfSection struct {
	Name          string
	TotalDuration time.Duration
	Count         int64
	startTime     time.Time
}

func (ps *PerfSection) Stop() {
	ps.TotalDuration += time.Since(ps.startTime)
	ps.Count += 1
}
