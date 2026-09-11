package api

import (
	"testing"

	"github.com/andreybaranovskiy/simulation/internal/model"
)

func TestFilenameForIsSafe(t *testing.T) {
	cases := map[string]string{
		"Third inspection bay vs. two": "Third-inspection-bay-vs-two.pdf",
		"  Terminal / baseline  ":      "Terminal-baseline.pdf",
		"résumé":                       "rsum.pdf", // non-ASCII letters are dropped, not transliterated
		"":                             "report.pdf",
		"***":                          "report.pdf",
		"a-b_c":                        "a-b-c.pdf", // space, dash and underscore all become a single dash
	}
	for input, want := range cases {
		if got := filenameFor(input); got != want {
			t.Errorf("filenameFor(%q) = %q, want %q", input, got, want)
		}
	}
}

// An empty selection means every section the kind supports, and the result must
// come back in the canonical print order regardless of the order requested.
func TestCleanSectionsFillsAndOrders(t *testing.T) {
	full := cleanSections(model.ReportScenario, nil)
	if len(full) != len(model.DefaultSections(model.ReportScenario)) {
		t.Errorf("an empty selection gave %d sections, want the full default set", len(full))
	}

	// Requested out of order, with an unknown and a duplicate mixed in.
	got := cleanSections(model.ReportScenario, []string{
		model.SectionKPIs, "not-a-section", model.SectionSummary, model.SectionKPIs,
	})
	want := []string{model.SectionSummary, model.SectionKPIs}
	if len(got) != len(want) {
		t.Fatalf("cleanSections returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cleanSections[%d] = %q, want %q (order should be canonical)", i, got[i], want[i])
		}
	}
}

// A comparison-only section must not survive on a scenario report.
func TestCleanSectionsRejectsWrongKind(t *testing.T) {
	got := cleanSections(model.ReportScenario, []string{model.SectionDifference, model.SectionKPIs})
	for _, section := range got {
		if section == model.SectionDifference {
			t.Error("the difference section is comparison-only and should not appear on a scenario report")
		}
	}
	if len(got) != 1 || got[0] != model.SectionKPIs {
		t.Errorf("cleanSections kept %v, want just the kpis section", got)
	}
}

func TestDedupeStringsKeepsFirstOrder(t *testing.T) {
	got := dedupeStrings([]string{" a ", "b", "a", "", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupeStrings gave %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dedupeStrings[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
