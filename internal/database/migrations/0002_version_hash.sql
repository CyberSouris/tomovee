-- 0002_version_hash.sql — store the OpenSubtitles content hash per version so
-- matching can run as a separate background step without re-reading files.

ALTER TABLE version ADD COLUMN hash TEXT;