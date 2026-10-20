-- 0005_schema_redesign.sql — files become the primary table.
--
-- catalog_entry is reduced to the identity and artwork worth caching locally:
-- media type (an INTEGER enum), title, release year, the external ids, and the
-- poster path. Everything the source can hand back on demand (overview, rating,
-- vote count, runtime) is fetched when a detail view asks for it instead of
-- being stored. Catalog status is gone: whether a title is matched follows from
-- whether any media_file points at it.
--
-- version becomes media_file and gains the file's own status (an INTEGER enum),
-- which is the single source of truth for matched / needs_lookup. file_path is
-- still relative to its library, and a file may exist with no catalog link at
-- all: a scanned but unmatched file is a row, not a catalog entry. resolution_
-- label is dropped, derivable from the width and height.
--
-- episode keeps its local numbering and title; its status, overview, airdate,
-- and is_special all come from the original source on demand.

-- --- catalog_entry -----------------------------------------------------------
-- Rebuilt to drop status, runtime_minutes, overview, rating, vote_count, and
-- original_title. media_type TEXT ('movie' | 'series') maps to 0 | 1.

-- The old catalog/episode status is read here, before those tables are rebuilt
-- without it, so every file can be backfilled with its former match state.
CREATE TEMP TABLE file_status_backfill AS
SELECT v.id AS version_id,
       CASE WHEN COALESCE(ce.status, ep.status) = 'matched' THEN 1 ELSE 0 END AS status
FROM version v
LEFT JOIN catalog_entry ce ON ce.id = v.catalog_entry_id
LEFT JOIN episode ep ON ep.id = v.episode_id;

CREATE TABLE catalog_entry_new (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type     INTEGER NOT NULL CHECK (media_type IN (0, 1)),
    title          TEXT NOT NULL,
    release_year   INTEGER,
    imdb_id        VARCHAR(20),
    tmdb_id        INTEGER,
    poster_path    TEXT,
    created_at     TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    updated_at     TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO catalog_entry_new
    (id, media_type, title, release_year, imdb_id, tmdb_id, poster_path, created_at, updated_at)
SELECT id,
       CASE media_type WHEN 'series' THEN 1 ELSE 0 END,
       title, release_year, imdb_id, tmdb_id, poster_path, created_at, updated_at
FROM catalog_entry;

DROP TABLE catalog_entry;
ALTER TABLE catalog_entry_new RENAME TO catalog_entry;

CREATE INDEX idx_catalog_entry_title ON catalog_entry (title);
CREATE UNIQUE INDEX ux_catalog_entry_imdb ON catalog_entry (imdb_id) WHERE imdb_id IS NOT NULL;
CREATE UNIQUE INDEX ux_catalog_entry_tmdb ON catalog_entry (tmdb_id) WHERE tmdb_id IS NOT NULL;

-- --- episode -----------------------------------------------------------------
-- Rebuilt to drop status, overview, airdate, and is_special.
CREATE TABLE episode_new (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_entry_id INTEGER NOT NULL REFERENCES catalog_entry(id) ON DELETE CASCADE,
    season_number    INTEGER,
    episode_number   INTEGER,
    title            TEXT,
    created_at       TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    updated_at       TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO episode_new
    (id, catalog_entry_id, season_number, episode_number, title, created_at, updated_at)
SELECT id, catalog_entry_id, season_number, episode_number, title, created_at, updated_at
FROM episode;

DROP TABLE episode;
ALTER TABLE episode_new RENAME TO episode;

CREATE INDEX idx_episode_catalog ON episode (catalog_entry_id);

-- --- media_file (was version) -----------------------------------------------
-- Rebuilt to gain status, lose resolution_label, relax the owner CHECK so an
-- unmatched file can stand alone, and switch the owner foreign keys from
-- CASCADE to SET NULL so deleting a catalog entry no longer destroys the file.
-- The old status lived on the owning entry or episode; it is backfilled here.
CREATE TABLE media_file (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id        INTEGER REFERENCES library(id) ON DELETE SET NULL,
    catalog_entry_id  INTEGER REFERENCES catalog_entry(id) ON DELETE SET NULL,
    episode_id        INTEGER REFERENCES episode(id) ON DELETE SET NULL,
    file_path         TEXT NOT NULL,
    size_bytes        INTEGER,
    mtime             TEXT,
    duration_seconds  REAL,
    container         TEXT,
    resolution_width  INTEGER,
    resolution_height INTEGER,
    video_codec       TEXT,
    hdr               INTEGER,
    frame_rate        REAL,
    bit_depth         INTEGER,
    hash              TEXT,
    -- 0 = needs_lookup, 1 = matched. The file's own state; nothing else stores it.
    status            INTEGER NOT NULL DEFAULT 0 CHECK (status IN (0, 1)),
    created_at        TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    updated_at        TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO media_file
    (id, library_id, catalog_entry_id, episode_id, file_path, size_bytes, mtime,
     duration_seconds, container, resolution_width, resolution_height, video_codec,
     hdr, frame_rate, bit_depth, hash, status, created_at, updated_at)
SELECT v.id, v.library_id, v.catalog_entry_id, v.episode_id, v.file_path, v.size_bytes,
       v.mtime, v.duration_seconds, v.container, v.resolution_width, v.resolution_height,
       v.video_codec, v.hdr, v.frame_rate, v.bit_depth, v.hash,
       COALESCE(fs.status, 0),
       v.created_at, v.updated_at
FROM version v
LEFT JOIN file_status_backfill fs ON fs.version_id = v.id;

DROP TABLE version;
DROP TABLE file_status_backfill;

CREATE UNIQUE INDEX ux_media_file_library_path
    ON media_file (library_id, file_path) WHERE library_id IS NOT NULL;
CREATE INDEX idx_media_file_catalog ON media_file (catalog_entry_id);
CREATE INDEX idx_media_file_episode ON media_file (episode_id);
CREATE INDEX idx_media_file_status ON media_file (status);

-- --- audio_track / subtitle_track -------------------------------------------
-- Rebuilt to repoint the foreign key from version to media_file.
CREATE TABLE audio_track_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
    language      TEXT,
    codec         TEXT,
    channels      INTEGER,
    source        TEXT NOT NULL DEFAULT 'ffprobe'
);

INSERT INTO audio_track_new (id, media_file_id, language, codec, channels, source)
SELECT id, version_id, language, codec, channels, source FROM audio_track;

DROP TABLE audio_track;
ALTER TABLE audio_track_new RENAME TO audio_track;

CREATE INDEX idx_audio_track_media_file ON audio_track (media_file_id);

CREATE TABLE subtitle_track_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
    language      TEXT,
    format        TEXT,
    source        TEXT NOT NULL DEFAULT 'ffprobe'
);

INSERT INTO subtitle_track_new (id, media_file_id, language, format, source)
SELECT id, version_id, language, format, source FROM subtitle_track;

DROP TABLE subtitle_track;
ALTER TABLE subtitle_track_new RENAME TO subtitle_track;

CREATE INDEX idx_subtitle_track_media_file ON subtitle_track (media_file_id);
