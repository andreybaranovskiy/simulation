-- Phase 1: identity, projects, assets, site plans.
-- MySQL 8.0+. All tables InnoDB / utf8mb4.

CREATE TABLE users (
    id            CHAR(36)     NOT NULL,
    email         VARCHAR(320) NOT NULL,
    email_norm    VARCHAR(320) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    display_name  VARCHAR(120) NOT NULL,
    is_admin      TINYINT(1)   NOT NULL DEFAULT 0,
    can_upload_go TINYINT(1)   NOT NULL DEFAULT 0,
    is_active     TINYINT(1)   NOT NULL DEFAULT 1,
    created_at    DATETIME(3)  NOT NULL,
    updated_at    DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_users_email_norm (email_norm)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- id is the SHA-256 hex of the opaque session token; the raw token is never stored.
CREATE TABLE sessions (
    id           CHAR(64)     NOT NULL,
    user_id      CHAR(36)     NOT NULL,
    created_at   DATETIME(3)  NOT NULL,
    last_seen_at DATETIME(3)  NOT NULL,
    expires_at   DATETIME(3)  NOT NULL,
    user_agent   VARCHAR(255) NOT NULL DEFAULT '',
    ip           VARCHAR(45)  NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    KEY ix_sessions_user (user_id),
    KEY ix_sessions_expires (expires_at),
    CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE projects (
    id          CHAR(36)     NOT NULL,
    name        VARCHAR(160) NOT NULL,
    description TEXT         NOT NULL,
    owner_id    CHAR(36)     NOT NULL,
    archived_at DATETIME(3)  NULL,
    created_at  DATETIME(3)  NOT NULL,
    updated_at  DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY ix_projects_owner (owner_id),
    CONSTRAINT fk_projects_owner FOREIGN KEY (owner_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE project_members (
    project_id CHAR(36)                          NOT NULL,
    user_id    CHAR(36)                          NOT NULL,
    role       ENUM('owner','editor','viewer')   NOT NULL,
    created_at DATETIME(3)                       NOT NULL,
    PRIMARY KEY (project_id, user_id),
    KEY ix_project_members_user (user_id),
    CONSTRAINT fk_pm_project FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE,
    CONSTRAINT fk_pm_user    FOREIGN KEY (user_id)    REFERENCES users (id)    ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE assets (
    id            CHAR(36)     NOT NULL,
    project_id    CHAR(36)     NOT NULL,
    kind          ENUM('plan_image','model_3d','model_spec','animation_json','go_model','other') NOT NULL,
    original_name VARCHAR(255) NOT NULL,
    content_type  VARCHAR(160) NOT NULL,
    size_bytes    BIGINT UNSIGNED NOT NULL,
    sha256        CHAR(64)     NOT NULL,
    storage_path  VARCHAR(512) NOT NULL,
    meta          JSON         NULL,
    uploaded_by   CHAR(36)     NOT NULL,
    created_at    DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY ix_assets_project_kind (project_id, kind),
    KEY ix_assets_sha256 (sha256),
    CONSTRAINT fk_assets_project FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE,
    CONSTRAINT fk_assets_user    FOREIGN KEY (uploaded_by) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Georeference for a 2D plan image: pixels -> metres in world space.
CREATE TABLE site_plans (
    id               CHAR(36)     NOT NULL,
    project_id       CHAR(36)     NOT NULL,
    asset_id         CHAR(36)     NOT NULL,
    name             VARCHAR(160) NOT NULL,
    image_width      INT          NOT NULL,
    image_height     INT          NOT NULL,
    meters_per_pixel DOUBLE       NOT NULL DEFAULT 1,
    origin_px_x      DOUBLE       NOT NULL DEFAULT 0,
    origin_px_y      DOUBLE       NOT NULL DEFAULT 0,
    rotation_deg     DOUBLE       NOT NULL DEFAULT 0,
    flip_y           TINYINT(1)   NOT NULL DEFAULT 1,
    calibration      JSON         NULL,
    created_at       DATETIME(3)  NOT NULL,
    updated_at       DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY ix_site_plans_project (project_id),
    CONSTRAINT fk_site_plans_project FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE,
    CONSTRAINT fk_site_plans_asset   FOREIGN KEY (asset_id)   REFERENCES assets (id)   ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE audit_log (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    at          DATETIME(3)  NOT NULL,
    user_id     CHAR(36)     NULL,
    project_id  CHAR(36)     NULL,
    action      VARCHAR(80)  NOT NULL,
    target_kind VARCHAR(40)  NOT NULL DEFAULT '',
    target_id   VARCHAR(64)  NOT NULL DEFAULT '',
    detail      JSON         NULL,
    ip          VARCHAR(45)  NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    KEY ix_audit_at (at),
    KEY ix_audit_project (project_id, at),
    KEY ix_audit_user (user_id, at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
