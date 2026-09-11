package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()

	if scenarioID := q.Get("scenarioId"); scenarioID != "" {
		sc, err := s.store.Scenarios.ByID(r.Context(), scenarioID)
		if err != nil {
			if isNotFound(err) {
				return notFound("That scenario")
			}
			return err
		}
		if sc.ProjectID != r.PathValue("projectID") {
			return notFound("That scenario")
		}

		runs, err := s.store.Runs.ListForScenario(r.Context(), scenarioID)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, runs)
		return nil
	}

	runs, err := s.store.Runs.ListForProject(r.Context(), r.PathValue("projectID"),
		queryInt(q.Get("limit"), 100, 1, 500))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, runs)
	return nil
}

// handleStartRun queues a scenario. It returns immediately with the run rows;
// progress arrives on the event stream.
func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.scenarioInProject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	if s.dispatcher == nil {
		return &APIError{
			Status: http.StatusServiceUnavailable, Code: "engine_unavailable",
			Message: "The simulation engine is not available on this server.",
		}
	}

	m, err := s.store.Models.ByID(ctx, sc.ModelID)
	if err != nil {
		return err
	}
	if !m.Source.Runnable() {
		return badRequest("An imported animation is played back rather than run.")
	}

	runs, err := s.dispatcher.Enqueue(ctx, sc, auth.UserID(ctx))
	if err != nil {
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), ProjectID: sc.ProjectID, Action: "run.start",
		TargetKind: "scenario", TargetID: sc.ID,
		Detail: map[string]int{"runs": len(runs)}, IP: s.clientIP(r),
	})

	writeJSON(w, http.StatusAccepted, runs)
	return nil
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) error {
	run, err := s.runInProject(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, run)
	return nil
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) error {
	run, err := s.runInProject(r)
	if err != nil {
		return err
	}

	if s.dispatcher == nil {
		return badRequest("The simulation engine is not available on this server.")
	}

	changed, err := s.dispatcher.Cancel(r.Context(), run.ID)
	if err != nil {
		return err
	}
	if !changed {
		return conflict("That run has already finished.")
	}

	writeNoContent(w)
	return nil
}

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) error {
	run, err := s.runInProject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	if !run.Status.Terminal() {
		return conflict("Cancel the run before deleting it.")
	}

	if err := s.store.Runs.Delete(ctx, run.ID); err != nil {
		return err
	}
	s.removeRunArtifacts(run)

	writeNoContent(w)
	return nil
}

func (s *Server) handleRunKPIs(w http.ResponseWriter, r *http.Request) error {
	run, err := s.runInProject(r)
	if err != nil {
		return err
	}

	kpis, err := s.store.Runs.KPIsFor(r.Context(), run.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, kpis)
	return nil
}

// handleRunArtifact streams a file out of a run's directory: the manifest, a
// playback chunk, or an aggregate.
//
// Serving these through the server rather than letting IIS reach the directory
// is what keeps a project's results private. Every request goes through the
// same membership check as the rest of the API.
func (s *Server) handleRunArtifact(w http.ResponseWriter, r *http.Request) error {
	run, err := s.runInProject(r)
	if err != nil {
		return err
	}

	if !run.Viewable() {
		return notFound("That run's results")
	}

	rel := r.PathValue("path")
	absPath, err := s.resolveArtifactPath(run, rel)
	if err != nil {
		return err
	}

	file, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return notFound("That file")
		}
		return internal(err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return internal(err)
	}
	if info.IsDir() {
		return notFound("That file")
	}

	w.Header().Set("Content-Type", artifactContentType(rel))

	// A finished run's artifacts never change, so they can be cached hard.
	// This is what lets a viewer scrub back and forth without refetching.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", fmt.Sprintf(`"%s-%d-%d"`, run.ID, info.Size(), info.ModTime().UnixNano()))

	// A chunk is deliberately NOT served as Content-Encoding: gzip. Only its
	// body is compressed; the magic bytes and header in front of it are plain,
	// so a browser told to inflate the response would fail on the first eight
	// bytes. The client decompresses the body itself after reading the header,
	// which is what lets it decide whether a chunk is worth inflating at all.
	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), file)
	return nil
}

// resolveArtifactPath maps a request path to a file inside a run's directory,
// refusing anything that would escape it.
func (s *Server) resolveArtifactPath(run *model.Run, rel string) (string, error) {
	if rel == "" {
		return "", badRequest("Name a file.")
	}

	clean := path.Clean("/" + rel)
	clean = strings.TrimPrefix(clean, "/")

	if clean == "" || clean == "." || strings.HasPrefix(clean, "..") {
		return "", notFound("That file")
	}

	// Only the files a viewer legitimately needs are reachable. The raw event
	// trace is deliberately not among them: it is an intermediate that can be
	// very large, and nothing in the browser reads it.
	if !allowedArtifact(clean) {
		return "", notFound("That file")
	}

	base := s.runArtifactDir(run)
	abs := filepath.Join(base, filepath.FromSlash(clean))

	if !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", notFound("That file")
	}
	return abs, nil
}

// allowedArtifact is an allow list rather than a deny list, so a new file
// dropped into a run directory is not served by accident.
func allowedArtifact(clean string) bool {
	switch clean {
	case runstore.ManifestFile, runstore.ResultFile, runstore.ModelFile:
		return true
	}

	if strings.HasPrefix(clean, runstore.AggDir+"/") && strings.HasSuffix(clean, ".json") {
		return true
	}
	if strings.HasPrefix(clean, runstore.FramesDir+"/") && strings.HasSuffix(clean, ".bin") {
		return true
	}
	return false
}

func artifactContentType(rel string) string {
	if strings.HasSuffix(rel, ".json") {
		return "application/json; charset=utf-8"
	}
	return "application/octet-stream"
}

func (s *Server) runArtifactDir(run *model.Run) string {
	dir := run.ArtifactDir
	if dir == "" {
		return ""
	}
	return filepath.Join(s.cfg.Storage.DataDir, filepath.FromSlash(dir))
}

// removeRunArtifacts deletes a run's directory. Failures are logged rather
// than surfaced: the database row is already gone, and a leftover directory is
// a disk-space problem rather than a correctness one.
func (s *Server) removeRunArtifacts(run *model.Run) {
	dir := s.runArtifactDir(run)
	if dir == "" {
		return
	}

	// A last guard against removing anything outside the data directory, since
	// this is the one place the server deletes a tree.
	base := s.cfg.Storage.DataDir
	if !strings.HasPrefix(dir, base+string(os.PathSeparator)) {
		s.log.Error("refusing to delete a run directory outside the data directory",
			"run", run.ID, "dir", dir)
		return
	}

	if err := os.RemoveAll(dir); err != nil {
		s.log.Warn("could not remove a run's artifacts", "run", run.ID, "dir", dir, "error", err)
	}
}

func (s *Server) runInProject(r *http.Request) (*model.Run, error) {
	run, err := s.store.Runs.ByID(r.Context(), r.PathValue("runID"))
	if err != nil {
		if isNotFound(err) {
			return nil, notFound("That run")
		}
		return nil, err
	}
	if run.ProjectID != r.PathValue("projectID") {
		return nil, notFound("That run")
	}
	return run, nil
}

// handleRunEvents streams live run progress as server-sent events.
//
// Server-sent events rather than websockets: the traffic is one-way, it
// reconnects on its own, and it passes through an IIS reverse proxy without
// the upgrade handshake a websocket needs.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")

	flusher, ok := w.(http.Flusher)
	if !ok {
		return internal(fmt.Errorf("the response writer cannot stream"))
	}
	if s.dispatcher == nil {
		return &APIError{
			Status: http.StatusServiceUnavailable, Code: "engine_unavailable",
			Message: "The simulation engine is not available on this server.",
		}
	}

	events, unsubscribe := s.dispatcher.Events().Subscribe(projectID)
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this an IIS or ARR buffer holds events until the response ends,
	// which for a stream is never.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// A subscriber joins mid-flight, so the current state of every unfinished
	// run is sent first. Without it a browser that connects a moment after a
	// run starts would show nothing until the next progress tick.
	s.sendRunSnapshot(w, flusher, projectID, r)

	// Keep-alive comments stop an idle connection being closed by a proxy.
	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return nil

		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := writeSSE(w, "run", event); err != nil {
				return nil
			}
			flusher.Flush()

		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

func (s *Server) sendRunSnapshot(w http.ResponseWriter, flusher http.Flusher, projectID string, r *http.Request) {
	runs, err := s.store.Runs.ListForProject(r.Context(), projectID, 50)
	if err != nil {
		s.log.Debug("could not send the run snapshot", "error", err)
		return
	}

	for _, run := range runs {
		if run.Status.Terminal() {
			continue
		}
		_ = writeSSE(w, "run", map[string]any{
			"type": "progress", "runId": run.ID, "scenarioId": run.ScenarioID,
			"projectId": run.ProjectID, "status": string(run.Status),
			"progress": run.Progress, "simTime": run.SimTime,
			"entities": run.EntityCount, "records": run.RecordCount,
		})
	}
	flusher.Flush()
}

func writeSSE(w http.ResponseWriter, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}
