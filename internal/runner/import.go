package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/importer"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
)

// ImportAnimation converts an uploaded animation into a viewable run.
//
// It does not go through the simulation queue. There is no engine involved and
// no model to execute: the work is a bounded file conversion, so it runs
// in-process rather than paying for a subprocess and a worker slot. It does
// use the same run row and the same event stream, because to everyone
// downstream an imported animation and a simulated run are the same thing.
func (d *Dispatcher) ImportAnimation(ctx context.Context, scenario *model.Scenario,
	source io.Reader, userID string) (*model.Run, error) {

	run := &model.Run{
		ProjectID:  scenario.ProjectID,
		ScenarioID: scenario.ID,
		Status:     model.RunActive,
		CreatedBy:  userID,
	}
	if err := d.store.Runs.Create(ctx, run); err != nil {
		return nil, err
	}

	relDir := RunDirFor(run.ID)
	absDir := filepath.Join(d.dataDir, filepath.FromSlash(relDir))
	run.ArtifactDir = filepath.ToSlash(relDir)

	d.events.Publish(Event{
		Type: EventStarted, RunID: run.ID, ScenarioID: scenario.ID,
		ProjectID: scenario.ProjectID, Status: string(model.RunActive),
	})

	started := time.Now()
	result, err := importer.Import(source, runstore.Dir(absDir), run.ID, runstore.BuildOptions{
		Log: d.log,
		Progress: func(fraction float64, stage string) {
			d.events.Publish(Event{
				Type: EventProgress, RunID: run.ID, ScenarioID: scenario.ID,
				ProjectID: scenario.ProjectID, Status: string(model.RunBuilding),
				Progress: fraction,
			})
		},
	})

	run.DurationMS = time.Since(started).Milliseconds()

	if err != nil {
		run.Status = model.RunFailed
		run.Error = err.Error()

		// A failed import leaves a half-written directory behind, which would
		// otherwise accumulate on every bad upload.
		if rmErr := os.RemoveAll(absDir); rmErr != nil {
			d.log.Warn("could not clean up a failed import", "dir", absDir, "error", rmErr)
		}
		run.ArtifactDir = ""
	} else {
		run.Status = model.RunDone
		run.Progress = 1
		run.SimTime = result.Duration
		run.EntityCount = uint64(result.Objects)
		run.RecordCount = uint64(result.Transitions)
		run.EngineVersion = "import/1"
		run.Warnings = marshalWarnings(result.Warnings)

		if manifest, mErr := runstore.Dir(absDir).ReadManifest(); mErr == nil {
			run.ArtifactBytes = manifest.Counts.TotalBytes
		}
	}

	if finishErr := d.store.Runs.Finish(ctx, run); finishErr != nil {
		return run, fmt.Errorf("record the import: %w", finishErr)
	}

	if run.Status == model.RunDone {
		if kpiErr := d.saveKPIs(ctx, run.ID, absDir); kpiErr != nil {
			d.log.Warn("could not store the imported run's KPIs", "run", run.ID, "error", kpiErr)
		}
	}

	d.events.Publish(Event{
		Type: EventFinished, RunID: run.ID, ScenarioID: scenario.ID,
		ProjectID: scenario.ProjectID, Status: string(run.Status),
		Progress: run.Progress, Error: run.Error,
	})

	if err != nil {
		return run, err
	}
	return run, nil
}
