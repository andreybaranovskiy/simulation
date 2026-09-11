package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type ScenarioStore struct{ db *db.DB }

const scenarioColumns = `id, project_id, model_id, name, description, params,
	seed, replications, site_plan_id, sort_order, archived_at,
	created_by, created_at, updated_at`

func (s *ScenarioStore) Create(ctx context.Context, sc *model.Scenario) error {
	if sc.ID == "" {
		sc.ID = NewID()
	}
	ts := now()
	sc.CreatedAt, sc.UpdatedAt = ts, ts
	if sc.Replications < 1 {
		sc.Replications = 1
	}
	if len(sc.Params) == 0 {
		sc.Params = []byte("{}")
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scenarios (`+scenarioColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, sc.ProjectID, sc.ModelID, sc.Name, sc.Description, []byte(sc.Params),
		nullOrUint64(sc.Seed), sc.Replications, nullOrString(sc.SitePlanID),
		sc.SortOrder, nil, sc.CreatedBy, sc.CreatedAt, sc.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create scenario: %w", mapErr(err))
	}
	return nil
}

func (s *ScenarioStore) ByID(ctx context.Context, id string) (*model.Scenario, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+scenarioColumns+` FROM scenarios WHERE id = ?`, id)
	var sc model.Scenario
	if err := scanScenario(row, &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

// List returns a project's scenarios with the model name, a run count and the
// most recent run attached.
//
// The latest run is joined rather than fetched per row because the scenario
// list is the page a user sits on while runs are in flight, and one query per
// scenario would make it slower exactly when it is being watched.
func (s *ScenarioStore) List(ctx context.Context, projectID string, includeArchived bool) ([]model.Scenario, error) {
	query := `
SELECT ` + prefixed("s", scenarioColumns) + `,
       m.name,
       (SELECT COUNT(*) FROM runs r WHERE r.scenario_id = s.id),
       latest.id, latest.status, latest.progress, latest.replication, latest.seed,
       latest.queued_at, latest.started_at, latest.finished_at,
       latest.duration_ms, latest.error,
       latest.entity_count, latest.sim_time
FROM scenarios s
JOIN models m ON m.id = s.model_id
LEFT JOIN runs latest ON latest.id = (
    SELECT r2.id FROM runs r2
    WHERE r2.scenario_id = s.id
    ORDER BY r2.queued_at DESC, r2.id DESC
    LIMIT 1
)
WHERE s.project_id = ?`

	if !includeArchived {
		query += ` AND s.archived_at IS NULL`
	}
	query += ` ORDER BY s.sort_order, s.created_at`

	rows, err := s.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("list scenarios: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.Scenario{}
	for rows.Next() {
		var sc model.Scenario
		var params []byte
		var seed sql.NullInt64
		var planID sql.NullString
		var archived sql.NullTime

		var runID, runStatus, runError sql.NullString
		var runProgress sql.NullFloat64
		var runReplication sql.NullInt64
		var runSeed sql.NullInt64
		var runQueued, runStarted, runFinished sql.NullTime
		var runDuration, runEntities sql.NullInt64
		var runSimTime sql.NullFloat64

		err := rows.Scan(
			&sc.ID, &sc.ProjectID, &sc.ModelID, &sc.Name, &sc.Description, &params,
			&seed, &sc.Replications, &planID, &sc.SortOrder, &archived,
			&sc.CreatedBy, &sc.CreatedAt, &sc.UpdatedAt,
			&sc.ModelName, &sc.RunCount,
			&runID, &runStatus, &runProgress, &runReplication, &runSeed,
			&runQueued, &runStarted, &runFinished, &runDuration, &runError,
			&runEntities, &runSimTime)
		if err != nil {
			return nil, fmt.Errorf("scan scenario: %w", mapErr(err))
		}

		applyScenarioScan(&sc, params, seed, planID, archived)

		if runID.Valid {
			sc.LatestRun = &model.Run{
				ID:           runID.String,
				ProjectID:    sc.ProjectID,
				ScenarioID:   sc.ID,
				Status:       model.RunStatus(runStatus.String),
				Progress:     runProgress.Float64,
				Replication:  int(runReplication.Int64),
				Seed:         uint64(runSeed.Int64),
				QueuedAt:     runQueued.Time.UTC(),
				StartedAt:    timePtr(runStarted),
				FinishedAt:   timePtr(runFinished),
				DurationMS:   runDuration.Int64,
				EntityCount:  uint64(runEntities.Int64),
				SimTime:      runSimTime.Float64,
				Error:        runError.String,
				ScenarioName: sc.Name,
			}
		}

		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *ScenarioStore) Update(ctx context.Context, sc *model.Scenario) error {
	sc.UpdatedAt = now()
	if sc.Replications < 1 {
		sc.Replications = 1
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE scenarios
		 SET name = ?, description = ?, params = ?, seed = ?, replications = ?,
		     site_plan_id = ?, sort_order = ?, updated_at = ?
		 WHERE id = ?`,
		sc.Name, sc.Description, []byte(sc.Params), nullOrUint64(sc.Seed),
		sc.Replications, nullOrString(sc.SitePlanID), sc.SortOrder, sc.UpdatedAt, sc.ID)
	return affectedOne(res, err, "update scenario")
}

func (s *ScenarioStore) SetArchived(ctx context.Context, id string, archived bool) error {
	var at any
	if archived {
		at = now()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE scenarios SET archived_at = ?, updated_at = ? WHERE id = ?`,
		at, now(), id)
	return affectedOne(res, err, "archive scenario")
}

func (s *ScenarioStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM scenarios WHERE id = ?`, id)
	return affectedOne(res, err, "delete scenario")
}

// Duplicate copies a scenario, which is how a comparison set is built: start
// from one that works and change a single parameter.
func (s *ScenarioStore) Duplicate(ctx context.Context, id, name, userID string) (*model.Scenario, error) {
	src, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}

	copied := *src
	copied.ID = ""
	copied.Name = name
	copied.CreatedBy = userID
	copied.ArchivedAt = nil
	copied.SortOrder = src.SortOrder + 1

	if err := s.Create(ctx, &copied); err != nil {
		return nil, err
	}
	return &copied, nil
}

func scanScenario(sc scanner, out *model.Scenario) error {
	var params []byte
	var seed sql.NullInt64
	var planID sql.NullString
	var archived sql.NullTime

	err := sc.Scan(&out.ID, &out.ProjectID, &out.ModelID, &out.Name, &out.Description,
		&params, &seed, &out.Replications, &planID, &out.SortOrder, &archived,
		&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return fmt.Errorf("scan scenario: %w", mapErr(err))
	}

	applyScenarioScan(out, params, seed, planID, archived)
	return nil
}

func applyScenarioScan(sc *model.Scenario, params []byte, seed sql.NullInt64, planID sql.NullString, archived sql.NullTime) {
	if len(params) > 0 {
		sc.Params = append([]byte(nil), params...)
	} else {
		sc.Params = []byte("{}")
	}

	if seed.Valid {
		// The column is unsigned but the driver hands back a signed value, so
		// the round trip has to go through the same width to survive a seed
		// above the signed maximum.
		v := uint64(seed.Int64)
		sc.Seed = &v
	}

	sc.SitePlanID = strPtr(planID)
	sc.ArchivedAt = timePtr(archived)
	sc.CreatedAt, sc.UpdatedAt = sc.CreatedAt.UTC(), sc.UpdatedAt.UTC()
}

func nullOrUint64(p *uint64) any {
	if p == nil {
		return nil
	}
	return *p
}
