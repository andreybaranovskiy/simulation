package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/report"
)

// maxReportScenarios bounds a comparison report, matching the comparison view
// it is printed from: past a handful of columns a table stops fitting a page.
const maxReportScenarios = 6

type reportRequest struct {
	Name        string   `json:"name"`
	Subtitle    string   `json:"subtitle"`
	Kind        string   `json:"kind"`
	ScenarioIDs []string `json:"scenarioIds"`
	Sections    []string `json:"sections"`
}

func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) error {
	reports, err := s.store.Reports.List(r.Context(), r.PathValue("projectID"))
	if err != nil {
		return err
	}

	// The list view names the scenarios a report is about, so fill them in
	// rather than making the client fetch each one to render a card.
	for i := range reports {
		reports[i].ScenarioNames = s.scenarioNames(r.Context(), reports[i].ScenarioIDs)
	}

	writeJSON(w, http.StatusOK, reports)
	return nil
}

func (s *Server) handleGetReport(w http.ResponseWriter, r *http.Request) error {
	rep, err := s.loadReport(r.Context(), r.PathValue("projectID"), r.PathValue("reportID"))
	if err != nil {
		return err
	}
	rep.ScenarioNames = s.scenarioNames(r.Context(), rep.ScenarioIDs)
	writeJSON(w, http.StatusOK, rep)
	return nil
}

func (s *Server) handleCreateReport(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	var req reportRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	rep, apiErr := s.buildReport(ctx, projectID, &req)
	if apiErr != nil {
		return apiErr
	}
	rep.CreatedBy = auth.UserID(ctx)

	if err := s.store.Reports.Create(ctx, rep); err != nil {
		return err
	}
	rep.ScenarioNames = s.scenarioNames(ctx, rep.ScenarioIDs)
	writeJSON(w, http.StatusCreated, rep)
	return nil
}

func (s *Server) handleUpdateReport(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	existing, err := s.loadReport(ctx, projectID, r.PathValue("reportID"))
	if err != nil {
		return err
	}

	var req reportRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	// The kind is fixed at creation: a scenario report and a comparison are
	// different documents, and changing one into the other is a new report,
	// not an edit.
	req.Kind = string(existing.Kind)

	rep, apiErr := s.buildReport(ctx, projectID, &req)
	if apiErr != nil {
		return apiErr
	}
	rep.ID = existing.ID

	if err := s.store.Reports.Update(ctx, rep); err != nil {
		return err
	}
	rep.CreatedBy = existing.CreatedBy
	rep.CreatedAt = existing.CreatedAt
	rep.ScenarioNames = s.scenarioNames(ctx, rep.ScenarioIDs)
	writeJSON(w, http.StatusOK, rep)
	return nil
}

func (s *Server) handleDeleteReport(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	if _, err := s.loadReport(ctx, projectID, r.PathValue("reportID")); err != nil {
		return err
	}
	if err := s.store.Reports.Delete(ctx, r.PathValue("reportID")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// handleExportReport renders a saved report to PDF.
func (s *Server) handleExportReport(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	rep, err := s.loadReport(ctx, projectID, r.PathValue("reportID"))
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/print/projects/%s/reports/%s", projectID, rep.ID)
	return s.streamPDF(w, r, rep.Kind, rep.Name, rep.Subtitle, path)
}

// handleExportAdhoc renders a report that was assembled in the builder but not
// saved, so a one-off export does not litter the project with definitions.
func (s *Server) handleExportAdhoc(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	var req reportRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	rep, apiErr := s.buildReport(ctx, projectID, &req)
	if apiErr != nil {
		return apiErr
	}

	// The whole definition travels in the URL, so the print page can render it
	// without a row to read back.
	query := url.Values{}
	query.Set("kind", string(rep.Kind))
	query.Set("scenarios", strings.Join(rep.ScenarioIDs, ","))
	query.Set("sections", strings.Join(rep.Sections, ","))
	query.Set("title", rep.Name)
	if rep.Subtitle != "" {
		query.Set("subtitle", rep.Subtitle)
	}

	path := fmt.Sprintf("/print/projects/%s/report?%s", projectID, query.Encode())
	return s.streamPDF(w, r, rep.Kind, rep.Name, rep.Subtitle, path)
}

// streamPDF is the shared tail of both export paths: it checks the renderer is
// available, mints a session for the caller, renders, and streams the file.
func (s *Server) streamPDF(w http.ResponseWriter, r *http.Request, kind model.ReportKind, name, subtitle, path string) error {
	if s.renderer == nil {
		return &APIError{
			Status:  http.StatusServiceUnavailable,
			Code:    "reporting_unavailable",
			Message: "PDF export is not available: no browser was found on the server.",
		}
	}

	ctx := r.Context()

	// The report is rendered as the caller, in their own short-lived session,
	// so it can only ever contain data they are allowed to see. The session is
	// closed as soon as the render returns.
	token, apiErr := s.mintReportSession(ctx, r)
	if apiErr != nil {
		return apiErr
	}
	defer func() {
		if err := s.auth.Logout(context.WithoutCancel(ctx), token); err != nil {
			s.log.Warn("close report session", "error", err)
		}
	}()

	footer := subtitle
	if footer == "" {
		footer = s.reportFooter(ctx, r.PathValue("projectID"))
	}

	pdf, err := s.renderer.Render(ctx, report.Job{
		Path:         path,
		SessionToken: token,
		Title:        name,
		Footer:       footer,
		// A comparison is wide; a single scenario reads down a page. The
		// orientation follows the document, not a setting.
		Landscape: kind == model.ReportComparison,
	})
	if err != nil {
		return internal(fmt.Errorf("render report: %w", err))
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filenameFor(name)))
	w.Header().Set("Content-Length", fmt.Sprint(len(pdf)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pdf)
	return nil
}

// mintReportSession opens a session for the current user, used only to carry
// the render's own authentication and torn down immediately after.
func (s *Server) mintReportSession(ctx context.Context, r *http.Request) (string, *APIError) {
	userID := auth.UserID(ctx)
	if userID == "" {
		return "", unauthorized("")
	}

	info, err := s.auth.OpenSession(ctx, userID, "report-export", s.clientIP(r))
	if err != nil {
		return "", internal(fmt.Errorf("open report session: %w", err))
	}
	return info.Token, nil
}

// buildReport validates a request into a report ready to store or render. It is
// the one place the rules live, so create, update and ad-hoc export agree.
func (s *Server) buildReport(ctx context.Context, projectID string, req *reportRequest) (*model.Report, *APIError) {
	fields := map[string]string{}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		fields["name"] = "A report needs a name."
	} else if len(name) > 200 {
		fields["name"] = "Keep the name under 200 characters."
	}

	kind := model.ReportKind(req.Kind)
	if kind != model.ReportScenario && kind != model.ReportComparison {
		fields["kind"] = "Choose a scenario or a comparison report."
	}

	ids := dedupeStrings(req.ScenarioIDs)
	switch {
	case kind == model.ReportScenario && len(ids) != 1:
		fields["scenarioIds"] = "A scenario report covers exactly one scenario."
	case kind == model.ReportComparison && len(ids) < 2:
		fields["scenarioIds"] = "A comparison needs at least two scenarios."
	case len(ids) > maxReportScenarios:
		fields["scenarioIds"] = fmt.Sprintf("A report covers at most %d scenarios.", maxReportScenarios)
	}

	if len(fields) > 0 {
		return nil, invalidFields(fields)
	}

	// Every scenario has to belong to this project, both to keep a report
	// inside its own project and so a caller cannot pull another project's
	// scenario into one by id.
	for _, id := range ids {
		scenario, err := s.store.Scenarios.ByID(ctx, id)
		if err != nil {
			if isNotFound(err) {
				return nil, badRequest("One of those scenarios does not exist.")
			}
			return nil, internal(err)
		}
		if scenario.ProjectID != projectID {
			return nil, badRequest("One of those scenarios is not in this project.")
		}
	}

	return &model.Report{
		ProjectID:   projectID,
		Name:        name,
		Subtitle:    strings.TrimSpace(req.Subtitle),
		Kind:        kind,
		ScenarioIDs: ids,
		Sections:    cleanSections(kind, req.Sections),
	}, nil
}

// loadReport fetches a report and confirms it belongs to the addressed project,
// so a report id from another project reads as not found rather than leaking.
func (s *Server) loadReport(ctx context.Context, projectID, reportID string) (*model.Report, error) {
	rep, err := s.store.Reports.ByID(ctx, reportID)
	if err != nil {
		if isNotFound(err) {
			return nil, notFound("That report")
		}
		return nil, err
	}
	if rep.ProjectID != projectID {
		return nil, notFound("That report")
	}
	return rep, nil
}

func (s *Server) scenarioNames(ctx context.Context, ids []string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if scenario, err := s.store.Scenarios.ByID(ctx, id); err == nil {
			names = append(names, scenario.Name)
		}
	}
	return names
}

// reportFooter is the default footer when a report has no subtitle: the project
// name and the date, which is what a printed page is expected to carry.
func (s *Server) reportFooter(ctx context.Context, projectID string) string {
	project, err := s.store.Projects.ByIDAdmin(ctx, projectID)
	if err != nil {
		return time.Now().Format("2 January 2006")
	}
	return fmt.Sprintf("%s · %s", project.Name, time.Now().Format("2 January 2006"))
}

// cleanSections keeps only the sections the kind supports and returns them in
// the canonical print order, not the order they were requested in, so a report
// prints its blocks in a stable sequence however the client sent them. An empty
// or fully invalid selection falls back to the full default set.
func cleanSections(kind model.ReportKind, requested []string) []string {
	wanted := map[string]bool{}
	for _, section := range requested {
		wanted[section] = true
	}

	var out []string
	for _, section := range model.DefaultSections(kind) {
		if wanted[section] {
			out = append(out, section)
		}
	}

	if len(out) == 0 {
		return model.DefaultSections(kind)
	}
	return out
}

func dedupeStrings(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// filenameFor turns a report name into a safe download filename.
func filenameFor(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}

	// Collapse runs of separators so "a / b" becomes "a-b", not "a--b".
	slug := strings.Trim(collapseDashes(b.String()), "-")
	if slug == "" {
		slug = "report"
	}
	return slug + ".pdf"
}

func collapseDashes(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if !prevDash {
				b.WriteRune(r)
			}
			prevDash = true
			continue
		}
		b.WriteRune(r)
		prevDash = false
	}
	return b.String()
}
