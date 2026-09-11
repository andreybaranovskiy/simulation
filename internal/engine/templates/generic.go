package templates

import (
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/engine/rng"
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
)

// The generic template is a tandem queueing network: arrivals pass through a
// chain of service stages, each with its own server count and service time.
//
// It exists for two reasons. It covers any system the two domain templates do
// not, and because a tandem queue has known analytic behaviour it is the model
// to reach for when checking that the engine itself is behaving.
func init() {
	register(&Template{
		Key:         "queueing_network",
		Name:        "Queueing network",
		Description: "Arrivals through a chain of service stages. Domain-neutral, and the right starting point for a system that is not a terminal or a warehouse.",
		Domain:      spec.DomainGeneric,
		Params: []ParamDef{
			{ID: "horizonHours", Label: "Simulated hours", Group: "Run",
				Default: 4, Min: 1, Max: 168, Step: 1, Unit: "h"},
			{ID: "warmUpMinutes", Label: "Warm-up", Group: "Run",
				Default: 15, Min: 0, Max: 720, Step: 5, Unit: "min"},

			{ID: "arrivalSeconds", Label: "Mean time between arrivals", Group: "Demand",
				Default: 30, Min: 1, Max: 3600, Step: 1, Unit: "s"},
			{ID: "entitySpeed", Label: "Movement speed", Group: "Demand",
				Default: 2, Min: 0.1, Max: 50, Step: 0.1, Unit: "m/s"},

			{ID: "stages", Label: "Service stages", Group: "Network",
				Default: 3, Min: 1, Max: 10, Step: 1, Integer: true,
				Description: "Stages are visited in order, each with its own queue."},
			{ID: "stageDistance", Label: "Distance between stages", Group: "Network",
				Default: 30, Min: 0, Max: 500, Step: 5, Unit: "m"},

			{ID: "serversPerStage", Label: "Servers per stage", Group: "Service",
				Default: 2, Min: 1, Max: 50, Step: 1, Integer: true},
			{ID: "serviceSeconds", Label: "Mean service time", Group: "Service",
				Default: 50, Min: 1, Max: 3600, Step: 1, Unit: "s"},
			{ID: "serviceVariability", Label: "Service variability", Group: "Service",
				Default: 30, Min: 0, Max: 200, Step: 5, Unit: "%",
				Description: "Spread around the mean. More variability lengthens queues even when the average is unchanged."},

			{ID: "queueLimit", Label: "Queue capacity per stage", Group: "Service",
				Default: 0, Min: 0, Max: 1000, Step: 1, Integer: true,
				Description: "Arrivals to a full queue are turned away. Zero means no limit."},
			{ID: "maxWaitMinutes", Label: "Maximum wait before giving up", Group: "Service",
				Default: 0, Min: 0, Max: 600, Step: 1, Unit: "min",
				Description: "Zero means entities wait as long as it takes."},
		},
		build: buildQueueingNetwork,
	})
}

func buildQueueingNetwork(v map[string]float64) (*spec.Model, error) {
	stages := intOf(v["stages"], 1)
	distance := v["stageDistance"]
	service := v["serviceSeconds"]
	spread := service * v["serviceVariability"] / 100

	m := &spec.Model{
		Schema:      spec.SchemaVersion,
		Name:        "Queueing network",
		Description: fmt.Sprintf("Arrivals through %d service stages in series.", stages),
		Domain:      spec.DomainGeneric,
		Horizon:     v["horizonHours"] * 3600,
		WarmUp:      clampWarmUp(v["warmUpMinutes"]*60, v["horizonHours"]*3600),

		EntityTypes: []spec.EntityType{{
			ID:    "item",
			Label: "Item",
			Shape: spec.Shape{Type: "box", Length: 1.5, Width: 1.5, Height: 1.5, Color: "#4c8dff"},
			Speed: rng.Fixed(v["entitySpeed"]),
		}},

		Nodes: []spec.Node{{ID: "entry", Label: "Arrival", X: 0, Y: 0}},

		Sources: []spec.Source{{
			ID: "arrivals", Label: "Arrivals",
			Entity: "item", Node: "entry",
			Arrival: expo(v["arrivalSeconds"]),
			Route:   "through",
		}},
	}

	steps := []spec.Step{}

	for i := 0; i < stages; i++ {
		nodeID := fmt.Sprintf("stage_%d", i+1)
		resID := fmt.Sprintf("server_%d", i+1)
		label := fmt.Sprintf("Stage %d", i+1)
		x := float64(i+1) * distance

		m.Nodes = append(m.Nodes, spec.Node{ID: nodeID, Label: label, X: x, Y: 0})

		previous := "entry"
		if i > 0 {
			previous = fmt.Sprintf("stage_%d", i)
		}
		m.Links = append(m.Links, spec.Link{From: previous, To: nodeID, Bidirectional: true})

		resource := spec.Resource{
			ID: resID, Label: label + " server",
			Capacity: intOf(v["serversPerStage"], 1),
			Node:     nodeID,
			// A clamped normal keeps the mean a planner set while still
			// producing the spread that makes queues form.
			Service: normPositive(service, spread, service*0.05),
			Queue: spec.QueueSpec{
				Discipline: spec.FIFO,
				Capacity:   intOf(v["queueLimit"], 0),
				MaxWait:    v["maxWaitMinutes"] * 60,
			},
		}
		m.Resources = append(m.Resources, resource)

		steps = append(steps,
			spec.Step{Type: spec.StepTravel, To: nodeID, Label: "Move to " + label},
			spec.Step{Type: spec.StepUse, Resource: resID, Label: label + " service"},
		)
	}

	exitNode := "exit"
	m.Nodes = append(m.Nodes, spec.Node{
		ID: exitNode, Label: "Departure", X: float64(stages+1) * distance, Y: 0,
	})
	m.Links = append(m.Links, spec.Link{
		From: fmt.Sprintf("stage_%d", stages), To: exitNode, Bidirectional: false,
	})

	steps = append(steps,
		spec.Step{Type: spec.StepTravel, To: exitNode, Label: "Depart"},
		spec.Step{Type: spec.StepExit},
	)

	m.Routes = []spec.Route{{
		ID: "through", Label: "Through the network",
		Source: "arrivals", Steps: steps,
	}}

	m.Params = registry["queueing_network"].specParams(v)
	return m, nil
}

// rngConstantZero is a service time of zero, used where a resource is held
// across other steps rather than serving for a time of its own.
func rngConstantZero() rng.Dist { return rng.Fixed(0) }

func ptr(d rng.Dist) *rng.Dist { return &d }
