-- 0001_initial.sql — Tomovee catalog schema
-- Naming follows snake_case per the project conventions.
-- The schema_migrations table is created by the migration runner itself.

CREATE TABLE config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE catalog_entry (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type      TEXT NOT NULL CHECK (media_type IN ('movie', 'series')),
    title           TEXT NOT NULL,
    original_title  TEXT,
    release_year    INTEGER,
    overview        TEXT,
    runtime_minutes INTEGER,
    rating          REAL,
    vote_count      INTEGER,
    imdb_id         TEXT,
    tmdb_id         INTEGER,
    poster_path     TEXT,
    status          TEXT NOT NULL DEFAULT 'needs_lookup'
                    CHECK (status IN ('matched', 'needs_lookup', 'missing')),
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_catalog_entry_title  ON catalog_entry (title);
CREATE INDEX idx_catalog_entry_status ON catalog_entry (status);
CREATE UNIQUE INDEX ux_catalog_entry_imdb ON catalog_entry (imdb_id) WHERE imdb_id IS NOT NULL;
CREATE UNIQUE INDEX ux_catalog_entry_tmdb ON catalog_entry (tmdb_id) WHERE tmdb_id IS NOT NULL;

CREATE TABLE genre (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE catalog_entry_genre (
    catalog_entry_id INTEGER NOT NULL REFERENCES catalog_entry(id) ON DELETE CASCADE,
    genre_id         INTEGER NOT NULL REFERENCES genre(id) ON DELETE CASCADE,
    PRIMARY KEY (catalog_entry_id, genre_id)
);

CREATE TABLE series_metadata (
    catalog_entry_id INTEGER PRIMARY KEY REFERENCES catalog_entry(id) ON DELETE CASCADE,
    first_air_date   TEXT,
    last_air_date    TEXT,
    num_seasons      INTEGER,
    num_episodes     INTEGER
);

CREATE TABLE episode (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_entry_id INTEGER NOT NULL REFERENCES catalog_entry(id) ON DELETE CASCADE,
    season_number    INTEGER,
    episode_number   INTEGER,
    title            TEXT,
    overview         TEXT,
    airdate          TEXT,
    is_special       INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT 'needs_lookup'
                     CHECK (status IN ('matched', 'needs_lookup', 'missing')),
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_episode_catalog ON episode (catalog_entry_id);

CREATE TABLE version (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_entry_id  INTEGER REFERENCES catalog_entry(id) ON DELETE CASCADE,
    episode_id        INTEGER REFERENCES episode(id) ON DELETE CASCADE,
    file_path         TEXT NOT NULL,
    size_bytes        INTEGER,
    mtime             TEXT,
    duration_seconds  REAL,
    container         TEXT,
    resolution_width  INTEGER,
    resolution_height INTEGER,
    resolution_label  TEXT,
    video_codec       TEXT,
    hdr               INTEGER,
    frame_rate        REAL,
    bit_depth         INTEGER,
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK ((catalog_entry_id IS NOT NULL) OR (episode_id IS NOT NULL))
);

CREATE UNIQUE INDEX ux_version_file_path ON version (file_path);
CREATE INDEX idx_version_catalog ON version (catalog_entry_id);
CREATE INDEX idx_version_episode ON version (episode_id);

CREATE TABLE audio_track (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id INTEGER NOT NULL REFERENCES version(id) ON DELETE CASCADE,
    language   TEXT,
    codec      TEXT,
    channels   INTEGER,
    source     TEXT NOT NULL DEFAULT 'ffprobe'
);

CREATE INDEX idx_audio_track_version ON audio_track (version_id);

CREATE TABLE subtitle_track (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id INTEGER NOT NULL REFERENCES version(id) ON DELETE CASCADE,
    language   TEXT,
    format     TEXT,
    source     TEXT NOT NULL DEFAULT 'ffprobe'
);

CREATE INDEX idx_subtitle_track_version ON subtitle_track (version_id);

CREATE TABLE lookup_cache (
    kind       TEXT NOT NULL,
    key        TEXT NOT NULL,
    payload    TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (kind, key)
);

CREATE TABLE watch_folder (
    path      TEXT PRIMARY KEY,
    enabled   INTEGER NOT NULL DEFAULT 0,
    last_scan TEXT
);