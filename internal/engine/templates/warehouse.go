package templates

import (
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
)

// The warehouse template models order picking: an order is released, a picker
// walks the aisles it needs, then the order is packed and shipped from a dock.
//
// What decides throughput here is rarely the packing bench; it is how far
// pickers walk and how many of them there are. Aisles are therefore laid out
// as real geometry with real distances, so the travel time in the result comes
// from the layout rather than from a guessed constant.
func init() {
	register(&Template{
		Key:         "warehouse_picking",
		Name:        "Warehouse order picking",
		Description: "Orders released to pickers who walk the aisles, then pack and ship from dock doors. Reports order cycle time, picker utilisation and aisle congestion.",
		Domain:      spec.DomainWarehouse,
		Params: []ParamDef{
			{ID: "horizonHours", Label: "Simulated hours", Group: "Run",
				Default: 8, Min: 1, Max: 168, Step: 1, Unit: "h"},
			{ID: "warmUpMinutes", Label: "Warm-up", Group: "Run",
				Default: 20, Min: 0, Max: 720, Step: 5, Unit: "min"},

			{ID: "orderArrivalSeconds", Label: "Mean time between orders", Group: "Demand",
				Default: 60, Min: 5, Max: 3600, Step: 5, Unit: "s",
				Description: "Lower means a busier wave. 60 seconds is 60 orders an hour."},
			{ID: "linesPerOrder", Label: "Average lines per order", Group: "Demand",
				Default: 3, Min: 1, Max: 12, Step: 1, Integer: true,
				Description: "Each line sends the picker to one more aisle."},

			{ID: "aisles", Label: "Aisles", Group: "Layout",
				Default: 6, Min: 2, Max: 24, Step: 1, Integer: true},
			{ID: "aisleLength", Label: "Aisle length", Group: "Layout",
				Default: 40, Min: 10, Max: 200, Step: 5, Unit: "m"},
			{ID: "aisleSpacing", Label: "Distance between aisles", Group: "Layout",
				Default: 8, Min: 2, Max: 30, Step: 1, Unit: "m"},
			{ID: "aisleCapacity", Label: "Pickers allowed in one aisle", Group: "Layout",
				Default: 1, Min: 0, Max: 10, Step: 1, Integer: true,
				Description: "A narrow aisle holds one picker at a time, which is what creates congestion. Zero means no limit."},

			{ID: "pickers", Label: "Pickers", Group: "Labour",
				Default: 10, Min: 1, Max: 60, Step: 1, Integer: true,
				Description: "A picker is held for a whole order, walking included, so this is the parameter that usually binds."},
			{ID: "pickerSpeed", Label: "Walking speed", Group: "Labour",
				Default: 1.2, Min: 0.3, Max: 4, Step: 0.1, Unit: "m/s"},
			{ID: "pickSeconds", Label: "Mean time to pick one line", Group: "Labour",
				Default: 25, Min: 3, Max: 600, Step: 1, Unit: "s"},

			{ID: "packBenches", Label: "Packing benches", Group: "Despatch",
				Default: 3, Min: 1, Max: 30, Step: 1, Integer: true},
			{ID: "packSeconds", Label: "Mean packing time per order", Group: "Despatch",
				Default: 90, Min: 10, Max: 1800, Step: 5, Unit: "s"},
			{ID: "dockDoors", Label: "Dock doors", Group: "Despatch",
				Default: 2, Min: 1, Max: 30, Step: 1, Integer: true},
			{ID: "loadSeconds", Label: "Mean loading time per order", Group: "Despatch",
				Default: 40, Min: 5, Max: 900, Step: 5, Unit: "s"},
		},
		build: buildWarehouse,
	})
}

func buildWarehouse(v map[string]float64) (*spec.Model, error) {
	aisleCount := intOf(v["aisles"], 2)
	aisleLength := v["aisleLength"]
	spacing := v["aisleSpacing"]
	pickTime := v["pickSeconds"]
	packTime := v["packSeconds"]
	loadTime := v["loadSeconds"]

	m := &spec.Model{
		Schema:      spec.SchemaVersion,
		Name:        "Warehouse order picking",
		Description: "Orders picked from aisles, packed and loaded at the dock.",
		Domain:      spec.DomainWarehouse,
		Horizon:     v["horizonHours"] * 3600,
		WarmUp:      clampWarmUp(v["warmUpMinutes"]*60, v["horizonHours"]*3600),

		EntityTypes: []spec.EntityType{{
			ID:    "order",
			Label: "Order",
			Shape: spec.Shape{Type: "box", Length: 1.2, Width: 0.8, Height: 1.4, Color: "#3fc98a"},
			Speed: normPositive(v["pickerSpeed"], v["pickerSpeed"]*0.15, 0.2),
		}},

		Nodes: []spec.Node{
			{ID: "release", Label: "Order release", X: 0, Y: 0},
			{ID: "main_aisle", Label: "Main aisle", X: 20, Y: 0},
			{ID: "pack", Label: "Packing", X: 20, Y: -30},
			{ID: "dock", Label: "Dock doors", X: 0, Y: -50},
			{ID: "shipped", Label: "Shipped", X: -25, Y: -50},
		},

		Links: []spec.Link{
			{From: "release", To: "main_aisle", Bidirectional: true},
			{From: "main_aisle", To: "pack", Bidirectional: true},
			{From: "pack", To: "dock", Bidirectional: true},
			{From: "dock", To: "shipped", Bidirectional: false},
		},

		Resources: []spec.Resource{
			{
				ID: "picker", Label: "Picker",
				Capacity: intOf(v["pickers"], 1),
				Node:     "release",
				// A picker is held for the whole order, which is why a long
				// walk costs labour rather than just time.
				Service: rngConstantZero(),
			},
			{
				ID: "pack_bench", Label: "Packing bench",
				Capacity: intOf(v["packBenches"], 1),
				Node:     "pack",
				Service:  triAround(packTime),
			},
			{
				ID: "dock_door", Label: "Dock door",
				Capacity: intOf(v["dockDoors"], 1),
				Node:     "dock",
				Service:  triAround(loadTime),
			},
		},

		Sources: []spec.Source{{
			ID: "order_release", Label: "Order release",
			Entity: "order", Node: "release",
			Arrival: expo(v["orderArrivalSeconds"]),
			Route:   "pick_pack_ship",
		}},
	}

	// Each aisle is a node at the far end, reached from the main aisle. The
	// distance a picker covers is therefore the real geometry of the layout.
	aisleBranches := make([]spec.Branch, 0, aisleCount)

	for i := 0; i < aisleCount; i++ {
		id := fmt.Sprintf("aisle_%d", i+1)
		label := fmt.Sprintf("Aisle %d", i+1)
		y := float64(i) * spacing

		m.Nodes = append(m.Nodes,
			spec.Node{ID: id + "_entry", Label: label + " entry", X: 20, Y: y},
			spec.Node{ID: id + "_face", Label: label + " pick face", X: 20 + aisleLength, Y: y},
		)

		m.Links = append(m.Links,
			spec.Link{From: "main_aisle", To: id + "_entry", Bidirectional: true},
			spec.Link{
				From: id + "_entry", To: id + "_face", Bidirectional: true,
				// A narrow aisle is the classic warehouse bottleneck: one
				// picker in it blocks the next, and that shows up as
				// congestion rather than as extra walking.
				Capacity: intOf(v["aisleCapacity"], 0),
			},
		)

		m.Zones = append(m.Zones, spec.Zone{
			ID: id, Label: label,
			X: 20, Y: y - spacing/3, Width: aisleLength, Height: spacing * 2 / 3,
			Color: "#3fc98a",
		})

		aisleBranches = append(aisleBranches, spec.Branch{
			Weight: 1, Label: label,
			Steps: []spec.Step{
				{Type: spec.StepTravel, To: id + "_face", Label: "Walk to " + label},
				{Type: spec.StepDelay, Label: "Pick a line",
					Duration: ptr(triAround(pickTime))},
				{Type: spec.StepTravel, To: "main_aisle", Label: "Back to the main aisle"},
			},
		})
	}

	// One branch step per line: each sends the picker to a randomly chosen
	// aisle, so an order with more lines walks further.
	lines := intOf(v["linesPerOrder"], 1)
	steps := []spec.Step{
		{Type: spec.StepSeize, Resource: "picker", Label: "Assign a picker"},
		{Type: spec.StepTravel, To: "main_aisle", Label: "Enter the pick area"},
	}
	for i := 0; i < lines; i++ {
		steps = append(steps, spec.Step{
			Type:     spec.StepBranch,
			Label:    fmt.Sprintf("Pick line %d", i+1),
			Branches: aisleBranches,
		})
	}
	steps = append(steps,
		spec.Step{Type: spec.StepTravel, To: "pack", Label: "Carry to packing"},
		spec.Step{Type: spec.StepRelease, Resource: "picker", Label: "Picker released"},
		spec.Step{Type: spec.StepUse, Resource: "pack_bench", Label: "Pack the order"},
		spec.Step{Type: spec.StepTravel, To: "dock", Label: "Move to the dock"},
		spec.Step{Type: spec.StepUse, Resource: "dock_door", Label: "Load"},
		spec.Step{Type: spec.StepTravel, To: "shipped", Label: "Despatched"},
		spec.Step{Type: spec.StepExit},
	)

	m.Routes = []spec.Route{{
		ID: "pick_pack_ship", Label: "Pick, pack and ship",
		Source: "order_release",
		Steps:  steps,
	}}

	m.Params = registry["warehouse_picking"].specParams(v)
	return m, nil
}
