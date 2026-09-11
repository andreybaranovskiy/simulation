package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/analytics"
	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type RunStore struct{ db *db.DB }

const runColumns = `id, project_id, scenario_id, replication, seed, status,
	queued_at, started_at, finished_at, progress, sim_time, entity_count,
	record_count, duration_ms, artifact_bytes, engine_version, artifact_dir,
	error, warnings, created_by`

func (s *RunStore) Create(ctx context.Context, r *model.Run) error {
	if r.ID == "" {
		r.ID = NewID()
	}
	r.QueuedAt = now()
	if r.Status == "" {
		r.Status = model.RunQueued
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (`+runColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ProjectID, r.ScenarioID, r.Replication, r.Seed, r.Status,
		r.QueuedAt, nil, nil, 0, 0, 0, 0, 0, 0, r.EngineVersion, r.ArtifactDir,
		nil, nil, r.CreatedBy)
	if err != nil {
		return fmt.Errorf("create run: %w", mapErr(err))
	}
	return nil
}

func (s *RunStore) ByID(ctx context.Context, id string) (*model.Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id)
	var r model.Run
	if err := scanRun(row, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListForScenario returns a scenario's runs, newest first.
func (s *RunStore) ListForScenario(ctx context.Context, scenarioID string) ([]model.Run, error) {
	return s.list(ctx, `SELECT `+runColumns+` FROM runs WHERE scenario_id = ?
	                    ORDER BY queued_at DESC, id DESC`, scenarioID)
}

// ListForProject returns a project's runs, newest first, with scenario names.
func (s *RunStore) ListForProject(ctx context.Context, projectID string, limit int) ([]model.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := `
SELECT ` + prefixed("r", runColumns) + `, s.name
FROM runs r
JOIN scenarios s ON s.id = r.scenario_id
WHERE r.project_id = ?
ORDER BY r.queued_at DESC, r.id DESC
LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.Run{}
	for rows.Next() {
		var r model.Run
		if err := scanRunWithScenario(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *RunStore) list(ctx context.Context, query string, args ...any) ([]model.Run, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.Run{}
	for rows.Next() {
		var r model.Run
		if err := scanRun(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClaimNext takes the oldest queued run and marks it running, atomically.
//
// The update is conditional on the row still being queued, so two dispatchers
// racing for the same run cannot both win: the second one's update matches
// nothing and it moves on.
func (s *RunStore) ClaimNext(ctx context.Context) (*model.Run, error) {
	for {
		var id string
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM runs WHERE status = 'queued' ORDER BY queued_at LIMIT 1`).Scan(&id)
		if err != nil {
			return nil, mapErr(err)
		}

		res, err := s.db.ExecContext(ctx,
			`UPDATE runs SET status = 'running', started_at = ? WHERE id = ? AND status = 'queued'`,
			now(), id)
		if err != nil {
			return nil, fmt.Errorf("claim run: %w", mapErr(err))
		}

		affected, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 0 {
			// Someone else claimed it between the select and the update. Try
			// the next one rather than failing the dispatcher.
			continue
		}

		return s.ByID(ctx, id)
	}
}

// UpdateProgress records a run's position. It is called frequently while a run
// is in flight, so it writes only the columns that change.
func (s *RunStore) UpdateProgress(ctx context.Context, id string, status model.RunStatus,
	progress, simTime float64, entities, records uint64) error {

	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, progress = ?, sim_time = ?, entity_count = ?, record_count = ?
		 WHERE id = ? AND status IN ('running','building')`,
		status, progress, simTime, entities, records, id)
	if err != nil {
		return fmt.Errorf("update run progress: %w", mapErr(err))
	}
	return nil
}

// Finish records a terminal state.
func (s *RunStore) Finish(ctx context.Context, r *model.Run) error {
	finished := now()
	r.FinishedAt = &finished

	var warnings any
	if len(r.Warnings) > 0 {
		warnings = []byte(r.Warnings)
	}

	var errText any
	if r.Error != "" {
		errText = r.Error
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE runs
		 SET status = ?, finished_at = ?, progress = ?, sim_time = ?,
		     entity_count = ?, record_count = ?, duration_ms = ?, artifact_bytes = ?,
		     engine_version = ?, artifact_dir = ?, error = ?, warnings = ?
		 WHERE id = ?`,
		r.Status, finished, r.Progress, r.SimTime, r.EntityCount, r.RecordCount,
		r.DurationMS, r.ArtifactBytes, r.EngineVersion, r.ArtifactDir,
		errText, warnings, r.ID)
	return affectedOne(res, err, "finish run")
}

// Cancel marks a run canceled if it has not already finished.
func (s *RunStore) Cancel(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'canceled', finished_at = ?
		 WHERE id = ? AND status IN ('queued','running','building')`,
		now(), id)
	if err != nil {
		return false, fmt.Errorf("cancel run: %w", mapErr(err))
	}
	affected, err := res.RowsAffected()
	return affected > 0, err
}

// ReclaimOrphans marks runs that were in flight when the server stopped as
// failed.
//
// Without this a crash leaves rows stuck reporting progress forever, and the
// user has no way to tell a hung run from a lost one.
func (s *RunStore) ReclaimOrphans(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs
		 SET status = 'failed', finished_at = ?,
		     error = 'The server stopped while this run was in progress.'
		 WHERE status IN ('running','building')`,
		now())
	if err != nil {
		return 0, fmt.Errorf("reclaim orphaned runs: %w", mapErr(err))
	}
	return res.RowsAffected()
}

func (s *RunStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, id)
	return affectedOne(res, err, "delete run")
}

// CountInFlight reports how many runs are occupying a worker slot, which is
// what the dispatcher checks before starting another.
func (s *RunStore) CountInFlight(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE status IN ('running','building')`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count runs in flight: %w", mapErr(err))
	}
	return n, nil
}

// SaveKPIs mirrors a run's KPIs into rows. The aggregate file on disk stays
// the source of truth; these rows exist so comparing ten scenarios is one
// indexed query rather than ten file reads.
func (s *RunStore) SaveKPIs(ctx context.Context, runID string, kpis []analytics.KPI) error {
	if len(kpis) == 0 {
		return nil
	}

	return s.db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_kpis WHERE run_id = ?`, runID); err != nil {
			return err
		}

		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO run_kpis
			 (run_id, kpi_key, value, label, unit, kpi_group, better, decimals, headline, resource_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, k := range kpis {
			_, err := stmt.ExecContext(ctx, runID, k.Key, k.Value, k.Label, k.Unit,
				k.Group, k.Better, k.Decimals, k.Headline, k.ResourceID)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// KPIsFor returns one run's KPIs.
func (s *RunStore) KPIsFor(ctx context.Context, runID string) ([]model.RunKPI, error) {
	return s.kpiQuery(ctx,
		`SELECT run_id, kpi_key, value, label, unit, kpi_group, better, decimals, headline, resource_id
		 FROM run_kpis WHERE run_id = ? ORDER BY kpi_group, kpi_key`, runID)
}

// KPIsForRuns returns KPIs across several runs in one query, which is what the
// comparison view is built on.
func (s *RunStore) KPIsForRuns(ctx context.Context, runIDs []string) ([]model.RunKPI, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}

	placeholders := ""
	args := make([]any, 0, len(runIDs))
	for i, id := range runIDs {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args = append(args, id)
	}

	return s.kpiQuery(ctx,
		`SELECT run_id, kpi_key, value, label, unit, kpi_group, better, decimals, headline, resource_id
		 FROM run_kpis WHERE run_id IN (`+placeholders+`)
		 ORDER BY kpi_group, kpi_key, run_id`, args...)
}

func (s *RunStore) kpiQuery(ctx context.Context, query string, args ...any) ([]model.RunKPI, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read run KPIs: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.RunKPI{}
	for rows.Next() {
		var k model.RunKPI
		err := rows.Scan(&k.RunID, &k.Key, &k.Value, &k.Label, &k.Unit,
			&k.Group, &k.Better, &k.Decimals, &k.Headline, &k.ResourceID)
		if err != nil {
			return nil, fmt.Errorf("scan run KPI: %w", mapErr(err))
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func scanRun(sc scanner, r *model.Run) error {
	var started, finished sql.NullTime
	var errText sql.NullString
	var warnings []byte
	var seed int64

	err := sc.Scan(&r.ID, &r.ProjectID, &r.ScenarioID, &r.Replication, &seed, &r.Status,
		&r.QueuedAt, &started, &finished, &r.Progress, &r.SimTime, &r.EntityCount,
		&r.RecordCount, &r.DurationMS, &r.ArtifactBytes, &r.EngineVersion, &r.ArtifactDir,
		&errText, &warnings, &r.CreatedBy)
	if err != nil {
		return fmt.Errorf("scan run: %w", mapErr(err))
	}

	applyRunScan(r, seed, started, finished, errText, warnings)
	return nil
}

func scanRunWithScenario(sc scanner, r *model.Run) error {
	var started, finished sql.NullTime
	var errText sql.NullString
	var warnings []byte
	var seed int64

	err := sc.Scan(&r.ID, &r.ProjectID, &r.ScenarioID, &r.Replication, &seed, &r.Status,
		&r.QueuedAt, &started, &finished, &r.Progress, &r.SimTime, &r.EntityCount,
		&r.RecordCount, &r.DurationMS, &r.ArtifactBytes, &r.EngineVersion, &r.ArtifactDir,
		&errText, &warnings, &r.CreatedBy, &r.ScenarioName)
	if err != nil {
		return fmt.Errorf("scan run: %w", mapErr(err))
	}

	applyRunScan(r, seed, started, finished, errText, warnings)
	return nil
}

func applyRunScan(r *model.Run, seed int64, started, finished sql.NullTime,
	errText sql.NullString, warnings []byte) {

	// The seed column is unsigned but the driver returns a signed value, so
	// the conversion has to be explicit for a seed above the signed maximum.
	r.Seed = uint64(seed)
	r.StartedAt = timePtr(started)
	r.FinishedAt = timePtr(finished)
	r.Error = errText.String
	r.QueuedAt = r.QueuedAt.UTC()

	if len(warnings) > 0 {
		r.Warnings = append([]byte(nil), warnings...)
	}
}

// MarshalWarnings is a small helper for callers building a warnings list.
func MarshalWarnings(warnings []string) json.RawMessage {
	if len(warnings) == 0 {
		return nil
	}
	data, err := json.Marshal(warnings)
	if err != nil {
		return nil
	}
	return data
}
