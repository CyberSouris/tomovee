-- 0003_libraries.sql — named libraries; version file paths are stored
-- relative to their library's root instead of as absolute paths.

CREATE TABLE library (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL UNIQUE,
    path      TEXT NOT NULL,
    enabled   INTEGER NOT NULL DEFAULT 0,
    last_scan TEXT
);

ALTER TABLE version ADD COLUMN library_id INTEGER REFERENCES library(id) ON DELETE SET NULL;

-- The old unique index keyed on the absolute path no longer applies once paths
-- are stored per-library-relative. Old rows that have not been normalized yet
-- (library_id IS NULL, absolute file_path) keep their absolute path and remain
-- unconstrained; they are rewritten the next time their library is scanned.
DROP INDEX ux_version_file_path;
CREATE UNIQUE INDEX ux_version_library_path ON version (library_id, file_path) WHERE library_id IS NOT NULL;

-- Migrate registered watch folders into libraries, naming each by its
-- basename. Folders whose basename is empty or would collide with an earlier
-- row keep the full path as their name.
WITH RECURSIVE split(path, head, rest) AS (
    SELECT path, NULL, rtrim(path, '/') || '/'
    FROM watch_folder
    UNION ALL
    SELECT path, substr(rest, 1, instr(rest, '/') - 1), substr(rest, instr(rest, '/') + 1)
    FROM split
    WHERE instr(rest, '/') > 0
),
named AS (
    SELECT wf.path AS folder_path, wf.enabled, wf.last_scan,
           sp.head AS basename,
           ROW_NUMBER() OVER (PARTITION BY sp.head ORDER BY wf.path) AS rn
    FROM watch_folder wf
    JOIN split sp ON sp.path = wf.path
    WHERE sp.rest = ''
)
INSERT INTO library (name, path, enabled, last_scan)
SELECT CASE WHEN basename IS NULL OR basename = '' OR rn > 1 THEN folder_path ELSE basename END,
       folder_path, enabled, last_scan
FROM named;

DROP TABLE watch_folder;