package analytics

import "math"

// Series is a time series sampled into fixed buckets.
//
// Buckets rather than raw points, because a run can produce millions of
// changes and no chart can draw them. Bucketing during the pass keeps memory
// bounded and gives the chart exactly the resolution it can render.
type Series struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Unit  string `json:"unit,omitempty"`
	// Kind tells the chart how to draw it: a rate is a bar, a level is a line.
	Kind string `json:"kind"`

	StartTime     float64   `json:"startTime"`
	BucketSeconds float64   `json:"bucketSeconds"`
	Values        []float64 `json:"values"`
}

// Bucketer collects a run's time series.
type Bucketer struct {
	startTime     float64
	bucketSeconds float64
	buckets       int

	counters map[string]*counter
	levels   map[string]*level
	order    []string
}

// counter accumulates events per bucket, giving a rate such as throughput.
type counter struct {
	label  string
	unit   string
	values []float64
}

// level tracks a quantity that holds a value over time, such as a queue length
// or the number of entities in the system.
//
// It is integrated rather than sampled: each bucket records the time-weighted
// average, so a queue that spikes briefly between two samples is not lost.
type level struct {
	label string
	unit  string

	current  float64
	lastTime float64
	// area is the integral of the value over the current bucket.
	area   float64
	bucket int
	values []float64
	peaks  []float64
	peak   float64
}

func NewBucketer(startTime, endTime float64, targetBuckets int) *Bucketer {
	duration := math.Max(endTime-startTime, 1)
	if targetBuckets < 1 {
		targetBuckets = 1
	}

	return &Bucketer{
		startTime:     startTime,
		bucketSeconds: duration / float64(targetBuckets),
		buckets:       targetBuckets,
		counters:      make(map[string]*counter),
		levels:        make(map[string]*level),
	}
}

func (b *Bucketer) BucketSeconds() float64 { return b.bucketSeconds }

func (b *Bucketer) indexOf(t float64) int {
	if b.bucketSeconds <= 0 {
		return 0
	}
	i := int((t - b.startTime) / b.bucketSeconds)
	if i < 0 {
		return 0
	}
	if i >= b.buckets {
		return b.buckets - 1
	}
	return i
}

// Count records one occurrence, such as an entity completing.
func (b *Bucketer) Count(key, label, unit string, t float64) {
	c, ok := b.counters[key]
	if !ok {
		c = &counter{label: label, unit: unit, values: make([]float64, b.buckets)}
		b.counters[key] = c
		b.order = append(b.order, "count:"+key)
	}
	c.values[b.indexOf(t)]++
}

// SetLevel records a quantity changing value at a moment.
func (b *Bucketer) SetLevel(key, label, unit string, t, value float64) {
	l, ok := b.levels[key]
	if !ok {
		l = &level{
			label: label, unit: unit,
			lastTime: b.startTime,
			values:   make([]float64, b.buckets),
			peaks:    make([]float64, b.buckets),
		}
		b.levels[key] = l
		b.order = append(b.order, "level:"+key)
	}

	b.integrate(l, t)
	l.current = value
	if value > l.peak {
		l.peak = value
	}
}

// integrate carries the level's area forward to t, closing off any buckets it
// crosses. This is what makes the average time-weighted rather than a sample
// of whatever the value happened to be at a bucket boundary.
func (b *Bucketer) integrate(l *level, t float64) {
	if t <= l.lastTime {
		return
	}

	for {
		bucketEnd := b.startTime + float64(l.bucket+1)*b.bucketSeconds

		if t <= bucketEnd || l.bucket >= b.buckets-1 {
			l.area += l.current * (t - l.lastTime)
			if l.current > l.peaks[l.bucket] {
				l.peaks[l.bucket] = l.current
			}
			l.lastTime = t
			return
		}

		l.area += l.current * (bucketEnd - l.lastTime)
		if l.current > l.peaks[l.bucket] {
			l.peaks[l.bucket] = l.current
		}
		l.values[l.bucket] = l.area / b.bucketSeconds

		l.area = 0
		l.lastTime = bucketEnd
		l.bucket++
	}
}

// Close finishes every open bucket at the run's end time.
func (b *Bucketer) Close(endTime float64) {
	for _, l := range b.levels {
		b.integrate(l, endTime)
		if l.bucket < b.buckets {
			// The final bucket may be partial; dividing by its real width
			// keeps the average honest rather than diluting it.
			width := endTime - (b.startTime + float64(l.bucket)*b.bucketSeconds)
			if width > 0 {
				l.values[l.bucket] = l.area / width
			}
		}
	}
}

// Series returns every collected series, counters first.
func (b *Bucketer) Series() []Series {
	out := make([]Series, 0, len(b.counters)+len(b.levels)*2)

	for _, tagged := range b.order {
		kind, key := tagged[:len(tagged)-len(trimPrefixKey(tagged))-1], trimPrefixKey(tagged)

		switch kind {
		case "count":
			c := b.counters[key]
			out = append(out, Series{
				Key: key, Label: c.label, Unit: c.unit, Kind: "rate",
				StartTime: b.startTime, BucketSeconds: b.bucketSeconds,
				Values: c.values,
			})
		case "level":
			l := b.levels[key]
			out = append(out, Series{
				Key: key, Label: l.label, Unit: l.unit, Kind: "level",
				StartTime: b.startTime, BucketSeconds: b.bucketSeconds,
				Values: l.values,
			})
			// The peak matters as much as the average: a queue averaging two
			// that hit thirty once is a different system from one that sat at
			// two all day.
			out = append(out, Series{
				Key: key + ".peak", Label: l.label + " (peak)", Unit: l.unit, Kind: "level",
				StartTime: b.startTime, BucketSeconds: b.bucketSeconds,
				Values: l.peaks,
			})
		}
	}
	return out
}

// Peak reports the highest value a level reached over the run.
func (b *Bucketer) Peak(key string) float64 {
	if l, ok := b.levels[key]; ok {
		return l.peak
	}
	return 0
}

// trimPrefixKey strips the "count:" or "level:" tag from an ordering entry.
func trimPrefixKey(tagged string) string {
	for i := 0; i < len(tagged); i++ {
		if tagged[i] == ':' {
			return tagged[i+1:]
		}
	}
	return tagged
}
