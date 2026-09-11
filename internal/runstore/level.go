package runstore

import (
	"fmt"
	"math"
)

// levelWriter builds one playback fidelity, emitting chunks as the pass
// crosses window boundaries.
//
// The stride is what separates the levels. Level 0 keeps every entity; a level
// with stride 4 keeps one in four. The choice is by entity id rather than at
// random, so an entity stays in or out of a level for its whole life and the
// overview does not flicker as chunks change.
type levelWriter struct {
	dir    Dir
	index  int
	stride int

	chunkSeconds float64
	startTime    float64
	endTime      float64

	// current is the chunk being filled.
	current    *Chunk
	chunkIndex int

	chunkCount int
	bytes      int64
	peakLive   int
	liveNow    int
}

func newLevelWriter(dir Dir, index, stride int, chunkSeconds, startTime, endTime float64) *levelWriter {
	lw := &levelWriter{
		dir:          dir,
		index:        index,
		stride:       stride,
		chunkSeconds: chunkSeconds,
		startTime:    startTime,
		endTime:      endTime,
	}
	lw.openChunk(0, nil)
	return lw
}

// keeps reports whether an entity belongs to this level.
func (lw *levelWriter) keeps(id uint32) bool {
	return lw.stride <= 1 || int(id)%lw.stride == 0
}

func (lw *levelWriter) windowStart(index int) float64 {
	return lw.startTime + float64(index)*lw.chunkSeconds
}

func (lw *levelWriter) windowEnd(index int) float64 {
	return lw.startTime + float64(index+1)*lw.chunkSeconds
}

// openChunk starts a window, seeding it with every entity already in the model
// so the window stands alone.
func (lw *levelWriter) openChunk(index int, live map[uint32]*entityState) {
	lw.chunkIndex = index
	lw.current = &Chunk{
		Header: ChunkHeader{
			Index:     index,
			StartTime: lw.windowStart(index),
			EndTime:   lw.windowEnd(index),
		},
	}

	lw.liveNow = 0
	for id, e := range live {
		if !lw.keeps(id) {
			continue
		}
		lw.current.Live = append(lw.current.Live, Live{
			ID: id, Class: e.class, State: e.state,
			SpanStart: e.spanStart, SX: e.sx, SY: e.sy, SZ: e.sz,
			SpanEnd: e.spanEnd, EX: e.ex, EY: e.ey, EZ: e.ez,
		})
		lw.liveNow++
	}

	if lw.liveNow > lw.peakLive {
		lw.peakLive = lw.liveNow
	}
}

// advanceTo closes and writes out every window that ends before t.
func (lw *levelWriter) advanceTo(t float64, live map[uint32]*entityState) error {
	for t >= lw.windowEnd(lw.chunkIndex) {
		if err := lw.flush(); err != nil {
			return err
		}

		next := lw.chunkIndex + 1
		// A long gap with no events should not write a run of empty chunks one
		// at a time; jump straight to the window containing t.
		if target := lw.chunkFor(t); target > next {
			for i := next; i < target; i++ {
				lw.openChunk(i, live)
				if err := lw.flush(); err != nil {
					return err
				}
			}
			next = target
		}
		lw.openChunk(next, live)
	}
	return nil
}

func (lw *levelWriter) chunkFor(t float64) int {
	if lw.chunkSeconds <= 0 {
		return 0
	}
	i := int(math.Floor((t - lw.startTime) / lw.chunkSeconds))
	if i < 0 {
		return 0
	}
	return i
}

func (lw *levelWriter) flush() error {
	path := lw.dir.Path(fmt.Sprintf("%s/lod%d/%05d.bin", FramesDir, lw.index, lw.chunkIndex))

	size, err := WriteChunk(path, lw.current)
	if err != nil {
		return err
	}

	lw.bytes += size
	lw.chunkCount++
	return nil
}

func (lw *levelWriter) spawn(t float64, id uint32, class uint16, x, y, z float64, live map[uint32]*entityState) error {
	if !lw.keeps(id) {
		return nil
	}
	if err := lw.advanceTo(t, live); err != nil {
		return err
	}

	lw.current.Spawns = append(lw.current.Spawns, Spawn{
		ID: id, Class: class, Time: t, X: x, Y: y, Z: z,
	})

	lw.liveNow++
	if lw.liveNow > lw.peakLive {
		lw.peakLive = lw.liveNow
	}
	return nil
}

// span files a motion leg. startTime places it in a window; endTime is the
// data the viewer interpolates towards.
//
// The two differ, and the distinction is the whole correctness of the format.
// A span that starts in one window and ends three windows later belongs to the
// window it starts in; the windows it crosses pick it up through their live
// entity seed, which already carries the motion in progress.
func (lw *levelWriter) span(startTime, endTime float64, id uint32, x, y, z float64, live map[uint32]*entityState) error {
	if !lw.keeps(id) {
		return nil
	}
	if err := lw.advanceTo(startTime, live); err != nil {
		return err
	}

	lw.current.Spans = append(lw.current.Spans, Span{ID: id, EndTime: endTime, X: x, Y: y, Z: z})
	return nil
}

func (lw *levelWriter) state(t float64, id uint32, state uint8, live map[uint32]*entityState) error {
	if !lw.keeps(id) {
		return nil
	}
	if err := lw.advanceTo(t, live); err != nil {
		return err
	}

	lw.current.States = append(lw.current.States, StateChange{ID: id, Time: t, State: state})
	return nil
}

func (lw *levelWriter) exit(t float64, id uint32, live map[uint32]*entityState) error {
	if !lw.keeps(id) {
		return nil
	}
	if err := lw.advanceTo(t, live); err != nil {
		return err
	}

	lw.current.Exits = append(lw.current.Exits, Exit{ID: id, Time: t})
	lw.liveNow--
	return nil
}

// close writes the final chunk and pads out to the run's end, so the viewer
// can scrub to the last second without hitting a missing file.
func (lw *levelWriter) close(live map[uint32]*entityState) error {
	if err := lw.flush(); err != nil {
		return err
	}

	final := lw.chunkFor(lw.endTime)
	for i := lw.chunkIndex + 1; i <= final; i++ {
		lw.openChunk(i, live)
		if err := lw.flush(); err != nil {
			return err
		}
	}
	return nil
}

func (lw *levelWriter) manifest() Level {
	return Level{
		Index:        lw.index,
		Stride:       lw.stride,
		ChunkSeconds: lw.chunkSeconds,
		ChunkCount:   lw.chunkCount,
		Bytes:        lw.bytes,
		PeakLive:     lw.peakLive,
	}
}
