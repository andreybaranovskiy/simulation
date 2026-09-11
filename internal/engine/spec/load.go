package spec

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andreybaranovskiy/simulation/internal/engine/rng"
)

// maxSpecBytes caps a model definition. A spec describes a system; it is not
// where bulk data belongs, and anything larger is a mistake worth catching.
const maxSpecBytes = 16 << 20

// Parse reads a model from JSON or YAML, detected from the content. It fills
// defaults and validates, so a Model returned here is safe to execute.
func Parse(data []byte) (*Model, error) {
	if len(data) > maxSpecBytes {
		return nil, fmt.Errorf("the model is %d bytes, above the %d byte limit", len(data), maxSpecBytes)
	}

	m := &Model{}
	trimmed := strings.TrimSpace(string(data))

	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal(data, m); err != nil {
			return nil, fmt.Errorf("the model is not valid JSON: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, m); err != nil {
			return nil, fmt.Errorf("the model is not valid YAML: %w", err)
		}
	}

	m.ApplyDefaults()
	if err := m.ValidateStrict(); err != nil {
		return nil, err
	}
	return m, nil
}

// ParseReader reads a model from a stream, bounded by the same size limit.
func ParseReader(r io.Reader) (*Model, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSpecBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the model: %w", err)
	}
	return Parse(data)
}

// Marshal renders a model as indented JSON, which is how it is stored.
func (m *Model) Marshal() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// ApplyDefaults fills in the values a spec may leave out. Doing this once here
// means the interpreter never has to ask whether a field was set.
func (m *Model) ApplyDefaults() {
	if m.Schema == "" {
		m.Schema = SchemaVersion
	}
	if m.Domain == "" {
		m.Domain = DomainGeneric
	}
	if m.Units.Length == "" {
		m.Units.Length = "m"
	}
	if m.Units.Time == "" {
		m.Units.Time = "s"
	}

	for i := range m.EntityTypes {
		e := &m.EntityTypes[i]
		if e.Label == "" {
			e.Label = e.ID
		}
		if e.Speed.Kind == "" && e.Speed.Value == 0 {
			// Roughly 5 km/h, a sensible default for anything moving around a
			// site under its own control.
			e.Speed = rng.Fixed(1.4)
		}
		if e.Shape.Type == "" {
			e.Shape.Type = "box"
		}
		if e.Shape.Length == 0 {
			e.Shape.Length = 2
		}
		if e.Shape.Width == 0 {
			e.Shape.Width = 2
		}
		if e.Shape.Height == 0 {
			e.Shape.Height = 2
		}
		if e.Shape.Color == "" {
			e.Shape.Color = defaultColors[i%len(defaultColors)]
		}
	}

	for i := range m.Nodes {
		if m.Nodes[i].Label == "" {
			m.Nodes[i].Label = m.Nodes[i].ID
		}
	}

	for i := range m.Resources {
		r := &m.Resources[i]
		if r.Label == "" {
			r.Label = r.ID
		}
		if r.Capacity == 0 {
			r.Capacity = 1
		}
		if r.Queue.Discipline == "" {
			r.Queue.Discipline = FIFO
		}
	}

	for i := range m.Sources {
		s := &m.Sources[i]
		if s.Label == "" {
			s.Label = s.ID
		}
	}

	for i := range m.Routes {
		r := &m.Routes[i]
		if r.Label == "" {
			r.Label = r.ID
		}
		applyStepDefaults(r.Steps)
	}

	for i := range m.Zones {
		if m.Zones[i].Label == "" {
			m.Zones[i].Label = m.Zones[i].ID
		}
	}

	for i := range m.Params {
		if m.Params[i].Label == "" {
			m.Params[i].Label = m.Params[i].ID
		}
	}
}

func applyStepDefaults(steps []Step) {
	for i := range steps {
		s := &steps[i]
		if s.Label == "" {
			s.Label = defaultStepLabel(*s)
		}
		for j := range s.Branches {
			if s.Branches[j].Weight == 0 {
				s.Branches[j].Weight = 1
			}
			applyStepDefaults(s.Branches[j].Steps)
		}
	}
}

func defaultStepLabel(s Step) string {
	switch s.Type {
	case StepTravel:
		return "Travel to " + s.To
	case StepSeize:
		return "Wait for " + s.Resource
	case StepRelease:
		return "Release " + s.Resource
	case StepUse:
		return "Use " + s.Resource
	case StepDelay:
		return "Delay"
	case StepBranch:
		return "Branch"
	case StepExit:
		return "Exit"
	}
	return string(s.Type)
}

// defaultColors is a readable categorical sequence used when a spec does not
// name a colour. The hues are spaced far enough apart to stay distinct in a
// crowded 2D view and in a printed report.
var defaultColors = []string{
	"#4c8dff", "#ff8a4c", "#3fc98a", "#e0566a",
	"#a97cff", "#f2c14e", "#4fd1e0", "#d97bb5",
}

// Clone returns a deep copy, so applying scenario parameters to a model does
// not mutate the stored definition every other scenario shares.
func (m *Model) Clone() (*Model, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("copy the model: %w", err)
	}
	out := &Model{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("copy the model: %w", err)
	}
	return out, nil
}

// Summary is the compact description shown in scenario lists and on a report's
// assumptions page.
type Summary struct {
	Name        string  `json:"name"`
	Domain      Domain  `json:"domain"`
	Horizon     float64 `json:"horizon"`
	WarmUp      float64 `json:"warmUp"`
	EntityTypes int     `json:"entityTypes"`
	Nodes       int     `json:"nodes"`
	Resources   int     `json:"resources"`
	Sources     int     `json:"sources"`
	Routes      int     `json:"routes"`
	// TotalCapacity is the sum of every resource capacity, a one-number sense
	// of how much the modelled system can do at once.
	TotalCapacity int `json:"totalCapacity"`
	// ExpectedArrivals estimates how many entities the run will create, which
	// is what decides whether a run takes a second or an hour.
	ExpectedArrivals int `json:"expectedArrivals"`
}

func (m *Model) Summary() Summary {
	s := Summary{
		Name:        m.Name,
		Domain:      m.Domain,
		Horizon:     m.Horizon,
		WarmUp:      m.WarmUp,
		EntityTypes: len(m.EntityTypes),
		Nodes:       len(m.Nodes),
		Resources:   len(m.Resources),
		Sources:     len(m.Sources),
		Routes:      len(m.Routes),
	}

	for _, r := range m.Resources {
		s.TotalCapacity += r.Capacity
	}
	s.ExpectedArrivals = m.ExpectedArrivals()
	return s
}

// ExpectedArrivals estimates the entity count from the arrival distributions.
// It is an estimate, not a promise: it is used to size buffers and to warn
// before someone starts a run that will produce a hundred million events.
func (m *Model) ExpectedArrivals() int {
	total := 0.0

	for _, src := range m.Sources {
		window := m.Horizon
		if src.Stop > 0 && src.Stop < window {
			window = src.Stop
		}
		window -= src.Start
		if window <= 0 {
			continue
		}

		gap := src.Arrival.ExpectedValue()
		if gap <= 0 {
			continue
		}

		count := window / gap
		if src.Batch.Kind != "" {
			if batch := src.Batch.ExpectedValue(); batch > 1 {
				count *= batch
			}
		}
		if src.Limit > 0 && count > float64(src.Limit) {
			count = float64(src.Limit)
		}
		total += count
	}

	if math.IsInf(total, 0) || math.IsNaN(total) || total > float64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int(total)
}
