package trace

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// maxHeaderBytes bounds the JSON preamble. The header describes a model's
// structure, not its data, so anything larger means a corrupt file.
const maxHeaderBytes = 32 << 20

// Record is one decoded event. It is a single struct rather than an interface
// so that iterating a stream of tens of millions of records allocates nothing.
type Record struct {
	Type RecordType
	Time float64

	// Entity is set for spawn, segment, state, exit and event records.
	Entity uint32
	// Class is set for spawn records.
	Class uint16
	// X, Y and Z are set for spawn and segment records.
	X, Y, Z float64

	// EntityState is set for state records.
	EntityState State

	// Resource is set for resource and event records.
	Resource uint16
	Busy     uint16
	Queued   uint16

	// Kind is set for event records.
	Kind EventKind

	// Metric and Value are set for metric records.
	Metric uint16
	Value  float64
}

// Reader decodes a trace stream.
type Reader struct {
	file   *os.File
	gz     *gzip.Reader
	buf    *bufio.Reader
	header Header

	scratch [40]byte
	count   uint64
}

// Open reads a trace's header and positions the reader at the first record.
func Open(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open trace: %w", err)
	}

	r := &Reader{file: file}
	if err := r.readPreamble(); err != nil {
		file.Close()
		return nil, err
	}

	gz, err := gzip.NewReader(bufio.NewReaderSize(file, writeBufferSize))
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("read trace stream: %w", err)
	}
	r.gz = gz
	r.buf = bufio.NewReaderSize(gz, writeBufferSize)

	return r, nil
}

func (r *Reader) readPreamble() error {
	var prefix [16]byte
	if _, err := io.ReadFull(r.file, prefix[:]); err != nil {
		return fmt.Errorf("read trace header: %w", err)
	}

	if [8]byte(prefix[0:8]) != Magic {
		return fmt.Errorf("not a trace file: the magic bytes do not match")
	}

	version := binary.LittleEndian.Uint32(prefix[8:12])
	if version != FormatVersion {
		return fmt.Errorf("this build reads trace format %d, the file is format %d",
			FormatVersion, version)
	}

	headerLen := binary.LittleEndian.Uint32(prefix[12:16])
	if headerLen > maxHeaderBytes {
		return fmt.Errorf("the trace header claims %d bytes, which is not plausible", headerLen)
	}

	headerJSON := make([]byte, headerLen)
	if _, err := io.ReadFull(r.file, headerJSON); err != nil {
		return fmt.Errorf("read trace header: %w", err)
	}
	if err := json.Unmarshal(headerJSON, &r.header); err != nil {
		return fmt.Errorf("parse trace header: %w", err)
	}
	return nil
}

// Header returns the decoded preamble.
func (r *Reader) Header() Header { return r.header }

// ReadHeader reads only a trace's header, without decompressing any records.
func ReadHeader(path string) (Header, error) {
	file, err := os.Open(path)
	if err != nil {
		return Header{}, fmt.Errorf("open trace: %w", err)
	}
	defer file.Close()

	r := &Reader{file: file}
	if err := r.readPreamble(); err != nil {
		return Header{}, err
	}
	return r.header, nil
}

// Next decodes the next record into rec. It returns io.EOF at the end of the
// stream. rec is overwritten each call, so a caller keeping records must copy.
func (r *Reader) Next(rec *Record) error {
	kind, err := r.buf.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return fmt.Errorf("read record type: %w", err)
	}

	rec.Type = RecordType(kind)

	switch rec.Type {
	case RecSpawn:
		b, err := r.read(26)
		if err != nil {
			return err
		}
		rec.Time = float64FromBytes(b[0:8])
		rec.Entity = binary.LittleEndian.Uint32(b[8:12])
		rec.Class = binary.LittleEndian.Uint16(b[12:14])
		rec.X = float32FromBytes(b[14:18])
		rec.Y = float32FromBytes(b[18:22])
		rec.Z = float32FromBytes(b[22:26])

	case RecSegment:
		b, err := r.read(24)
		if err != nil {
			return err
		}
		rec.Entity = binary.LittleEndian.Uint32(b[0:4])
		rec.Time = float64FromBytes(b[4:12])
		rec.X = float32FromBytes(b[12:16])
		rec.Y = float32FromBytes(b[16:20])
		rec.Z = float32FromBytes(b[20:24])

	case RecState:
		b, err := r.read(13)
		if err != nil {
			return err
		}
		rec.Time = float64FromBytes(b[0:8])
		rec.Entity = binary.LittleEndian.Uint32(b[8:12])
		rec.EntityState = State(b[12])

	case RecResource:
		b, err := r.read(14)
		if err != nil {
			return err
		}
		rec.Time = float64FromBytes(b[0:8])
		rec.Resource = binary.LittleEndian.Uint16(b[8:10])
		rec.Busy = binary.LittleEndian.Uint16(b[10:12])
		rec.Queued = binary.LittleEndian.Uint16(b[12:14])

	case RecExit:
		b, err := r.read(12)
		if err != nil {
			return err
		}
		rec.Time = float64FromBytes(b[0:8])
		rec.Entity = binary.LittleEndian.Uint32(b[8:12])

	case RecMetric:
		b, err := r.read(18)
		if err != nil {
			return err
		}
		rec.Time = float64FromBytes(b[0:8])
		rec.Metric = binary.LittleEndian.Uint16(b[8:10])
		rec.Value = float64FromBytes(b[10:18])

	case RecEvent:
		b, err := r.read(15)
		if err != nil {
			return err
		}
		rec.Time = float64FromBytes(b[0:8])
		rec.Entity = binary.LittleEndian.Uint32(b[8:12])
		rec.Kind = EventKind(b[12])
		rec.Resource = binary.LittleEndian.Uint16(b[13:15])

	default:
		// An unknown type means the stream is out of sync. There is no safe
		// way to skip forward, because record lengths vary by type.
		return fmt.Errorf("unknown record type 0x%02x after %d records; the trace is corrupt",
			kind, r.count)
	}

	r.count++
	return nil
}

func (r *Reader) read(n int) ([]byte, error) {
	b := r.scratch[:n]
	if _, err := io.ReadFull(r.buf, b); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("the trace ends in the middle of a record after %d records", r.count)
		}
		return nil, err
	}
	return b, nil
}

// Count reports how many records have been read.
func (r *Reader) Count() uint64 { return r.count }

func (r *Reader) Close() error {
	var firstErr error
	if err := r.gz.Close(); err != nil {
		firstErr = err
	}
	if err := r.file.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Each iterates the whole stream, calling fn for every record. It stops at the
// first error from fn. This is the shape every post-processing pass uses.
func (r *Reader) Each(fn func(rec *Record) error) error {
	var rec Record
	for {
		err := r.Next(&rec)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(&rec); err != nil {
			return err
		}
	}
}

func float32FromBytes(b []byte) float64 {
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
}

func float64FromBytes(b []byte) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(b))
}
