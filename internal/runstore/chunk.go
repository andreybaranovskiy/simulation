package runstore

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// A chunk is one window of simulated time, self-contained so the viewer can
// fetch any moment without reading what came before.
//
// Self-contained is the whole point. The trace is a delta stream: to know
// where an entity is at hour six you must replay the first six hours. A chunk
// instead opens with every live entity's current motion, so seeking is one
// fetch rather than a replay.
//
// Motion stays as spans rather than sampled positions. The viewer interpolates
// between endpoints, which is exact, and which keeps a chunk's size tied to
// how often entities change direction rather than to a frame rate.
var chunkMagic = [8]byte{'S', 'I', 'M', 'C', 'H', 'U', 'N', 'K'}

const chunkVersion uint32 = 1

// ChunkHeader is the JSON preamble of a chunk file.
type ChunkHeader struct {
	Index     int     `json:"index"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`

	LiveCount  int `json:"liveCount"`
	SpanCount  int `json:"spanCount"`
	StateCount int `json:"stateCount"`
	SpawnCount int `json:"spawnCount"`
	ExitCount  int `json:"exitCount"`
}

// Live is an entity already in the model when the window opens, together with
// the motion it is part-way through. Carrying the whole span rather than just
// a position is what lets the viewer place the entity correctly at any moment
// in the window, including while it is crossing a distance longer than the
// window itself.
type Live struct {
	ID    uint32
	Class uint16
	State uint8

	SpanStart  float64
	SX, SY, SZ float64

	SpanEnd    float64
	EX, EY, EZ float64
}

// PositionAt interpolates an entity's position. A span with no duration puts
// the entity at its endpoint rather than dividing by zero.
func (l Live) PositionAt(t float64) (x, y, z float64) {
	span := l.SpanEnd - l.SpanStart
	if span <= 0 || t >= l.SpanEnd {
		return l.EX, l.EY, l.EZ
	}
	if t <= l.SpanStart {
		return l.SX, l.SY, l.SZ
	}

	f := (t - l.SpanStart) / span
	return l.SX + (l.EX-l.SX)*f,
		l.SY + (l.EY-l.SY)*f,
		l.SZ + (l.EZ-l.SZ)*f
}

// Span is one leg of motion ending inside the window.
type Span struct {
	ID      uint32
	EndTime float64
	X, Y, Z float64
}

// StateChange marks an entity switching between travelling, queued, serving
// and the rest. The 2D view colours by it and the Gantt chart is built from it.
type StateChange struct {
	ID    uint32
	Time  float64
	State uint8
}

// Spawn is an entity appearing during the window.
type Spawn struct {
	ID      uint32
	Class   uint16
	Time    float64
	X, Y, Z float64
}

// Exit is an entity leaving during the window.
type Exit struct {
	ID   uint32
	Time float64
}

// Chunk is a decoded window.
type Chunk struct {
	Header ChunkHeader
	Live   []Live
	Spans  []Span
	States []StateChange
	Spawns []Spawn
	Exits  []Exit
}

// Record sizes, fixed so a reader can size its buffers up front.
const (
	liveSize  = 4 + 2 + 1 + 8 + 12 + 8 + 12 // 47
	spanSize  = 4 + 8 + 12                  // 24
	stateSize = 4 + 8 + 1                   // 13
	spawnSize = 4 + 2 + 8 + 12              // 26
	exitSize  = 4 + 8                       // 12
)

// WriteChunk serializes a window to disk and reports its size.
func WriteChunk(path string, c *Chunk) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, fmt.Errorf("create chunk directory: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create chunk: %w", err)
	}
	defer file.Close()

	c.Header.LiveCount = len(c.Live)
	c.Header.SpanCount = len(c.Spans)
	c.Header.StateCount = len(c.States)
	c.Header.SpawnCount = len(c.Spawns)
	c.Header.ExitCount = len(c.Exits)

	headerJSON, err := json.Marshal(c.Header)
	if err != nil {
		return 0, fmt.Errorf("encode chunk header: %w", err)
	}

	// The preamble stays uncompressed so a reader can learn what a chunk
	// contains, and decide to skip it, without inflating the body.
	prefix := make([]byte, 0, 16+len(headerJSON))
	prefix = append(prefix, chunkMagic[:]...)
	prefix = binary.LittleEndian.AppendUint32(prefix, chunkVersion)
	prefix = binary.LittleEndian.AppendUint32(prefix, uint32(len(headerJSON)))
	prefix = append(prefix, headerJSON...)

	if _, err := file.Write(prefix); err != nil {
		return 0, fmt.Errorf("write chunk header: %w", err)
	}

	buf := bufio.NewWriterSize(file, 1<<18)
	gz, err := gzip.NewWriterLevel(buf, gzip.BestSpeed)
	if err != nil {
		return 0, err
	}

	body := make([]byte, 0, 64)

	for _, l := range c.Live {
		body = body[:0]
		body = binary.LittleEndian.AppendUint32(body, l.ID)
		body = binary.LittleEndian.AppendUint16(body, l.Class)
		body = append(body, l.State)
		body = appendF64(body, l.SpanStart)
		body = appendF32(body, l.SX, l.SY, l.SZ)
		body = appendF64(body, l.SpanEnd)
		body = appendF32(body, l.EX, l.EY, l.EZ)
		if _, err := gz.Write(body); err != nil {
			return 0, err
		}
	}

	for _, s := range c.Spans {
		body = body[:0]
		body = binary.LittleEndian.AppendUint32(body, s.ID)
		body = appendF64(body, s.EndTime)
		body = appendF32(body, s.X, s.Y, s.Z)
		if _, err := gz.Write(body); err != nil {
			return 0, err
		}
	}

	for _, s := range c.States {
		body = body[:0]
		body = binary.LittleEndian.AppendUint32(body, s.ID)
		body = appendF64(body, s.Time)
		body = append(body, s.State)
		if _, err := gz.Write(body); err != nil {
			return 0, err
		}
	}

	for _, s := range c.Spawns {
		body = body[:0]
		body = binary.LittleEndian.AppendUint32(body, s.ID)
		body = binary.LittleEndian.AppendUint16(body, s.Class)
		body = appendF64(body, s.Time)
		body = appendF32(body, s.X, s.Y, s.Z)
		if _, err := gz.Write(body); err != nil {
			return 0, err
		}
	}

	for _, x := range c.Exits {
		body = body[:0]
		body = binary.LittleEndian.AppendUint32(body, x.ID)
		body = appendF64(body, x.Time)
		if _, err := gz.Write(body); err != nil {
			return 0, err
		}
	}

	if err := gz.Close(); err != nil {
		return 0, fmt.Errorf("finish chunk compression: %w", err)
	}
	if err := buf.Flush(); err != nil {
		return 0, err
	}

	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// ReadChunk loads a window. The server uses it to answer viewer requests and
// the tests use it to check what the build pass produced.
func ReadChunk(path string) (*Chunk, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var prefix [16]byte
	if _, err := io.ReadFull(file, prefix[:]); err != nil {
		return nil, fmt.Errorf("read chunk header: %w", err)
	}
	if [8]byte(prefix[0:8]) != chunkMagic {
		return nil, fmt.Errorf("not a chunk file: the magic bytes do not match")
	}
	if v := binary.LittleEndian.Uint32(prefix[8:12]); v != chunkVersion {
		return nil, fmt.Errorf("this build reads chunk format %d, the file is format %d", chunkVersion, v)
	}

	headerLen := binary.LittleEndian.Uint32(prefix[12:16])
	if headerLen > 1<<20 {
		return nil, fmt.Errorf("the chunk header claims %d bytes, which is not plausible", headerLen)
	}

	headerJSON := make([]byte, headerLen)
	if _, err := io.ReadFull(file, headerJSON); err != nil {
		return nil, fmt.Errorf("read chunk header: %w", err)
	}

	c := &Chunk{}
	if err := json.Unmarshal(headerJSON, &c.Header); err != nil {
		return nil, fmt.Errorf("parse chunk header: %w", err)
	}

	gz, err := gzip.NewReader(bufio.NewReaderSize(file, 1<<18))
	if err != nil {
		return nil, fmt.Errorf("read chunk body: %w", err)
	}
	defer gz.Close()

	r := bufio.NewReaderSize(gz, 1<<18)
	buf := make([]byte, liveSize)

	c.Live = make([]Live, 0, c.Header.LiveCount)
	for i := 0; i < c.Header.LiveCount; i++ {
		if _, err := io.ReadFull(r, buf[:liveSize]); err != nil {
			return nil, chunkReadErr("live entity", i, err)
		}
		c.Live = append(c.Live, Live{
			ID:        binary.LittleEndian.Uint32(buf[0:4]),
			Class:     binary.LittleEndian.Uint16(buf[4:6]),
			State:     buf[6],
			SpanStart: f64(buf[7:15]),
			SX:        f32(buf[15:19]), SY: f32(buf[19:23]), SZ: f32(buf[23:27]),
			SpanEnd: f64(buf[27:35]),
			EX:      f32(buf[35:39]), EY: f32(buf[39:43]), EZ: f32(buf[43:47]),
		})
	}

	c.Spans = make([]Span, 0, c.Header.SpanCount)
	for i := 0; i < c.Header.SpanCount; i++ {
		if _, err := io.ReadFull(r, buf[:spanSize]); err != nil {
			return nil, chunkReadErr("span", i, err)
		}
		c.Spans = append(c.Spans, Span{
			ID:      binary.LittleEndian.Uint32(buf[0:4]),
			EndTime: f64(buf[4:12]),
			X:       f32(buf[12:16]), Y: f32(buf[16:20]), Z: f32(buf[20:24]),
		})
	}

	c.States = make([]StateChange, 0, c.Header.StateCount)
	for i := 0; i < c.Header.StateCount; i++ {
		if _, err := io.ReadFull(r, buf[:stateSize]); err != nil {
			return nil, chunkReadErr("state change", i, err)
		}
		c.States = append(c.States, StateChange{
			ID:    binary.LittleEndian.Uint32(buf[0:4]),
			Time:  f64(buf[4:12]),
			State: buf[12],
		})
	}

	c.Spawns = make([]Spawn, 0, c.Header.SpawnCount)
	for i := 0; i < c.Header.SpawnCount; i++ {
		if _, err := io.ReadFull(r, buf[:spawnSize]); err != nil {
			return nil, chunkReadErr("spawn", i, err)
		}
		c.Spawns = append(c.Spawns, Spawn{
			ID:    binary.LittleEndian.Uint32(buf[0:4]),
			Class: binary.LittleEndian.Uint16(buf[4:6]),
			Time:  f64(buf[6:14]),
			X:     f32(buf[14:18]), Y: f32(buf[18:22]), Z: f32(buf[22:26]),
		})
	}

	c.Exits = make([]Exit, 0, c.Header.ExitCount)
	for i := 0; i < c.Header.ExitCount; i++ {
		if _, err := io.ReadFull(r, buf[:exitSize]); err != nil {
			return nil, chunkReadErr("exit", i, err)
		}
		c.Exits = append(c.Exits, Exit{
			ID:   binary.LittleEndian.Uint32(buf[0:4]),
			Time: f64(buf[4:12]),
		})
	}

	return c, nil
}

func chunkReadErr(what string, index int, err error) error {
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return fmt.Errorf("the chunk ends part-way through %s %d", what, index)
	}
	return fmt.Errorf("read %s %d: %w", what, index, err)
}

func appendF32(b []byte, values ...float64) []byte {
	for _, v := range values {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(float32(v)))
	}
	return b
}

func appendF64(b []byte, v float64) []byte {
	return binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
}

func f32(b []byte) float64 {
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
}

func f64(b []byte) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(b))
}
