// Sample uploaded Go model.
//
// This is what an uploaded model looks like: a plain Go program that reads the
// scenario's parameters as a JSON object on standard input and writes a model
// definition as JSON on standard output. It imports nothing outside the
// standard library, which is a requirement — the server compiles uploaded
// models offline, so a non-stdlib import will not resolve.
//
// The definition it writes is the same declarative model the built-in
// templates produce; the server parses and validates it and runs it with the
// same engine. Writing the model in Go rather than by hand is worth it when the
// structure is computed: here, a line of N inspection lanes feeding a shared
// exit, where N is a parameter, so one program covers a whole family of
// layouts.
//
// To try it: upload this file as a Go model (admin only, and only where the
// operator has enabled the feature), create a scenario with parameters such as
// {"lanes": 3, "arrivalSeconds": 40, "serviceSeconds": 90}, and run it.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// dist is a distribution, matching the definition format's shape. Only the
// fields a given distribution uses are written.
type dist struct {
	Distribution string  `json:"distribution"`
	Value        float64 `json:"value,omitempty"`
	Mean         float64 `json:"mean,omitempty"`
	Min          float64 `json:"min,omitempty"`
	Mode         float64 `json:"mode,omitempty"`
	Max          float64 `json:"max,omitempty"`
}

func constant(v float64) dist      { return dist{Distribution: "constant", Value: v} }
func exponential(mean float64) dist { return dist{Distribution: "exponential", Mean: mean} }

// triangular spreads a service time around a mean without going negative, which
// is what makes a queue form rather than every job taking exactly as long.
func triangular(mean float64) dist {
	return dist{Distribution: "triangular", Min: mean * 0.6, Mode: mean * 0.9, Max: mean * 1.5}
}

type shape struct {
	Type   string  `json:"type,omitempty"`
	Length float64 `json:"length,omitempty"`
	Width  float64 `json:"width,omitempty"`
	Height float64 `json:"height,omitempty"`
	Color  string  `json:"color,omitempty"`
}

type entityType struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Shape shape  `json:"shape,omitempty"`
	Speed dist   `json:"speed,omitempty"`
}

type node struct {
	ID    string  `json:"id"`
	Label string  `json:"label,omitempty"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

type link struct {
	From          string `json:"from"`
	To            string `json:"to"`
	Bidirectional bool   `json:"bidirectional,omitempty"`
}

type queueSpec struct {
	Discipline string `json:"discipline,omitempty"`
}

type resource struct {
	ID       string    `json:"id"`
	Label    string    `json:"label,omitempty"`
	Capacity int       `json:"capacity"`
	Node     string    `json:"node,omitempty"`
	Service  dist      `json:"service,omitempty"`
	Queue    queueSpec `json:"queue,omitempty"`
}

type source struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Entity  string `json:"entity"`
	Node    string `json:"node"`
	Arrival dist   `json:"arrival"`
	Route   string `json:"route,omitempty"`
}

type step struct {
	Type     string   `json:"type"`
	Label    string   `json:"label,omitempty"`
	To       string   `json:"to,omitempty"`
	Resource string   `json:"resource,omitempty"`
	Branches []branch `json:"branches,omitempty"`
}

type branch struct {
	Weight float64 `json:"weight"`
	Label  string  `json:"label,omitempty"`
	Steps  []step  `json:"steps"`
}

type route struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	Source string `json:"source,omitempty"`
	Steps  []step `json:"steps"`
}

type param struct {
	ID    string  `json:"id"`
	Label string  `json:"label,omitempty"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
}

type modelDef struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Domain      string       `json:"domain,omitempty"`
	Horizon     float64      `json:"horizon"`
	WarmUp      float64      `json:"warmUp,omitempty"`
	Params      []param      `json:"params,omitempty"`
	EntityTypes []entityType `json:"entityTypes"`
	Nodes       []node       `json:"nodes"`
	Links       []link       `json:"links,omitempty"`
	Resources   []resource   `json:"resources,omitempty"`
	Sources     []source     `json:"sources"`
	Routes      []route      `json:"routes"`
}

func main() {
	// Parameters arrive as a JSON object of name to number. Missing ones fall
	// back to a default, so the program is runnable before anything is tuned.
	params := map[string]float64{}
	_ = json.NewDecoder(os.Stdin).Decode(&params)

	get := func(name string, fallback float64) float64 {
		if v, ok := params[name]; ok {
			return v
		}
		return fallback
	}

	lanes := int(math.Max(1, get("lanes", 2)))
	arrivalSeconds := get("arrivalSeconds", 45)
	serviceSeconds := get("serviceSeconds", 90)
	horizonHours := get("horizonHours", 8)
	laneSpacing := get("laneSpacing", 12)

	m := modelDef{
		Schema:      "sim.model/v1",
		Name:        fmt.Sprintf("Inspection line, %d lanes", lanes),
		Description: "A row of inspection lanes fed from one gate, then a shared exit.",
		Domain:      "intralogistics",
		Horizon:     horizonHours * 3600,
		WarmUp:      30 * 60,

		// Declaring the parameters lets the scenario editor show them with
		// labels and units; the program still reads them off stdin regardless.
		Params: []param{
			{ID: "lanes", Label: "Inspection lanes", Value: float64(lanes)},
			{ID: "arrivalSeconds", Label: "Seconds between arrivals", Value: arrivalSeconds, Unit: "s"},
			{ID: "serviceSeconds", Label: "Inspection time", Value: serviceSeconds, Unit: "s"},
		},

		EntityTypes: []entityType{{
			ID:    "truck",
			Label: "Truck",
			Shape: shape{Type: "box", Length: 4, Width: 2.2, Height: 2.4, Color: "#4c8dff"},
			Speed: constant(8),
		}},

		Nodes: []node{{ID: "gate", Label: "Gate", X: 0, Y: 0}},

		Sources: []source{{
			ID: "arrivals", Label: "Arrivals",
			Entity: "truck", Node: "gate",
			Arrival: exponential(arrivalSeconds),
			Route:   "inspect",
		}},
	}

	// One lane per unit of the parameter, laid out along the Y axis. Each is a
	// node, a single-capacity resource, and a branch the route can take.
	for i := 0; i < lanes; i++ {
		laneNode := fmt.Sprintf("lane_%d", i+1)
		laneRes := fmt.Sprintf("bay_%d", i+1)
		y := (float64(i) - float64(lanes-1)/2) * laneSpacing

		m.Nodes = append(m.Nodes, node{ID: laneNode, Label: fmt.Sprintf("Lane %d", i+1), X: 40, Y: y})
		m.Links = append(m.Links, link{From: "gate", To: laneNode, Bidirectional: true})
		m.Resources = append(m.Resources, resource{
			ID: laneRes, Label: fmt.Sprintf("Inspection bay %d", i+1),
			Capacity: 1, Node: laneNode,
			Service: triangular(serviceSeconds),
			Queue:   queueSpec{Discipline: "fifo"},
		})
	}

	m.Nodes = append(m.Nodes, node{ID: "exit", Label: "Exit", X: 80, Y: 0})
	for i := 0; i < lanes; i++ {
		m.Links = append(m.Links, link{From: fmt.Sprintf("lane_%d", i+1), To: "exit"})
	}

	// A truck picks a lane at random with equal weight, which is what spreads
	// the load and makes the extra lanes matter. This is where writing the
	// model in Go earns its place: one branch per lane, built in a loop, rather
	// than a fixed structure a hand-authored model would have to repeat.
	laneBranches := make([]branch, lanes)
	for i := 0; i < lanes; i++ {
		laneBranches[i] = branch{
			Weight: 1,
			Label:  fmt.Sprintf("Lane %d", i+1),
			Steps: []step{
				{Type: "travel", To: fmt.Sprintf("lane_%d", i+1), Label: "To lane"},
				{Type: "use", Resource: fmt.Sprintf("bay_%d", i+1), Label: "Inspection"},
			},
		}
	}

	steps := []step{
		{Type: "branch", Label: "Choose a lane", Branches: laneBranches},
		{Type: "travel", To: "exit", Label: "Depart"},
		{Type: "exit"},
	}
	m.Routes = []route{{ID: "inspect", Label: "Through inspection", Source: "arrivals", Steps: steps}}

	if err := json.NewEncoder(os.Stdout).Encode(m); err != nil {
		fmt.Fprintln(os.Stderr, "encode model:", err)
		os.Exit(1)
	}
}
