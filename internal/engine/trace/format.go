// Package trace defines the binary event stream a simulation run produces.
//
// The format exists to make tens of millions of events affordable. Two
// decisions do most of that work.
//
// First, motion is recorded as segments, not samples. An entity crossing 200
// metres writes one record saying where it will be and when, rather than a
// position every tick. Cost becomes proportional to the number of decisions in
// the model, not to duration times entity count.
//
// Second, records are deltas against the reader's running state. A segment
// carries only its endpoint, because the start is wherever that entity already
// was. A motion record is 25 bytes.
//
// The trace is an intermediate artifact. The viewer never reads it: the
// post-processing pass turns it into sampled level-of-detail frames and
// aggregates, which are what get streamed to a browser.
package trace

import "fmt"

// Magic identifies a trace file.
var Magic = [8]byte{'S', 'I', 'M', 'T', 'R', 'A', 'C', 'E'}

// FormatVersion is bumped when the record layout changes. A reader refuses
// anything it does not know, rather than misinterpreting bytes.
const FormatVersion uint32 = 1

// RecordType tags each record. Values are fixed forever once shipped.
type RecordType uint8

const (
	RecSpawn    RecordType = 0x01
	RecSegment  RecordType = 0x02
	RecState    RecordType = 0x03
	RecResource RecordType = 0x04
	RecExit     RecordType = 0x05
	RecMetric   RecordType = 0x06
	RecEvent    RecordType = 0x07
)

func (r RecordType) String() string {
	switch r {
	case RecSpawn:
		return "spawn"
	case RecSegment:
		return "segment"
	case RecState:
		return "state"
	case RecResource:
		return "resource"
	case RecExit:
		return "exit"
	case RecMetric:
		return "metric"
	case RecEvent:
		return "event"
	}
	return fmt.Sprintf("unknown(0x%02x)", uint8(r))
}

// State is what an entity is doing. The Gantt chart and the congestion heatmap
// are both built from state transitions.
type State uint8

const (
	StateIdle State = iota
	StateTravelling
	StateQueued
	StateServing
	StateBlocked
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateTravelling:
		return "travelling"
	case StateQueued:
		return "queued"
	case StateServing:
		return "serving"
	case StateBlocked:
		return "blocked"
	}
	return "unknown"
}

// StateNames lists the states in order, for legends and report keys.
func StateNames() []string {
	return []string{"idle", "travelling", "queued", "serving", "blocked"}
}

// EventKind marks a discrete occurrence. These drive the KPIs: a wait time is
// the gap between a queue entry and the seize that follows it.
type EventKind uint8

const (
	EvQueueEnter EventKind = iota
	EvQueueExit
	EvSeize
	EvRelease
	// EvBalk is an entity turned away because a queue was full.
	EvBalk
	// EvRenege is an entity that waited too long and gave up.
	EvRenege
	EvResourceDown
	EvResourceUp
	EvShiftStart
	EvShiftEnd
)

func (e EventKind) String() string {
	switch e {
	case EvQueueEnter:
		return "queue_enter"
	case EvQueueExit:
		return "queue_exit"
	case EvSeize:
		return "seize"
	case EvRelease:
		return "release"
	case EvBalk:
		return "balk"
	case EvRenege:
		return "renege"
	case EvResourceDown:
		return "resource_down"
	case EvResourceUp:
		return "resource_up"
	case EvShiftStart:
		return "shift_start"
	case EvShiftEnd:
		return "shift_end"
	}
	return "unknown"
}

// Header is the JSON preamble. It holds everything needed to interpret the
// numeric ids in the record stream, so the stream itself carries no strings.
type Header struct {
	Schema  string `json:"schema"`
	Version uint32 `json:"version"`

	RunID      string `json:"runId"`
	ScenarioID string `json:"scenarioId,omitempty"`
	ModelName  string `json:"modelName"`
	Domain     string `json:"domain,omitempty"`

	Seed          uint64 `json:"seed"`
	Replication   int    `json:"replication"`
	EngineVersion string `json:"engineVersion"`

	Horizon float64 `json:"horizon"`
	WarmUp  float64 `json:"warmUp,omitempty"`

	// Bounds is the world extent in metres, used to size the heatmap grid and
	// to quantise positions when building level-of-detail frames.
	Bounds Bounds `json:"bounds"`

	// The tables below give meaning to the numeric ids in the records.
	Classes   []ClassInfo    `json:"classes"`
	Nodes     []NodeInfo     `json:"nodes"`
	Resources []ResourceInfo `json:"resources"`
	Zones     []ZoneInfo     `json:"zones,omitempty"`
	Metrics   []string       `json:"metrics,omitempty"`

	StartedAt string `json:"startedAt"`
}

type Bounds struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MinZ float64 `json:"minZ"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
	MaxZ float64 `json:"maxZ"`
}

// Pad grows the bounds by a margin so entities that stray slightly outside the
// node extent are still representable after quantisation.
func (b Bounds) Pad(margin float64) Bounds {
	b.MinX -= margin
	b.MinY -= margin
	b.MinZ -= margin
	b.MaxX += margin
	b.MaxY += margin
	b.MaxZ += margin
	return b
}

// Width, Height and Depth are the extents. A zero extent is widened to one
// metre so nothing downstream divides by zero.
func (b Bounds) Width() float64  { return nonZero(b.MaxX - b.MinX) }
func (b Bounds) Height() float64 { return nonZero(b.MaxY - b.MinY) }
func (b Bounds) Depth() float64  { return nonZero(b.MaxZ - b.MinZ) }

func nonZero(v float64) float64 {
	if v <= 0 {
		return 1
	}
	return v
}

type ClassInfo struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Color  string  `json:"color"`
	Shape  string  `json:"shape"`
	Length float64 `json:"length"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Model  string  `json:"model,omitempty"`
}

type NodeInfo struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Z     float64 `json:"z"`
}

type ResourceInfo struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Capacity int     `json:"capacity"`
	Node     string  `json:"node,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

type ZoneInfo struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Color  string  `json:"color,omitempty"`
}

// Footer is appended once the run finishes, recording what the stream
// contains. It is stored beside the trace rather than inside it, because a
// crashed run still leaves a readable stream without one.
type Footer struct {
	Complete     bool    `json:"complete"`
	EndTime      float64 `json:"endTime"`
	RecordCount  uint64  `json:"recordCount"`
	EntityCount  uint64  `json:"entityCount"`
	ExitCount    uint64  `json:"exitCount"`
	WallSeconds  float64 `json:"wallSeconds"`
	Error        string  `json:"error,omitempty"`
	Truncated    bool    `json:"truncated,omitempty"`
	TruncateNote string  `json:"truncateNote,omitempty"`
}
