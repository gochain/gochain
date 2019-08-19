package opencensus

import (
	"context"
	"fmt"
	"time"

	"contrib.go.opencensus.io/exporter/stackdriver"
	"go.opencensus.io/metric/metricdata"

	"github.com/gochain-io/gochain/v3/log"
	"github.com/gochain-io/gochain/v3/metrics"
)

// StartExporter launches a goroutine to periodically publish metrics from r to sd.
func StartExporter(d time.Duration, r metrics.Registry, sd *stackdriver.Exporter) *time.Ticker {
	t := time.NewTicker(d)
	go func() {
		for range t.C {
			m := convert(r)
			if err := sd.ExportMetrics(context.Background(), m); err != nil {
				log.Warn("Failed to export stackdriver metrics", "error", err)
			}
		}
	}()
	return t
}

// convert returns all metrics from the registry converted to opencensus format.
func convert(r metrics.Registry) []*metricdata.Metric {
	var m []*metricdata.Metric
	r.Each(func(name string, i interface{}) {
		switch i := i.(type) {
		case metrics.Counter:
			m = append(m, counter(name, i))
		case metrics.Gauge:
			m = append(m, gaugeInt(name, i))
		case metrics.GaugeFloat64:
			m = append(m, gaugeFloat(name, i))
		case metrics.Histogram:
			m = append(m, histogram(name, i)...)
		case metrics.Meter:
			m = append(m, meter(name, i)...)
		case metrics.Timer:
			m = append(m, timer(name, i)...)
		case metrics.ResettingTimer:
			m = append(m, resettingTimer(name, i)...)
		default:
			panic(fmt.Sprintf("unsupported type for '%s': %T", name, i))
		}
	})
	return m
}

func counter(name string, m metrics.Counter) *metricdata.Metric {
	return metric(name, cumulativeInt64, int64Point(time.Now(), m.Count()))
}

func gaugeInt(name string, m metrics.Gauge) *metricdata.Metric {
	return metric(name, metricdata.TypeGaugeInt64, int64Point(time.Now(), m.Value()))
}

func gaugeFloat(name string, m metrics.GaugeFloat64) *metricdata.Metric {
	return metric(name, gaugeFloat64, float64Point(time.Now(), m.Value()))
}

func histogram(name string, m metrics.Histogram) []*metricdata.Metric {
	now := time.Now()
	h := m.Snapshot()
	ps := h.Percentiles([]float64{0.5, 0.75, 0.95, 0.99, 0.999})
	var ms []*metricdata.Metric
	ms = append(ms, metric(name+".count", cumulativeInt64, int64Point(now, h.Count())))
	ms = append(ms, metricUnit(name+".min", cumulativeInt64, unitNS, int64Point(now, h.Min())))
	ms = append(ms, metricUnit(name+".max", cumulativeInt64, unitNS, int64Point(now, h.Max())))
	ms = append(ms, metricUnit(name+".std-dev", cumulativeFloat64, unitNS, float64Point(now, h.StdDev())))
	ms = append(ms, metricUnit(name+".50-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[0])))
	ms = append(ms, metricUnit(name+".75-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[1])))
	ms = append(ms, metricUnit(name+".95-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[2])))
	ms = append(ms, metricUnit(name+".99-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[3])))
	ms = append(ms, metricUnit(name+".999-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[4])))
	return ms
}

func meter(name string, m metrics.Meter) []*metricdata.Metric {
	now := time.Now()
	h := m.Snapshot()
	var ms []*metricdata.Metric
	ms = append(ms, metric(name+".count", cumulativeInt64, int64Point(now, h.Count())))
	ms = append(ms, metricUnit(name+".one-minute", gaugeFloat64, unitPerSecond, float64Point(now, h.Rate1())))
	ms = append(ms, metricUnit(name+".five-minute", gaugeFloat64, unitPerSecond, float64Point(now, h.Rate5())))
	ms = append(ms, metricUnit(name+".fifteen-minute", gaugeFloat64, unitPerSecond, float64Point(now, h.Rate15())))
	ms = append(ms, metricUnit(name+".mean", cumulativeFloat64, unitPerSecond, float64Point(now, h.RateMean())))
	return ms
}

func timer(name string, m metrics.Timer) []*metricdata.Metric {
	now := time.Now()
	t := m.Snapshot()
	ps := t.Percentiles([]float64{0.5, 0.75, 0.95, 0.99, 0.999})
	var ms []*metricdata.Metric
	ms = append(ms, metric(name+".count", cumulativeInt64, int64Point(now, t.Count())))
	ms = append(ms, metricUnit(name+".min", cumulativeInt64, unitNS, int64Point(now, t.Min())))
	ms = append(ms, metricUnit(name+".max", cumulativeInt64, unitNS, int64Point(now, t.Max())))
	ms = append(ms, metricUnit(name+".mean", cumulativeFloat64, unitNS, float64Point(now, t.Mean())))
	ms = append(ms, metricUnit(name+".std-dev", cumulativeFloat64, unitNS, float64Point(now, t.StdDev())))
	ms = append(ms, metricUnit(name+".50-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[0])))
	ms = append(ms, metricUnit(name+".75-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[1])))
	ms = append(ms, metricUnit(name+".95-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[2])))
	ms = append(ms, metricUnit(name+".99-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[3])))
	ms = append(ms, metricUnit(name+".999-percentile", cumulativeFloat64, unitNS, float64Point(now, ps[4])))
	ms = append(ms, metricUnit(name+".one-minute", gaugeFloat64, unitPerSecond, float64Point(now, t.Rate1())))
	ms = append(ms, metricUnit(name+".five-minute", gaugeFloat64, unitPerSecond, float64Point(now, t.Rate5())))
	ms = append(ms, metricUnit(name+".fifteen-minute", gaugeFloat64, unitPerSecond, float64Point(now, t.Rate15())))
	ms = append(ms, metricUnit(name+".mean-rate", cumulativeFloat64, unitPerSecond, float64Point(now, t.RateMean())))
	return ms
}

func resettingTimer(name string, m metrics.ResettingTimer) []*metricdata.Metric {
	now := time.Now()
	t := m.Snapshot()
	ps := t.Percentiles([]float64{50, 75, 95, 99})
	var ms []*metricdata.Metric
	ms = append(ms, metric(name+".count", cumulativeInt64, int64Point(now, int64(len(t.Values())))))
	ms = append(ms, metricUnit(name+".mean", cumulativeFloat64, unitNS, float64Point(now, t.Mean())))
	ms = append(ms, metricUnit(name+".50-percentile", cumulativeInt64, unitNS, int64Point(now, ps[0])))
	ms = append(ms, metricUnit(name+".75-percentile", cumulativeInt64, unitNS, int64Point(now, ps[1])))
	ms = append(ms, metricUnit(name+".95-percentile", cumulativeInt64, unitNS, int64Point(now, ps[2])))
	ms = append(ms, metricUnit(name+".99-percentile", cumulativeInt64, unitNS, int64Point(now, ps[3])))
	return ms
}

func metric(n string, t metricdata.Type, p metricdata.Point) *metricdata.Metric {
	return metricUnit(n, t, metricdata.UnitDimensionless, p)
}

func metricUnit(n string, t metricdata.Type, u metricdata.Unit, p metricdata.Point) *metricdata.Metric {
	return &metricdata.Metric{
		Descriptor: metricdata.Descriptor{Name: n, Type: t, Unit: u},
		TimeSeries: []*metricdata.TimeSeries{{
			Points: []metricdata.Point{p},
		}},
	}
}

var (
	float64Point      = metricdata.NewFloat64Point
	int64Point        = metricdata.NewInt64Point
	cumulativeInt64   = metricdata.TypeCumulativeInt64
	cumulativeFloat64 = metricdata.TypeCumulativeFloat64
	gaugeFloat64      = metricdata.TypeGaugeFloat64

	unitNS        = metricdata.Unit("ns")
	unitPerSecond = metricdata.Unit("/s")
)
