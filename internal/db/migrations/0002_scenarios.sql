-- Phase 2: models, scenarios, runs and the KPIs comparison joins on.
--
-- Bulk run data never lands here. A run's event trace, playback chunks and
-- aggregate files live on disk; this schema stores what a run was, how it
-- went, and the handful of numbers a dashboard sorts and compares by.

CREATE TABLE models (
    id           CHAR(36)     NOT NULL,
    project_id   CHAR(36)     NOT NULL,
    name         VARCHAR(160) NOT NULL,
    description  TEXT         NOT NULL,

    -- How the model is defined. A template is parameters over a built-in
    -- generator; a spec is a declarative model; go_source is user-supplied Go
    -- compiled server-side; animation is a pre-computed playback with no
    -- engine involved.
    source       ENUM('template','spec','go_source','animation') NOT NULL,
    template_key VARCHAR(80)  NOT NULL DEFAULT '',
    domain       VARCHAR(40)  NOT NULL DEFAULT 'generic',

    -- spec holds the resolved model for source='spec'. For a template it is
    -- null, because the generator plus the scenario's parameters reproduce it
    -- exactly and storing a copy would let the two drift apart.
    spec       JSON     NULL,
    -- asset_id points at the uploaded file behind a go_source or animation
    -- model.
    asset_id   CHAR(36) NULL,

    version    INT         NOT NULL DEFAULT 1,
    created_by CHAR(36)    NOT NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,

    PRIMARY KEY (id),
    KEY ix_models_project (project_id, updated_at),
    CONSTRAINT fk_models_project FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE,
    CONSTRAINT fk_models_asset   FOREIGN KEY (asset_id)   REFERENCES assets (id)   ON DELETE SET NULL,
    CONSTRAINT fk_models_user    FOREIGN KEY (created_by) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- A scenario is a model plus parameter values. Keeping it to parameters
-- rather than an edited copy of the model is what makes two scenarios
-- comparable: any difference between them is a difference somebody chose.
CREATE TABLE scenarios (
    id          CHAR(36)     NOT NULL,
    project_id  CHAR(36)     NOT NULL,
    model_id    CHAR(36)     NOT NULL,
    name        VARCHAR(160) NOT NULL,
    description TEXT         NOT NULL,

    params JSON NOT NULL,

    -- seed is null for a seed drawn at run time. Setting it makes a run
    -- reproducible.
    seed         BIGINT UNSIGNED NULL,
    replications INT             NOT NULL DEFAULT 1,

    -- site_plan_id ties the scenario to the georeferenced drawing it should be
    -- viewed over.
    site_plan_id CHAR(36) NULL,

    -- Ordering for the scenario list, so a user can arrange a comparison set.
    sort_order INT         NOT NULL DEFAULT 0,
    archived_at DATETIME(3) NULL,
    created_by CHAR(36)    NOT NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,

    PRIMARY KEY (id),
    KEY ix_scenarios_project (project_id, sort_order, created_at),
    KEY ix_scenarios_model (model_id),
    CONSTRAINT fk_scenarios_project FOREIGN KEY (project_id)   REFERENCES projects (id)   ON DELETE CASCADE,
    CONSTRAINT fk_scenarios_model   FOREIGN KEY (model_id)     REFERENCES models (id)     ON DELETE CASCADE,
    CONSTRAINT fk_scenarios_plan    FOREIGN KEY (site_plan_id) REFERENCES site_plans (id) ON DELETE SET NULL,
    CONSTRAINT fk_scenarios_user    FOREIGN KEY (created_by)   REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE runs (
    id          CHAR(36) NOT NULL,
    project_id  CHAR(36) NOT NULL,
    scenario_id CHAR(36) NOT NULL,

    -- replication distinguishes repeated runs of one scenario. Each gets a
    -- different seed derived from the scenario's, so the set is reproducible
    -- as a whole.
    replication INT             NOT NULL DEFAULT 0,
    seed        BIGINT UNSIGNED NOT NULL,

    -- building is its own state because turning a trace into playback chunks
    -- takes real time on a long run, and a user watching should see which
    -- half of the work is happening.
    status ENUM('queued','running','building','done','failed','canceled') NOT NULL DEFAULT 'queued',

    queued_at   DATETIME(3) NOT NULL,
    started_at  DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,

    -- Live progress, updated while a run is in flight.
    progress     DOUBLE          NOT NULL DEFAULT 0,
    sim_time     DOUBLE          NOT NULL DEFAULT 0,
    entity_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    record_count BIGINT UNSIGNED NOT NULL DEFAULT 0,

    duration_ms    BIGINT      NOT NULL DEFAULT 0,
    artifact_bytes BIGINT      NOT NULL DEFAULT 0,
    engine_version VARCHAR(40) NOT NULL DEFAULT '',

    -- artifact_dir is relative to the server's data directory, so moving the
    -- data volume does not invalidate every row.
    artifact_dir VARCHAR(255) NOT NULL DEFAULT '',

    error TEXT NULL,
    -- warnings carries anything that changes how the results should be read,
    -- such as a run that hit its entity limit.
    warnings JSON NULL,

    created_by CHAR(36) NOT NULL,

    PRIMARY KEY (id),
    KEY ix_runs_scenario (scenario_id, replication),
    KEY ix_runs_project (project_id, queued_at),
    -- The dispatcher polls for queued work; this index is what keeps that poll
    -- from scanning the table.
    KEY ix_runs_status (status, queued_at),
    CONSTRAINT fk_runs_project  FOREIGN KEY (project_id)  REFERENCES projects (id)  ON DELETE CASCADE,
    CONSTRAINT fk_runs_scenario FOREIGN KEY (scenario_id) REFERENCES scenarios (id) ON DELETE CASCADE,
    CONSTRAINT fk_runs_user     FOREIGN KEY (created_by)  REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- KPIs are mirrored out of the run's aggregate file into rows, so comparing
-- ten scenarios is one indexed query rather than ten file reads. The file on
-- disk stays the source of truth.
--
-- kpi_key and kpi_group avoid KEY and GROUP, which MySQL reserves.
CREATE TABLE run_kpis (
    run_id      CHAR(36)     NOT NULL,
    kpi_key     VARCHAR(160) NOT NULL,
    value       DOUBLE       NOT NULL,
    label       VARCHAR(200) NOT NULL,
    unit        VARCHAR(20)  NOT NULL DEFAULT '',
    kpi_group   VARCHAR(60)  NOT NULL DEFAULT '',
    better      VARCHAR(10)  NOT NULL DEFAULT '',
    decimals    TINYINT      NOT NULL DEFAULT 0,
    headline    TINYINT(1)   NOT NULL DEFAULT 0,
    resource_id VARCHAR(80)  NOT NULL DEFAULT '',

    PRIMARY KEY (run_id, kpi_key),
    -- Comparison reads one key across many runs, which is the opposite order
    -- to the primary key.
    KEY ix_run_kpis_key (kpi_key, value),
    CONSTRAINT fk_run_kpis_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
