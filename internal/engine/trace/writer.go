package trace

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// writeBufferSize is large enough that the gzip layer gets long runs to work
// with, which matters far more than record-level cleverness for a stream that
// is mostly repeated structure.
const writeBufferSize = 1 << 20

// Writer serializes a run's events.
//
// It is deliberately not safe for concurrent use. Godes runs one simulation
// per process with cooperative scheduling, so exactly one goroutine is
// executing model logic at a time, and a mutex here would buy nothing.
type Writer struct {
	file *os.File
	buf  *bufio.Writer
	gz   *gzip.Writer

	// scratch is reused for every record so writing does not allocate.
	scratch [40]byte

	classIDs   map[string]uint16
	resIDs     map[string]uint16
	metricIDs  map[string]uint16
	header     Header
	headerPath string

	records  uint64
	entities uint64
	exits    uint64
	lastTime float64

	// maxRecords stops a runaway model from filling the disk. Zero disables it.
	maxRecords uint64
	truncated  bool

	err error
}

// Options configures a Writer.
type Options struct {
	// MaxRecords caps the stream. A model with a bug can emit events without
	// bound, and an unbounded write would take the server's disk with it.
	MaxRecords uint64
	// CompressionLevel is a compress/gzip level. The default trades a little
	// ratio for speed, because the trace is written once and read once.
	CompressionLevel int
}

// Create opens a trace file and writes its header. The header must be complete
// before any record, since it defines the id tables the records use.
func Create(path string, header Header, opts Options) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create trace directory: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create trace file: %w", err)
	}

	header.Schema = "sim.trace/v1"
	header.Version = FormatVersion

	w := &Writer{
		file:       file,
		headerPath: path,
		header:     header,
		classIDs:   make(map[string]uint16, len(header.Classes)),
		resIDs:     make(map[string]uint16, len(header.Resources)),
		metricIDs:  make(map[string]uint16, len(header.Metrics)),
		maxRecords: opts.MaxRecords,
	}

	for i, c := range header.Classes {
		w.classIDs[c.ID] = uint16(i)
	}
	for i, r := range header.Resources {
		w.resIDs[r.ID] = uint16(i)
	}
	for i, m := range header.Metrics {
		w.metricIDs[m] = uint16(i)
	}

	if err := w.writePreamble(); err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}

	w.buf = bufio.NewWriterSize(file, writeBufferSize)

	level := opts.CompressionLevel
	if level == 0 {
		level = gzip.BestSpeed
	}
	gz, err := gzip.NewWriterLevel(w.buf, level)
	if err != nil {
		file.Close()
		os.Remove(path)
		return nil, fmt.Errorf("start trace compression: %w", err)
	}
	w.gz = gz

	return w, nil
}

// writePreamble emits the magic, version and JSON header uncompressed, so a
// reader can inspect what a trace contains without decompressing any of it.
func (w *Writer) writePreamble() error {
	headerJSON, err := json.Marshal(w.header)
	if err != nil {
		return fmt.Errorf("encode trace header: %w", err)
	}

	prefix := make([]byte, 0, 16+len(headerJSON))
	prefix = append(prefix, Magic[:]...)
	prefix = binary.LittleEndian.AppendUint32(prefix, FormatVersion)
	prefix = binary.LittleEndian.AppendUint32(prefix, uint32(len(headerJSON)))
	prefix = append(prefix, headerJSON...)

	if _, err := w.file.Write(prefix); err != nil {
		return fmt.Errorf("write trace header: %w", err)
	}
	return nil
}

// ClassIndex and ResourceIndex map a spec id to its numeric index. The
// interpreter resolves these once and then writes numbers.
func (w *Writer) ClassIndex(id string) uint16 { return w.classIDs[id] }

func (w *Writer) ResourceIndex(id string) uint16 { return w.resIDs[id] }

func (w *Writer) MetricIndex(key string) uint16 { return w.metricIDs[key] }

// Spawn records an entity entering the model at a position.
func (w *Writer) Spawn(t float64, id uint32, class uint16, x, y, z float64) {
	b := w.scratch[:0]
	b = append(b, byte(RecSpawn))
	b = appendFloat64(b, t)
	b = binary.LittleEndian.AppendUint32(b, id)
	b = binary.LittleEndian.AppendUint16(b, class)
	b = appendFloat32(b, x)
	b = appendFloat32(b, y)
	b = appendFloat32(b, z)

	w.write(b)
	w.entities++
	w.advance(t)
}

// Segment records that an entity moves in a straight line, ending at the given
// position at the given time. The start is wherever the entity already was,
// which is why this record is only 25 bytes.
//
// A segment whose endpoint equals the current position is how waiting in place
// is expressed, so queueing costs the same as moving.
func (w *Writer) Segment(endTime float64, id uint32, x, y, z float64) {
	b := w.scratch[:0]
	b = append(b, byte(RecSegment))
	b = binary.LittleEndian.AppendUint32(b, id)
	b = appendFloat64(b, endTime)
	b = appendFloat32(b, x)
	b = appendFloat32(b, y)
	b = appendFloat32(b, z)

	w.write(b)
	w.advance(endTime)
}

// State records what an entity is doing from t onward.
func (w *Writer) State(t float64, id uint32, state State) {
	b := w.scratch[:0]
	b = append(b, byte(RecState))
	b = appendFloat64(b, t)
	b = binary.LittleEndian.AppendUint32(b, id)
	b = append(b, byte(state))

	w.write(b)
	w.advance(t)
}

// Resource records a resource's occupancy and queue length changing. These
// samples are what the utilisation series and the Gantt chart are built from.
func (w *Writer) Resource(t float64, resource uint16, busy, queued uint16) {
	b := w.scratch[:0]
	b = append(b, byte(RecResource))
	b = appendFloat64(b, t)
	b = binary.LittleEndian.AppendUint16(b, resource)
	b = binary.LittleEndian.AppendUint16(b, busy)
	b = binary.LittleEndian.AppendUint16(b, queued)

	w.write(b)
	w.advance(t)
}

// Exit records an entity leaving the model.
func (w *Writer) Exit(t float64, id uint32) {
	b := w.scratch[:0]
	b = append(b, byte(RecExit))
	b = appendFloat64(b, t)
	b = binary.LittleEndian.AppendUint32(b, id)

	w.write(b)
	w.exits++
	w.advance(t)
}

// Metric records a scalar time-series sample.
func (w *Writer) Metric(t float64, key uint16, value float64) {
	b := w.scratch[:0]
	b = append(b, byte(RecMetric))
	b = appendFloat64(b, t)
	b = binary.LittleEndian.AppendUint16(b, key)
	b = appendFloat64(b, value)

	w.write(b)
	w.advance(t)
}

// Event records a discrete occurrence involving an entity and a resource.
func (w *Writer) Event(t float64, id uint32, kind EventKind, resource uint16) {
	b := w.scratch[:0]
	b = append(b, byte(RecEvent))
	b = appendFloat64(b, t)
	b = binary.LittleEndian.AppendUint32(b, id)
	b = append(b, byte(kind))
	b = binary.LittleEndian.AppendUint16(b, resource)

	w.write(b)
	w.advance(t)
}

func (w *Writer) write(b []byte) {
	if w.err != nil || w.truncated {
		return
	}
	if w.maxRecords > 0 && w.records >= w.maxRecords {
		w.truncated = true
		return
	}
	if _, err := w.gz.Write(b); err != nil {
		w.err = fmt.Errorf("write trace record: %w", err)
		return
	}
	w.records++
}

func (w *Writer) advance(t float64) {
	if t > w.lastTime {
		w.lastTime = t
	}
}

// Err reports the first write failure, if any.
func (w *Writer) Err() error { return w.err }

// Truncated reports whether the record cap was hit.
func (w *Writer) Truncated() bool { return w.truncated }

// Stats reports progress, which the runner streams to the server so the UI can
// show a live count instead of an indeterminate spinner.
func (w *Writer) Stats() (records, entities, exits uint64, simTime float64) {
	return w.records, w.entities, w.exits, w.lastTime
}

// Close flushes the stream and writes the footer beside the trace.
func (w *Writer) Close(footer Footer) error {
	footer.RecordCount = w.records
	footer.EntityCount = w.entities
	footer.ExitCount = w.exits
	footer.Truncated = w.truncated
	if w.truncated && footer.TruncateNote == "" {
		footer.TruncateNote = fmt.Sprintf(
			"the run produced more than %d events and was cut short; "+
				"shorten the horizon or slow the arrival rate", w.maxRecords)
	}
	if footer.EndTime == 0 {
		footer.EndTime = w.lastTime
	}

	// Close in order and keep the first error: a later failure is usually a
	// consequence of the earlier one.
	firstErr := w.err

	if err := w.gz.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("finish trace compression: %w", err)
	}
	if err := w.buf.Flush(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("flush trace: %w", err)
	}
	if err := w.file.Sync(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("sync trace: %w", err)
	}
	if err := w.file.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("close trace: %w", err)
	}

	if err := writeFooter(FooterPath(w.headerPath), footer); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// FooterPath is where the footer for a given trace lives.
func FooterPath(tracePath string) string {
	return tracePath + ".footer.json"
}

func writeFooter(path string, footer Footer) error {
	data, err := json.MarshalIndent(footer, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trace footer: %w", err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return fmt.Errorf("write trace footer: %w", err)
	}
	return nil
}

// ReadFooter loads a trace's footer. A missing footer means the run did not
// finish, which the caller reports rather than treating as corruption.
func ReadFooter(tracePath string) (Footer, error) {
	data, err := os.ReadFile(FooterPath(tracePath))
	if err != nil {
		return Footer{}, err
	}
	var f Footer
	if err := json.Unmarshal(data, &f); err != nil {
		return Footer{}, fmt.Errorf("read trace footer: %w", err)
	}
	return f, nil
}

// appendFloat32 narrows a coordinate to 32 bits. Single precision resolves
// roughly a millimetre over a ten-kilometre site, far finer than any model
// input, and halves the size of every motion record.
func appendFloat32(b []byte, v float64) []byte {
	return binary.LittleEndian.AppendUint32(b, math.Float32bits(float32(v)))
}

func appendFloat64(b []byte, v float64) []byte {
	return binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
}
