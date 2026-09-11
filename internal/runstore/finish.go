package runstore

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/andreybaranovskiy/simulation/internal/analytics"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
)

// RunSummary is the runner's own result file, read back here so the engine's
// statistics can join the ones derived from the trace. Some numbers only the
// engine could compute, such as each entity's queue wait.
type RunSummary struct {
	Seed          uint64  `json:"seed"`
	Replication   int     `json:"replication"`
	EngineVersion string  `json:"engineVersion"`
	EndTime       float64 `json:"endTime"`
	WallSeconds   float64 `json:"wallSeconds"`

	Created       int  `json:"created"`
	Completed     int  `json:"completed"`
	CutShort      int  `json:"cutShort"`
	Balked        int  `json:"balked"`
	Reneged       int  `json:"reneged"`
	StillInSystem int  `json:"stillInSystem"`
	LimitHit      bool `json:"limitHit"`

	Resources []struct {
		ID          string  `json:"ID"`
		Label       string  `json:"Label"`
		Capacity    int     `json:"Capacity"`
		Seized      int     `json:"Seized"`
		Balked      int     `json:"Balked"`
		Reneged     int     `json:"Reneged"`
		Utilisation float64 `json:"Utilisation"`
		AvgQueue    float64 `json:"AvgQueue"`
		PeakQueue   int     `json:"PeakQueue"`
		DowntimeSec float64 `json:"DowntimeSec"`
	} `json:"resources"`

	SystemTime analytics.Distribution            `json:"systemTime"`
	Waits      map[string]analytics.Distribution `json:"waits"`
}

// HeatmapFile is the serialized form of every built grid.
type HeatmapFile struct {
	Cols          int     `json:"cols"`
	Rows          int     `json:"rows"`
	CellMeters    float64 `json:"cellMeters"`
	MinX          float64 `json:"minX"`
	MinY          float64 `json:"minY"`
	Buckets       int     `json:"buckets"`
	BucketSeconds float64 `json:"bucketSeconds"`
	StartTime     float64 `json:"startTime"`

	Layers []HeatmapLayer `json:"layers"`
}

// HeatmapLayer is one metric's grid.
type HeatmapLayer struct {
	Metric      string  `json:"metric"`
	Label       string  `json:"label"`
	Unit        string  `json:"unit"`
	Description string  `json:"description"`
	Max         float64 `json:"max"`
	// Scale is the value the colour ramp should saturate at. It is the 95th
	// percentile rather than the maximum, because a heatmap is almost always
	// dominated by a few extreme cells and scaling to those washes out
	// everything a reader came to see.
	Scale float64 `json:"scale"`
	Total float64 `json:"total"`

	// Totals is the whole-run grid, row-major.
	Totals []float32 `json:"totals"`
	// Buckets are the per-time-slice grids, so the UI can scrub. They are
	// omitted when the metric only ever had one slice.
	Buckets [][]float32 `json:"buckets,omitempty"`
}

// finish writes every aggregate and the manifest.
func (b *builder) finish() (*Manifest, error) {
	summary := b.loadSummary()

	if b.opts.Progress != nil {
		b.opts.Progress(0.9, "writing aggregates")
	}

	kpis := b.buildKPIs(summary)
	if err := writeJSON(b.dir.Agg(KPIFile), kpis); err != nil {
		return nil, err
	}

	seriesList := b.series.Series()
	if err := writeJSON(b.dir.Agg(SeriesFile), seriesList); err != nil {
		return nil, err
	}

	ganttRows := b.gantt.Rows(b.startTime, b.endTime)
	if err := writeJSON(b.dir.Agg(GanttFile), ganttRows); err != nil {
		return nil, err
	}

	pathResult := b.paths.Result()
	if err := writeJSON(b.dir.Agg(PathsFile), pathResult); err != nil {
		return nil, err
	}

	heatmapInfos, err := b.writeHeatmaps()
	if err != nil {
		return nil, err
	}

	manifest := b.buildManifest(summary, heatmapInfos)

	if b.opts.Progress != nil {
		b.opts.Progress(1, "done")
	}
	return manifest, b.dir.WriteManifest(manifest)
}

func (b *builder) loadSummary() *RunSummary {
	data, err := os.ReadFile(b.dir.Result())
	if err != nil {
		return nil
	}

	var s RunSummary
	if err := json.Unmarshal(data, &s); err != nil {
		b.opts.Log.Warn("could not read the run summary, deriving what is possible from the trace",
			"error", err)
		return nil
	}
	return &s
}

func (b *builder) writeHeatmaps() ([]HeatmapInfo, error) {
	file := HeatmapFile{
		Cols:          b.gridSpec.Cols,
		Rows:          b.gridSpec.Rows,
		CellMeters:    b.gridSpec.CellMeters,
		MinX:          b.gridSpec.MinX,
		MinY:          b.gridSpec.MinY,
		Buckets:       b.gridSpec.Buckets,
		BucketSeconds: b.gridSpec.BucketSeconds,
		StartTime:     b.gridSpec.StartTime,
	}

	var infos []HeatmapInfo

	for _, info := range analytics.Metrics() {
		h := b.heatmaps[info.Metric]
		if h == nil || h.Empty() {
			// A layer with no data is not offered, so the UI never presents an
			// empty grid as if it were a result.
			continue
		}

		layer := HeatmapLayer{
			Metric:      string(info.Metric),
			Label:       info.Label,
			Unit:        info.Unit,
			Description: info.Description,
			Max:         h.Max(),
			Scale:       h.Percentile(0.95),
			Total:       h.Total(),
			Totals:      h.Totals(),
		}
		if layer.Scale <= 0 {
			layer.Scale = layer.Max
		}

		if b.gridSpec.Buckets > 1 {
			layer.Buckets = make([][]float32, b.gridSpec.Buckets)
			for i := 0; i < b.gridSpec.Buckets; i++ {
				layer.Buckets[i] = h.Bucket(i)
			}
		}

		file.Layers = append(file.Layers, layer)

		infos = append(infos, HeatmapInfo{
			Metric:        string(info.Metric),
			Label:         info.Label,
			Unit:          info.Unit,
			Description:   info.Description,
			Cols:          b.gridSpec.Cols,
			Rows:          b.gridSpec.Rows,
			CellMeters:    b.gridSpec.CellMeters,
			Buckets:       b.gridSpec.Buckets,
			BucketSeconds: b.gridSpec.BucketSeconds,
			Max:           h.Max(),
			Total:         h.Total(),
		})
	}

	if len(file.Layers) == 0 {
		return nil, nil
	}
	return infos, writeJSON(b.dir.Agg(HeatmapsFile), file)
}

// buildKPIs assembles the headline numbers. Keys are generic and stable, which
// is what lets scenario comparison join two runs without knowing the domain.
func (b *builder) buildKPIs(s *RunSummary) *analytics.KPISet {
	set := &analytics.KPISet{}

	measuredHours := math.Max((b.endTime-b.header.WarmUp)/3600, 1e-9)

	created, completed := b.created, b.completed
	if s != nil {
		created, completed = s.Created, s.Completed
	}

	set.Add(analytics.KPI{
		Key: "created", Label: "Entities created", Value: float64(created),
		Unit: analytics.UnitCount, Group: "Throughput", Decimals: 0,
		Description: "Everything the sources generated over the run.",
	})
	set.Add(analytics.KPI{
		Key: "completed", Label: "Entities completed", Value: float64(completed),
		Unit: analytics.UnitCount, Group: "Throughput", Better: analytics.Higher, Decimals: 0,
		Headline:    true,
		Description: "Entities that finished their route.",
	})
	set.Add(analytics.KPI{
		Key: "throughput", Label: "Throughput", Value: float64(completed) / measuredHours,
		Unit: analytics.UnitPerHour, Group: "Throughput", Better: analytics.Higher, Decimals: 1,
		Headline:    true,
		Description: "Completions per hour over the measured period.",
	})

	if s != nil {
		set.Add(analytics.KPI{
			Key: "system_time.mean", Label: "Mean time in system", Value: s.SystemTime.Mean,
			Unit: analytics.UnitSeconds, Group: "Time", Better: analytics.Lower, Decimals: 0,
			Headline:    true,
			Description: "Average time from arrival to departure.",
		})
		set.Add(analytics.KPI{
			Key: "system_time.p50", Label: "Median time in system", Value: s.SystemTime.P50,
			Unit: analytics.UnitSeconds, Group: "Time", Better: analytics.Lower, Decimals: 0,
		})
		set.Add(analytics.KPI{
			Key: "system_time.p95", Label: "95th percentile time in system", Value: s.SystemTime.P95,
			Unit: analytics.UnitSeconds, Group: "Time", Better: analytics.Lower, Decimals: 0,
			Headline: true,
			Description: "Nineteen entities in twenty finished faster than this. " +
				"The tail is what people notice, not the average.",
		})
		set.Add(analytics.KPI{
			Key: "system_time.max", Label: "Worst time in system", Value: s.SystemTime.Max,
			Unit: analytics.UnitSeconds, Group: "Time", Better: analytics.Lower, Decimals: 0,
		})

		if s.Balked > 0 || s.Reneged > 0 {
			set.Add(analytics.KPI{
				Key: "turned_away", Label: "Turned away", Value: float64(s.Balked),
				Unit: analytics.UnitCount, Group: "Service", Better: analytics.Lower, Decimals: 0,
				Description: "Arrived to find a full queue and left without being served.",
			})
			set.Add(analytics.KPI{
				Key: "gave_up", Label: "Gave up waiting", Value: float64(s.Reneged),
				Unit: analytics.UnitCount, Group: "Service", Better: analytics.Lower, Decimals: 0,
				Description: "Waited past the limit and left the queue.",
			})

			if created > 0 {
				lost := float64(s.Balked+s.Reneged) / float64(created) * 100
				set.Add(analytics.KPI{
					Key: "service_loss", Label: "Demand not served", Value: lost,
					Unit: analytics.UnitPercent, Group: "Service", Better: analytics.Lower, Decimals: 1,
					Headline:    true,
					Description: "Share of arrivals that left without being served.",
				})
			}
		}

		if s.CutShort > 0 {
			set.Add(analytics.KPI{
				Key: "cut_short", Label: "Unfinished at the horizon", Value: float64(s.CutShort),
				Unit: analytics.UnitCount, Group: "Service", Better: analytics.Lower, Decimals: 0,
				Description: "Still waiting when the run ended. A large number means the horizon is too short, or the system cannot keep up.",
			})
		}

		if s.LimitHit {
			set.Note("The run hit its entity limit and stopped creating arrivals early. " +
				"Every number here is a lower bound. Shorten the horizon or reduce the arrival rate.")
		}
		if s.StillInSystem > 0 {
			set.Note(fmt.Sprintf("%d entities were still in the system when the run ended.", s.StillInSystem))
		}
	}

	set.Add(analytics.KPI{
		Key: "wip.mean", Label: "Average in system", Value: b.meanWIP(),
		Unit: analytics.UnitEntities, Group: "Load", Better: analytics.Lower, Decimals: 1,
		Description: "How many entities were in the model at once, on average.",
	})
	set.Add(analytics.KPI{
		Key: "wip.peak", Label: "Peak in system", Value: b.series.Peak("wip"),
		Unit: analytics.UnitEntities, Group: "Load", Better: analytics.Lower, Decimals: 0,
	})

	b.addResourceKPIs(set, s)

	set.ApplyDomainLabels(b.header.Domain)
	return set
}

func (b *builder) addResourceKPIs(set *analytics.KPISet, s *RunSummary) {
	if s == nil {
		return
	}

	worstUtil, worstUtilLabel := 0.0, ""
	worstWait, worstWaitLabel := 0.0, ""

	for _, r := range s.Resources {
		set.Add(analytics.KPI{
			Key: "util." + r.ID, Label: r.Label + " utilisation", Value: r.Utilisation * 100,
			Unit: analytics.UnitPercent, Group: "Utilisation", Decimals: 1,
			ResourceID: r.ID,
			Description: "Share of its capacity in use over the measured period. " +
				"Above about 85% a queue grows quickly for any further demand.",
		})
		set.Add(analytics.KPI{
			Key: "queue.avg." + r.ID, Label: r.Label + " average queue", Value: r.AvgQueue,
			Unit: analytics.UnitEntities, Group: "Queueing", Better: analytics.Lower, Decimals: 2,
			ResourceID: r.ID,
		})
		set.Add(analytics.KPI{
			Key: "queue.peak." + r.ID, Label: r.Label + " peak queue", Value: float64(r.PeakQueue),
			Unit: analytics.UnitEntities, Group: "Queueing", Better: analytics.Lower, Decimals: 0,
			ResourceID: r.ID,
		})

		if wait, ok := s.Waits[r.ID]; ok && wait.Count > 0 {
			set.Add(analytics.KPI{
				Key: "wait.mean." + r.ID, Label: r.Label + " mean wait", Value: wait.Mean,
				Unit: analytics.UnitSeconds, Group: "Queueing", Better: analytics.Lower, Decimals: 0,
				ResourceID: r.ID,
			})
			set.Add(analytics.KPI{
				Key: "wait.p95." + r.ID, Label: r.Label + " 95th percentile wait", Value: wait.P95,
				Unit: analytics.UnitSeconds, Group: "Queueing", Better: analytics.Lower, Decimals: 0,
				ResourceID: r.ID,
			})

			if wait.Mean > worstWait {
				worstWait, worstWaitLabel = wait.Mean, r.Label
			}
		}

		if r.DowntimeSec > 0 {
			set.Add(analytics.KPI{
				Key: "downtime." + r.ID, Label: r.Label + " downtime", Value: r.DowntimeSec,
				Unit: analytics.UnitSeconds, Group: "Availability", Better: analytics.Lower, Decimals: 0,
				ResourceID: r.ID,
			})
		}

		if r.Utilisation > worstUtil {
			worstUtil, worstUtilLabel = r.Utilisation, r.Label
		}
	}

	// The bottleneck is the single most useful thing a run can tell someone,
	// so it is a KPI rather than something a reader has to find by scanning a
	// utilisation table.
	if worstUtilLabel != "" {
		set.Add(analytics.KPI{
			Key: "bottleneck.utilisation", Label: "Busiest resource", Value: worstUtil * 100,
			Unit: analytics.UnitPercent, Group: "Utilisation", Decimals: 1,
			Headline:    true,
			Description: worstUtilLabel + " was the busiest resource. It is the first place to add capacity.",
		})
	}
	if worstWaitLabel != "" {
		set.Add(analytics.KPI{
			Key: "bottleneck.wait", Label: "Longest mean wait", Value: worstWait,
			Unit: analytics.UnitSeconds, Group: "Queueing", Better: analytics.Lower, Decimals: 0,
			Description: "Entities waited longest for " + worstWaitLabel + ".",
		})
	}
}

// meanWIP is the time-weighted average number of entities in the model,
// recovered from the series rather than tracked twice.
func (b *builder) meanWIP() float64 {
	for _, s := range b.series.Series() {
		if s.Key != "wip" || len(s.Values) == 0 {
			continue
		}
		sum := 0.0
		for _, v := range s.Values {
			sum += v
		}
		return sum / float64(len(s.Values))
	}
	return 0
}

func (b *builder) buildManifest(s *RunSummary, heatmaps []HeatmapInfo) *Manifest {
	m := &Manifest{
		RunID:         b.header.RunID,
		ModelName:     b.header.ModelName,
		Domain:        b.header.Domain,
		Seed:          b.header.Seed,
		Replication:   b.header.Replication,
		EngineVersion: b.header.EngineVersion,
		StartTime:     b.startTime,
		EndTime:       b.endTime,
		WarmUp:        b.header.WarmUp,
		Bounds: Bounds{
			MinX: b.header.Bounds.MinX, MinY: b.header.Bounds.MinY, MinZ: b.header.Bounds.MinZ,
			MaxX: b.header.Bounds.MaxX, MaxY: b.header.Bounds.MaxY, MaxZ: b.header.Bounds.MaxZ,
		},
		Heatmaps: heatmaps,
		Available: Available{
			Series: true, Gantt: true, Paths: true,
			Heatmaps: len(heatmaps) > 0, KPIs: true,
		},
		Counts: Counts{
			Entities: b.created,
			Records:  b.records,
			Spans:    b.spans,
		},
	}

	for i, c := range b.header.Classes {
		count := 0
		if i < len(b.classCounts) {
			count = b.classCounts[i]
		}
		m.Classes = append(m.Classes, ClassInfo{
			ID: c.ID, Label: c.Label, Color: c.Color, Shape: c.Shape,
			Length: c.Length, Width: c.Width, Height: c.Height, Model: c.Model,
			Count: count,
		})
	}
	for _, n := range b.header.Nodes {
		m.Nodes = append(m.Nodes, NodeInfo{ID: n.ID, Label: n.Label, X: n.X, Y: n.Y, Z: n.Z})
	}
	for _, r := range b.header.Resources {
		m.Resources = append(m.Resources, ResourceInfo{
			ID: r.ID, Label: r.Label, Capacity: r.Capacity, X: r.X, Y: r.Y,
		})
	}
	for _, z := range b.header.Zones {
		m.Zones = append(m.Zones, ZoneInfo{
			ID: z.ID, Label: z.Label, X: z.X, Y: z.Y,
			Width: z.Width, Height: z.Height, Color: z.Color,
		})
	}

	for _, lw := range b.levels {
		level := lw.manifest()
		m.Levels = append(m.Levels, level)
		m.Counts.Chunks += level.ChunkCount
	}

	if s != nil {
		if s.LimitHit {
			m.Warnings = append(m.Warnings,
				"The run hit its entity limit and stopped creating arrivals early, so these results are a lower bound.")
		}
		if s.CutShort > 0 {
			m.Warnings = append(m.Warnings, fmt.Sprintf(
				"%d entities were still waiting when the horizon arrived and are excluded from throughput.", s.CutShort))
		}
	}

	if footer, err := trace.ReadFooter(b.dir.Trace()); err == nil {
		if footer.Truncated {
			m.Warnings = append(m.Warnings,
				"The event trace was truncated at its record limit, so playback stops before the end of the run.")
		}
		if !footer.Complete && footer.Error != "" {
			m.Warnings = append(m.Warnings, "The run did not finish: "+footer.Error)
		}
	}

	m.Counts.TotalBytes = dirSize(string(b.dir))
	return m
}
