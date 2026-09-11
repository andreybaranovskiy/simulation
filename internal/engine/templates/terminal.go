package templates

import (
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
)

// The container terminal template models the path a road truck takes through a
// marine terminal: in at the gate, through inspection, to a yard block for the
// crane, then back out. The quantity a terminal operator cares about is truck
// turnaround time, and the two things that drive it are gate lanes and crane
// availability, so those are the headline parameters.
func init() {
	register(&Template{
		Key:         "container_terminal",
		Name:        "Container terminal",
		Description: "Road trucks entering a marine terminal: gate, inspection, yard crane and exit. Reports turnaround time, gate queueing and crane utilisation.",
		Domain:      spec.DomainTerminal,
		Params: []ParamDef{
			{ID: "horizonHours", Label: "Simulated hours", Group: "Run",
				Default: 8, Min: 1, Max: 168, Step: 1, Unit: "h",
				Description: "Length of the modelled shift or day."},
			{ID: "warmUpMinutes", Label: "Warm-up", Group: "Run",
				Default: 30, Min: 0, Max: 720, Step: 5, Unit: "min",
				Description: "Ignored at the start, so statistics describe steady state rather than an empty terminal."},

			{ID: "truckArrivalSeconds", Label: "Mean time between truck arrivals", Group: "Demand",
				Default: 90, Min: 10, Max: 3600, Step: 5, Unit: "s",
				Description: "Lower means busier. 90 seconds is about 40 trucks an hour."},
			{ID: "truckSpeed", Label: "Truck speed on site", Group: "Demand",
				Default: 6, Min: 1, Max: 20, Step: 0.5, Unit: "m/s",
				Description: "About 22 km/h at the default."},

			{ID: "gateLanes", Label: "Gate lanes", Group: "Gate",
				Default: 3, Min: 1, Max: 20, Step: 1, Integer: true,
				Description: "Lanes processing arriving trucks in parallel."},
			{ID: "gateServiceSeconds", Label: "Mean gate processing time", Group: "Gate",
				Default: 120, Min: 10, Max: 1800, Step: 10, Unit: "s",
				Description: "Average time at the gate. Individual trucks vary either side of it."},
			{ID: "gateQueueLimit", Label: "Gate queue capacity", Group: "Gate",
				Default: 0, Min: 0, Max: 200, Step: 1, Integer: true,
				Description: "Trucks that arrive to a full queue are turned away. Zero means no limit."},

			{ID: "inspectionShare", Label: "Trucks sent to inspection", Group: "Inspection",
				Default: 15, Min: 0, Max: 100, Step: 1, Unit: "%",
				Description: "Share diverted for a customs or safety check."},
			{ID: "inspectionBays", Label: "Inspection bays", Group: "Inspection",
				Default: 2, Min: 1, Max: 10, Step: 1, Integer: true,
				Description: "Two bays keep up with the default arrival rate; one does not."},
			{ID: "inspectionMinutes", Label: "Mean inspection time", Group: "Inspection",
				Default: 12, Min: 1, Max: 120, Step: 1, Unit: "min"},

			{ID: "yardCranes", Label: "Yard cranes", Group: "Yard",
				Default: 4, Min: 1, Max: 20, Step: 1, Integer: true,
				Description: "Cranes handling containers on and off trucks, split between the two yard blocks."},
			{ID: "craneCycleSeconds", Label: "Mean crane cycle time", Group: "Yard",
				Default: 180, Min: 30, Max: 1800, Step: 10, Unit: "s",
				Description: "One container on or off a truck."},
			{ID: "craneBreakdownHours", Label: "Mean time between crane failures", Group: "Yard",
				Default: 0, Min: 0, Max: 500, Step: 1, Unit: "h",
				Description: "Zero means cranes never fail."},
			{ID: "craneRepairMinutes", Label: "Crane repair time", Group: "Yard",
				Default: 45, Min: 1, Max: 600, Step: 5, Unit: "min"},

			{ID: "exitLanes", Label: "Exit lanes", Group: "Exit",
				Default: 2, Min: 1, Max: 20, Step: 1, Integer: true},
			{ID: "exitServiceSeconds", Label: "Mean exit check time", Group: "Exit",
				Default: 60, Min: 5, Max: 900, Step: 5, Unit: "s"},
		},
		build: buildTerminal,
	})
}

func buildTerminal(v map[string]float64) (*spec.Model, error) {
	gateService := v["gateServiceSeconds"]
	craneCycle := v["craneCycleSeconds"]
	inspection := v["inspectionMinutes"] * 60
	exitService := v["exitServiceSeconds"]

	m := &spec.Model{
		Schema:      spec.SchemaVersion,
		Name:        "Container terminal",
		Description: "Road trucks through gate, inspection, yard crane and exit.",
		Domain:      spec.DomainTerminal,
		Horizon:     v["horizonHours"] * 3600,
		WarmUp:      clampWarmUp(v["warmUpMinutes"]*60, v["horizonHours"]*3600),

		EntityTypes: []spec.EntityType{{
			ID:    "truck",
			Label: "Container truck",
			Shape: spec.Shape{Type: "box", Length: 16, Width: 2.6, Height: 4, Color: "#4c8dff"},
			// Real traffic does not move at one speed, and that spread is
			// what makes arrivals at the crane irregular.
			Speed: normPositive(v["truckSpeed"], v["truckSpeed"]*0.2, 0.5),
		}},

		// Coordinates are metres on the site plan: roughly a 900 by 400 metre
		// terminal, with the gate at the west end and the quay to the east.
		Nodes: []spec.Node{
			{ID: "approach", Label: "Approach road", X: 0, Y: 200},
			{ID: "gate", Label: "Gate", X: 120, Y: 200},
			{ID: "inspection", Label: "Inspection bay", X: 220, Y: 60},
			{ID: "yard_junction", Label: "Yard junction", X: 320, Y: 200},
			{ID: "yard_a", Label: "Yard block A", X: 560, Y: 120},
			{ID: "yard_b", Label: "Yard block B", X: 560, Y: 280},
			{ID: "exit_lane", Label: "Exit check", X: 200, Y: 340},
			{ID: "departure", Label: "Departure", X: 0, Y: 340},
		},

		Links: []spec.Link{
			{From: "approach", To: "gate", Bidirectional: false},
			{From: "gate", To: "yard_junction", Bidirectional: true},
			{From: "gate", To: "inspection", Bidirectional: true},
			{From: "inspection", To: "yard_junction", Bidirectional: true},
			{From: "yard_junction", To: "yard_a", Bidirectional: true},
			{From: "yard_junction", To: "yard_b", Bidirectional: true},
			{From: "yard_junction", To: "exit_lane", Bidirectional: true},
			{From: "exit_lane", To: "departure", Bidirectional: false},
		},

		Zones: []spec.Zone{
			{ID: "gate_area", Label: "Gate area", X: 80, Y: 150, Width: 120, Height: 100, Color: "#4c8dff"},
			{ID: "yard", Label: "Container yard", X: 480, Y: 60, Width: 200, Height: 300, Color: "#3fc98a"},
			{ID: "inspection_area", Label: "Inspection", X: 180, Y: 20, Width: 100, Height: 80, Color: "#f2c14e"},
		},

		Resources: []spec.Resource{
			{
				ID: "gate_lane", Label: "Gate lane",
				Capacity: intOf(v["gateLanes"], 1),
				Node:     "gate",
				Service:  triAround(gateService),
				Queue: spec.QueueSpec{
					Discipline: spec.FIFO,
					Capacity:   intOf(v["gateQueueLimit"], 0),
				},
			},
			{
				ID: "inspection_bay", Label: "Inspection bay",
				Capacity: intOf(v["inspectionBays"], 1),
				Node:     "inspection",
				Service:  triAroundWide(inspection),
			},
			{
				ID: "exit_lane", Label: "Exit lane",
				Capacity: intOf(v["exitLanes"], 1),
				Node:     "exit_lane",
				Service:  triAround(exitService),
			},
		},

		Sources: []spec.Source{{
			ID: "gate_arrivals", Label: "Truck arrivals",
			Entity: "truck", Node: "approach",
			// Arrivals at a gate are close to a Poisson process, which is what
			// an exponential gap produces.
			Arrival: expo(v["truckArrivalSeconds"]),
			Route:   "truck_visit",
		}},
	}

	// Cranes are one resource per yard block, so the two blocks queue
	// independently and a heatmap can show one busier than the other.
	cranesPerBlock := intOf(v["yardCranes"], 1)
	half := cranesPerBlock / 2
	if half < 1 {
		half = 1
	}
	remainder := cranesPerBlock - half
	if remainder < 1 {
		remainder = 1
	}

	for _, block := range []struct {
		id, node, label string
		capacity        int
	}{
		{"crane_a", "yard_a", "Yard crane, block A", half},
		{"crane_b", "yard_b", "Yard crane, block B", remainder},
	} {
		r := spec.Resource{
			ID: block.id, Label: block.label,
			Capacity: block.capacity,
			Node:     block.node,
			Service:  triAround(craneCycle),
		}
		if hours := v["craneBreakdownHours"]; hours > 0 {
			r.Failure = &spec.Failure{
				Uptime: expo(hours * 3600),
				Repair: triAroundWide(v["craneRepairMinutes"] * 60),
			}
		}
		m.Resources = append(m.Resources, r)
	}

	inspectShare := v["inspectionShare"]

	m.Routes = []spec.Route{{
		ID: "truck_visit", Label: "Truck visit", Source: "gate_arrivals",
		Steps: []spec.Step{
			{Type: spec.StepTravel, To: "gate", Label: "Drive to the gate"},
			{Type: spec.StepUse, Resource: "gate_lane", Label: "Gate processing"},

			// The inspection diversion is what makes the gate queue and the
			// yard queue interact: a truck held at inspection is one the crane
			// is not waiting for.
			{Type: spec.StepBranch, Label: "Selected for inspection?", Branches: []spec.Branch{
				{
					Weight: inspectShare, Label: "Inspected",
					Steps: []spec.Step{
						{Type: spec.StepTravel, To: "inspection"},
						{Type: spec.StepUse, Resource: "inspection_bay", Label: "Customs inspection"},
					},
				},
				{Weight: 100 - inspectShare, Label: "Straight through"},
			}},

			{Type: spec.StepTravel, To: "yard_junction", Label: "Drive into the yard"},

			{Type: spec.StepBranch, Label: "Which yard block?", Branches: []spec.Branch{
				{
					Weight: 1, Label: "Block A",
					Steps: []spec.Step{
						{Type: spec.StepTravel, To: "yard_a"},
						{Type: spec.StepUse, Resource: "crane_a", Label: "Container handled"},
						{Type: spec.StepTravel, To: "yard_junction"},
					},
				},
				{
					Weight: 1, Label: "Block B",
					Steps: []spec.Step{
						{Type: spec.StepTravel, To: "yard_b"},
						{Type: spec.StepUse, Resource: "crane_b", Label: "Container handled"},
						{Type: spec.StepTravel, To: "yard_junction"},
					},
				},
			}},

			{Type: spec.StepTravel, To: "exit_lane", Label: "Drive to the exit"},
			{Type: spec.StepUse, Resource: "exit_lane", Label: "Exit check"},
			{Type: spec.StepTravel, To: "departure", Label: "Leave the terminal"},
			{Type: spec.StepExit},
		},
	}}

	m.Params = registry["container_terminal"].specParams(v)
	return m, nil
}
