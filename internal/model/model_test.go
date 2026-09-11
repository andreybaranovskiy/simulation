package model

import (
	"math"
	"testing"
)

func TestRoleOrdering(t *testing.T) {
	tests := []struct {
		have, want Role
		ok         bool
	}{
		{RoleOwner, RoleOwner, true},
		{RoleOwner, RoleEditor, true},
		{RoleOwner, RoleViewer, true},
		{RoleEditor, RoleOwner, false},
		{RoleEditor, RoleEditor, true},
		{RoleEditor, RoleViewer, true},
		{RoleViewer, RoleEditor, false},
		{RoleViewer, RoleViewer, true},
		{Role(""), RoleViewer, false},
		{Role("superuser"), RoleViewer, false},
	}

	for _, tt := range tests {
		if got := tt.have.AtLeast(tt.want); got != tt.ok {
			t.Errorf("Role(%q).AtLeast(%q) = %v, want %v", tt.have, tt.want, got, tt.ok)
		}
	}
}

func TestRoleCapabilities(t *testing.T) {
	if !RoleViewer.CanRead() || RoleViewer.CanWrite() || RoleViewer.CanAdmin() {
		t.Error("a viewer should read only")
	}
	if !RoleEditor.CanWrite() || RoleEditor.CanAdmin() {
		t.Error("an editor should write but not administer")
	}
	if !RoleOwner.CanAdmin() {
		t.Error("an owner should administer")
	}
	// An unrecognised role must grant nothing, so a bad database value fails
	// closed rather than open.
	if Role("nonsense").CanRead() {
		t.Error("an unknown role granted read access")
	}
}

func TestParseRole(t *testing.T) {
	if r, ok := ParseRole("  Owner "); !ok || r != RoleOwner {
		t.Errorf("ParseRole(\"  Owner \") = %q, %v; want owner, true", r, ok)
	}
	if _, ok := ParseRole("root"); ok {
		t.Error("ParseRole accepted an undefined role")
	}
}

func TestParseAssetKind(t *testing.T) {
	if k, ok := ParseAssetKind("PLAN_IMAGE"); !ok || k != AssetPlanImage {
		t.Errorf("ParseAssetKind(\"PLAN_IMAGE\") = %q, %v", k, ok)
	}
	if _, ok := ParseAssetKind("executable"); ok {
		t.Error("ParseAssetKind accepted an undefined kind")
	}
}

// The plan transform is what makes heatmap cells and report scale bars real
// distances, so a round trip through it must land back where it started.
func TestSitePlanCoordinateRoundTrip(t *testing.T) {
	plans := []SitePlan{
		{MetersPerPixel: 1, FlipY: false},
		{MetersPerPixel: 0.25, OriginPxX: 120, OriginPxY: 340, FlipY: true},
		{MetersPerPixel: 2.5, OriginPxX: -40, OriginPxY: 15, RotationDeg: 30, FlipY: true},
		{MetersPerPixel: 0.8, RotationDeg: -117.5, FlipY: false},
	}

	pixels := [][2]float64{{0, 0}, {100, 250}, {-30, 12.5}, {2048, 1536}}

	for _, plan := range plans {
		for _, p := range pixels {
			x, y := plan.PixelToWorld(p[0], p[1])
			gotX, gotY := plan.WorldToPixel(x, y)

			if math.Abs(gotX-p[0]) > 1e-9 || math.Abs(gotY-p[1]) > 1e-9 {
				t.Errorf("round trip of (%g, %g) with scale %g rotation %g gave (%g, %g)",
					p[0], p[1], plan.MetersPerPixel, plan.RotationDeg, gotX, gotY)
			}
		}
	}
}

// A calibrated plan must report the real length the user measured.
func TestSitePlanMeasuresKnownDistance(t *testing.T) {
	// 400 pixels were declared to span 100 metres.
	plan := SitePlan{MetersPerPixel: 100.0 / 400.0, FlipY: true}

	x1, y1 := plan.PixelToWorld(0, 0)
	x2, y2 := plan.PixelToWorld(400, 0)

	got := math.Hypot(x2-x1, y2-y1)
	if math.Abs(got-100) > 1e-9 {
		t.Fatalf("measured %g metres across the calibrated span, want 100", got)
	}
}

func TestSitePlanFlipY(t *testing.T) {
	// Image Y grows downward; with FlipY the world Y must grow upward.
	plan := SitePlan{MetersPerPixel: 1, FlipY: true}
	_, y := plan.PixelToWorld(0, 10)
	if y != -10 {
		t.Fatalf("with FlipY, 10 pixels down gave world y = %g, want -10", y)
	}

	plan.FlipY = false
	_, y = plan.PixelToWorld(0, 10)
	if y != 10 {
		t.Fatalf("without FlipY, 10 pixels down gave world y = %g, want 10", y)
	}
}

func TestSitePlanZeroScaleDoesNotDivideByZero(t *testing.T) {
	plan := SitePlan{MetersPerPixel: 0, OriginPxX: 5, OriginPxY: 7}
	px, py := plan.WorldToPixel(100, 100)
	if px != 5 || py != 7 {
		t.Fatalf("WorldToPixel with a zero scale = (%g, %g), want the origin (5, 7)", px, py)
	}
}
