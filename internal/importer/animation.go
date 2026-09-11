// Package importer converts externally produced animation files into the
// platform's run artifacts.
//
// It exists so the format the original viewer used keeps working. Someone with
// a 28 MB export from another tool should be able to upload it, play it back,
// and get heatmaps and a report out of it, without the simulation engine being
// involved at all.
//
// The conversion writes the same event trace the engine writes, then runs the
// same build pass. Everything downstream is therefore identical: an imported
// animation and a simulated run are the same kind of artifact.
package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
)

// Animation is the legacy format: a list of objects and a flat list of
// timestamped transitions against them.
type Animation struct {
	Animation struct {
		Name      string  `json:"name"`
		TimeScale float64 `json:"time_scale"`
	} `json:"animation"`

	Objects     []AnimationObject     `json:"objects"`
	Transitions []AnimationTransition `json:"transition"`
}

// AnimationObject is one thing in the scene and its starting pose.
type AnimationObject struct {
	ID    json.RawMessage `json:"id"`
	Type  string          `json:"type"`
	X     float64         `json:"x"`
	Y     float64         `json:"y"`
	Z     float64         `json:"z"`
	Color string          `json:"color"`

	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Depth  float64 `json:"depth"`

	RotationX float64 `json:"rotation_x"`
	RotationY float64 `json:"rotation_y"`
	RotationZ float64 `json:"rotation_z"`
}

// AnimationTransition is one keyframe.
type AnimationTransition struct {
	Time  float64         `json:"time"`
	ObjID json.RawMessage `json:"objId"`
	ID    json.RawMessage `json:"id"`
	Type  string          `json:"type"`
	X     float64         `json:"x"`
	Y     float64         `json:"y"`
	Z     float64         `json:"z"`
}

// The legacy format is written in the three.js convention, where Y is up and Z
// runs across the ground. This platform puts north on Y and height on Z,
// because a site plan is a map and a map's second axis is a direction, not an
// elevation.
//
// Mapping between them is the difference between a terminal 10 km long and 400
// m wide, and one that is 10 km long, 400 m TALL and infinitely thin. Without
// this the lane offsets in a file become altitudes and every entity ends up on
// a single line.
func toWorld(x, y, z float64) (wx, wy, wz float64) {
	return x, z, y
}

// Result reports what an import produced.
type Result struct {
	Name        string   `json:"name"`
	Objects     int      `json:"objects"`
	Transitions int      `json:"transitions"`
	Moves       int      `json:"moves"`
	Ignored     int      `json:"ignored"`
	Duration    float64  `json:"duration"`
	Classes     []string `json:"classes"`
	Warnings    []string `json:"warnings,omitempty"`
}

// maxAnimationBytes bounds an uploaded animation. The original demo file is
// 28 MB; a limit well above that still stops a mistake from exhausting memory,
// since the whole document has to be parsed before it can be sorted by time.
const maxAnimationBytes = 1 << 30

// Import reads an animation and writes a complete run directory.
func Import(r io.Reader, dir runstore.Dir, runID string, opts runstore.BuildOptions) (*Result, error) {
	anim, err := parse(r)
	if err != nil {
		return nil, err
	}
	if len(anim.Objects) == 0 {
		return nil, fmt.Errorf("the file has no objects")
	}

	if err := dir.Ensure(); err != nil {
		return nil, err
	}

	result, err := writeTrace(anim, dir, runID)
	if err != nil {
		return nil, err
	}

	// A minimal result file so the build pass reports the same counts as a
	// simulated run would.
	if err := writeResultFile(dir, result); err != nil {
		return nil, err
	}

	if _, err := runstore.Build(dir, opts); err != nil {
		return nil, fmt.Errorf("build the viewer artifacts: %w", err)
	}
	return result, nil
}

func parse(r io.Reader) (*Animation, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAnimationBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the animation: %w", err)
	}
	if len(data) > maxAnimationBytes {
		return nil, fmt.Errorf("the animation is larger than the %d MB limit", maxAnimationBytes>>20)
	}

	var anim Animation
	if err := json.Unmarshal(data, &anim); err != nil {
		return nil, fmt.Errorf("the animation is not valid JSON: %w", err)
	}
	return &anim, nil
}

// writeTrace converts the animation into the engine's own event format.
func writeTrace(anim *Animation, dir runstore.Dir, runID string) (*Result, error) {
	result := &Result{
		Name:        strings.TrimSpace(anim.Animation.Name),
		Objects:     len(anim.Objects),
		Transitions: len(anim.Transitions),
	}
	if result.Name == "" {
		result.Name = "Imported animation"
	}

	// Object types become entity classes, which is what gives the viewer a
	// legend and lets a reader filter by kind.
	classes, classOf := buildClasses(anim.Objects)
	for _, c := range classes {
		result.Classes = append(result.Classes, c.ID)
	}

	ids := make([]string, len(anim.Objects))
	indexOf := make(map[string]uint32, len(anim.Objects))
	for i, o := range anim.Objects {
		ids[i] = identifier(o.ID)
		// Entity ids start at one; zero is reserved in the trace for events
		// that belong to no entity.
		indexOf[ids[i]] = uint32(i + 1)
	}

	moves := collectMoves(anim, indexOf, result)
	bounds := computeBounds(anim.Objects, moves)

	header := trace.Header{
		RunID:         runID,
		ModelName:     result.Name,
		Domain:        "generic",
		EngineVersion: "import/1",
		Bounds:        bounds,
		Classes:       classes,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}

	// Transitions are not necessarily ordered, and the whole pipeline assumes
	// a trace is. Sorting once here is what lets everything downstream stream.
	sort.SliceStable(moves, func(i, j int) bool { return moves[i].time < moves[j].time })

	if len(moves) > 0 {
		result.Duration = moves[len(moves)-1].time
	}
	header.Horizon = result.Duration

	w, err := trace.Create(dir.Trace(), header, trace.Options{})
	if err != nil {
		return nil, err
	}

	// Every object exists from the start, which is what the format means: it
	// describes a fixed cast moving around, not arrivals and departures.
	for i, o := range anim.Objects {
		id := uint32(i + 1)
		x, y, z := toWorld(o.X, o.Y, o.Z)
		w.Spawn(0, id, classOf[ids[i]], x, y, z)
		w.State(0, id, trace.StateIdle)
	}

	for _, m := range moves {
		w.Segment(m.time, m.entity, m.x, m.y, m.z)
	}

	// Closing every object at the end keeps the entity accounting balanced,
	// which the build pass and the KPI set both assume.
	for i := range anim.Objects {
		w.Exit(result.Duration, uint32(i+1))
	}

	footer := trace.Footer{Complete: true, EndTime: result.Duration}
	if err := w.Close(footer); err != nil {
		return nil, err
	}

	if result.Moves == 0 {
		result.Warnings = append(result.Warnings,
			"The file contains no move transitions, so nothing will appear to move.")
	}
	if result.Ignored > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d transitions were ignored because they are not movements. "+
				"Rotation is not carried through the import.", result.Ignored))
	}

	return result, nil
}

type move struct {
	time    float64
	entity  uint32
	x, y, z float64
}

// collectMoves turns transitions into motion, carrying forward each object's
// last known position for any axis a transition leaves out.
func collectMoves(anim *Animation, indexOf map[string]uint32, result *Result) []move {
	// Last-known positions are kept in the SOURCE axes, because a transition
	// that omits an axis is omitting one of the source's, not one of ours.
	last := make(map[uint32][3]float64, len(anim.Objects))
	for i, o := range anim.Objects {
		last[uint32(i+1)] = [3]float64{o.X, o.Y, o.Z}
	}

	moves := make([]move, 0, len(anim.Transitions))

	for i := range anim.Transitions {
		t := &anim.Transitions[i]

		// Rotation and anything else is skipped rather than guessed at. The
		// viewer derives heading from the direction of travel, which is more
		// reliable than a rotation channel that may disagree with the path.
		if !strings.EqualFold(t.Type, "move") {
			result.Ignored++
			continue
		}

		raw := t.ObjID
		if len(raw) == 0 {
			raw = t.ID
		}
		entity, ok := indexOf[identifier(raw)]
		if !ok {
			result.Ignored++
			continue
		}

		previous := last[entity]
		x, y, z := toWorld(
			pick(t.X, previous[0]),
			pick(t.Y, previous[1]),
			pick(t.Z, previous[2]),
		)

		m := move{time: t.Time, entity: entity, x: x, y: y, z: z}
		last[entity] = [3]float64{
			pick(t.X, previous[0]),
			pick(t.Y, previous[1]),
			pick(t.Z, previous[2]),
		}

		moves = append(moves, m)
		result.Moves++
	}

	return moves
}

// pick keeps a non-finite coordinate from corrupting a position. A missing
// value decodes as zero, which is indistinguishable from a real zero, so only
// genuinely unusable values fall back.
func pick(value, fallback float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return value
}

// buildClasses maps object types onto entity classes, giving each a colour.
func buildClasses(objects []AnimationObject) ([]trace.ClassInfo, map[string]uint16) {
	type classKey struct{ typ, color string }

	order := []classKey{}
	seen := map[classKey]uint16{}
	classOf := map[string]uint16{}

	for _, o := range objects {
		key := classKey{typ: strings.ToLower(strings.TrimSpace(o.Type)), color: o.Color}
		if key.typ == "" {
			key.typ = "object"
		}

		if _, ok := seen[key]; !ok {
			seen[key] = uint16(len(order))
			order = append(order, key)
		}
	}

	// A type that appears in several colours becomes several classes, and two
	// legend entries reading "Container truck" would be useless. The colour
	// goes in the label only where it is what tells them apart.
	typeCounts := map[string]int{}
	for _, key := range order {
		typeCounts[key.typ]++
	}

	classes := make([]trace.ClassInfo, 0, len(order))
	for i, key := range order {
		label := strings.ReplaceAll(key.typ, "_", " ")
		if label != "" {
			label = strings.ToUpper(label[:1]) + label[1:]
		}
		if typeCounts[key.typ] > 1 {
			if colorName := strings.TrimSpace(key.color); colorName != "" {
				label = fmt.Sprintf("%s (%s)", label, colorName)
			} else {
				label = fmt.Sprintf("%s %d", label, i+1)
			}
		}

		classes = append(classes, trace.ClassInfo{
			ID:    fmt.Sprintf("%s_%d", key.typ, i),
			Label: label,
			Color: resolveColor(key.color, i),
			Shape: "box",
			// The legacy format's dimensions were in a viewer-specific unit
			// with no stated scale. A plausible default is more honest than
			// carrying a number whose meaning is unknown.
			Length: 4, Width: 2, Height: 2,
		})
	}

	for _, o := range objects {
		key := classKey{typ: strings.ToLower(strings.TrimSpace(o.Type)), color: o.Color}
		if key.typ == "" {
			key.typ = "object"
		}
		classOf[identifier(o.ID)] = seen[key]
	}

	return classes, classOf
}

// resolveColor turns the format's colour names into hex the viewer can use.
func resolveColor(name string, index int) string {
	name = strings.ToLower(strings.TrimSpace(name))

	if strings.HasPrefix(name, "#") {
		return name
	}

	named := map[string]string{
		"red": "#e0566a", "green": "#3fc98a", "blue": "#4c8dff",
		"yellow": "#f2c14e", "orange": "#ff8a4c", "purple": "#a97cff",
		"black": "#3a4055", "white": "#e8eefc", "grey": "#8a93ad",
		"gray": "#8a93ad", "cyan": "#4fd1e0", "pink": "#d97bb5",
	}
	if hex, ok := named[name]; ok {
		return hex
	}

	fallback := []string{"#4c8dff", "#ff8a4c", "#3fc98a", "#e0566a", "#a97cff", "#f2c14e"}
	return fallback[index%len(fallback)]
}

// computeBounds finds the extent of everything, so the heatmap grid and the 2D
// view frame the right area.
func computeBounds(objects []AnimationObject, moves []move) trace.Bounds {
	b := trace.Bounds{
		MinX: math.Inf(1), MinY: math.Inf(1), MinZ: math.Inf(1),
		MaxX: math.Inf(-1), MaxY: math.Inf(-1), MaxZ: math.Inf(-1),
	}

	grow := func(x, y, z float64) {
		b.MinX, b.MaxX = math.Min(b.MinX, x), math.Max(b.MaxX, x)
		b.MinY, b.MaxY = math.Min(b.MinY, y), math.Max(b.MaxY, y)
		b.MinZ, b.MaxZ = math.Min(b.MinZ, z), math.Max(b.MaxZ, z)
	}

	for _, o := range objects {
		grow(toWorld(o.X, o.Y, o.Z))
	}
	for _, m := range moves {
		grow(m.x, m.y, m.z)
	}

	if math.IsInf(b.MinX, 1) {
		return trace.Bounds{MaxX: 100, MaxY: 100}
	}

	// A margin proportional to the scene keeps entities at the edge visible
	// and gives the heatmap grid somewhere to put them.
	margin := math.Max(math.Max(b.MaxX-b.MinX, b.MaxY-b.MinY)*0.02, 5)
	return b.Pad(margin)
}

// identifier normalizes an id that may be a number or a string in the source.
func identifier(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}

	var asNumber float64
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return strconv.FormatFloat(asNumber, 'f', -1, 64)
	}

	return strings.Trim(string(raw), `"`)
}

// writeResultFile gives the build pass the counts it expects from a run.
func writeResultFile(dir runstore.Dir, result *Result) error {
	summary := map[string]any{
		"engineVersion": "import/1",
		"endTime":       result.Duration,
		"created":       result.Objects,
		"completed":     result.Objects,
		"cutShort":      0,
		"systemTime": map[string]any{
			"count": result.Objects,
			"mean":  result.Duration,
			"p50":   result.Duration,
			"p95":   result.Duration,
			"max":   result.Duration,
		},
		"resources": []any{},
		"waits":     map[string]any{},
	}

	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return writeFile(dir.Result(), data)
}
