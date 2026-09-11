-- Phase 6: saved report definitions.
--
-- A report row is a definition, not a rendered document: it says which
-- scenarios and which sections belong in a report, and the PDF is produced on
-- demand from whatever the current run data is. Storing the definition rather
-- than the output means a report reruns against fresh results instead of going
-- stale the moment a scenario is run again.

CREATE TABLE reports (
    id          CHAR(36)     NOT NULL,
    project_id  CHAR(36)     NOT NULL,
    name        VARCHAR(200) NOT NULL,
    subtitle    VARCHAR(400) NOT NULL DEFAULT '',

    -- scenario is one scenario in depth; comparison is several side by side.
    kind        ENUM('scenario','comparison') NOT NULL,

    -- scenario_ids is an ordered list, so the report controls the column order
    -- of a comparison. sections is the subset of blocks to include; an empty
    -- array means every block the kind supports.
    scenario_ids JSON NOT NULL,
    sections     JSON NOT NULL,

    created_by CHAR(36)    NOT NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,

    PRIMARY KEY (id),
    KEY ix_reports_project (project_id, updated_at),
    CONSTRAINT fk_reports_project FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE,
    CONSTRAINT fk_reports_user    FOREIGN KEY (created_by) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
