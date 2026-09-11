package templates

import (
	"strings"
	"testing"
)

// Every template must produce a model that passes validation for its defaults
// and at both ends of every parameter's range. A template that only works in
// the middle of its ranges is a template that will fail in a user's hands.
func TestTemplatesBuildAtDefaults(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("no templates are registered")
	}

	for _, tpl := range all {
		t.Run(tpl.Key, func(t *testing.T) {
			model, err := tpl.Build(nil)
			if err != nil {
				t.Fatalf("build at defaults: %v", err)
			}

			if model.Name == "" {
				t.Error("the model has no name")
			}
			if len(model.Sources) == 0 {
				t.Error("the model has no sources, so nothing would ever arrive")
			}
			if len(model.Routes) == 0 {
				t.Error("the model has no routes")
			}
			if model.Horizon <= 0 {
				t.Error("the model has no horizon")
			}
			if model.Domain != tpl.Domain {
				t.Errorf("the model declares domain %q, the template declares %q", model.Domain, tpl.Domain)
			}

			// Every parameter must reach the model, or a scenario that changes
			// it would silently do nothing.
			if len(model.Params) != len(tpl.Params) {
				t.Errorf("the template declares %d parameters but the model carries %d",
					len(tpl.Params), len(model.Params))
			}
		})
	}
}

func TestTemplatesBuildAtParameterExtremes(t *testing.T) {
	for _, tpl := range All() {
		for _, p := range tpl.Params {
			for _, bound := range []struct {
				name  string
				value float64
			}{{"min", p.Min}, {"max", p.Max}} {
				t.Run(tpl.Key+"/"+p.ID+"/"+bound.name, func(t *testing.T) {
					values := tpl.Defaults()
					values[p.ID] = bound.value

					if _, err := tpl.Build(values); err != nil {
						t.Fatalf("%s at its %s of %g: %v", p.Label, bound.name, bound.value, err)
					}
				})
			}
		}
	}
}

func TestBuildRejectsOutOfRangeValues(t *testing.T) {
	tpl, ok := Get("container_terminal")
	if !ok {
		t.Fatal("the container terminal template is not registered")
	}

	values := tpl.Defaults()
	values["gateLanes"] = 9999

	_, err := tpl.Build(values)
	if err == nil {
		t.Fatal("Build accepted a gate lane count far outside its range")
	}
	if !strings.Contains(err.Error(), "range") {
		t.Errorf("the error %q does not explain that the value is out of range", err)
	}
}

func TestBuildRejectsUnknownParameters(t *testing.T) {
	tpl, _ := Get("container_terminal")

	_, err := tpl.Build(map[string]float64{"noSuchKnob": 1})
	if err == nil {
		t.Fatal("Build accepted a parameter the template does not have")
	}
	if !strings.Contains(err.Error(), "noSuchKnob") {
		t.Errorf("the error %q does not name the offending parameter", err)
	}
}

// A template's parameter definitions have to be coherent, or the editor will
// render a control nobody can use.
func TestParameterDefinitionsAreSane(t *testing.T) {
	for _, tpl := range All() {
		t.Run(tpl.Key, func(t *testing.T) {
			seen := map[string]bool{}

			for _, p := range tpl.Params {
				if p.ID == "" || p.Label == "" {
					t.Errorf("parameter %q needs both an id and a label", p.ID)
				}
				if seen[p.ID] {
					t.Errorf("duplicate parameter id %q", p.ID)
				}
				seen[p.ID] = true

				if p.Min > p.Max {
					t.Errorf("%s has a minimum above its maximum", p.ID)
				}
				if p.Default < p.Min || p.Default > p.Max {
					t.Errorf("%s defaults to %g, outside its range %g to %g",
						p.ID, p.Default, p.Min, p.Max)
				}
			}
		})
	}
}

// Expected arrivals is what warns a user before they start a run that will
// take an hour, so it has to be in the right order of magnitude.
func TestExpectedArrivalsIsPlausible(t *testing.T) {
	tpl, _ := Get("container_terminal")

	values := tpl.Defaults()
	values["horizonHours"] = 8
	values["truckArrivalSeconds"] = 90

	model, err := tpl.Build(values)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Eight hours at one truck every 90 seconds is 320.
	got := model.ExpectedArrivals()
	if got < 280 || got > 360 {
		t.Errorf("expected about 320 arrivals, the model estimates %d", got)
	}
}
