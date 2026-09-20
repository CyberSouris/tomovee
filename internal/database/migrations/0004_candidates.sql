-- 0004_candidates.sql — per-entry shortlist of ambiguous match candidates that
-- the user still needs to review. They persist so a rematch that stays
-- ambiguous surfaces a picker both on the Unmatched screen and on the title's
-- detail page, even after a reload.

CREATE TABLE candidate (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_entry_id INTEGER NOT NULL REFERENCES catalog_entry(id) ON DELETE CASCADE,
    tmdb_id          INTEGER NOT NULL DEFAULT 0,
    imdb_id          TEXT NOT NULL DEFAULT '',
    title            TEXT NOT NULL DEFAULT '',
    year             INTEGER NOT NULL DEFAULT 0,
    media_type       TEXT NOT NULL DEFAULT '',
    score            REAL NOT NULL DEFAULT 0,
    overview         TEXT NOT NULL DEFAULT '',
    poster_path      TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX ix_candidate_catalog_entry ON candidate (catalog_entry_id);