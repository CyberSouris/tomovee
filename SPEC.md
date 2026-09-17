# Tomovee — Movie & TV Database

Author: Cyber Souris <cybersouris@proton.me>
Module: `github.com/cybersouris/tomovee`
Status: active specification — treat as the authoritative plan for all development work.

## 1. Purpose

Tomovee is a self-hosted movie and TV series database. It scans configured
directories for media files, extracts technical metadata (duration, languages,
subtitles, resolution, format, codecs), identifies each file via external
databases (OpenSubtitles hash API, TMDB, IMDB datasets), and stores everything in
a local SQLite database. A web UI served by the daemon lets the user browse,
search, and review the catalog.

The application must never move, rename, delete, or otherwise modify original
media files without the user's explicit consent. Cataloging is read-only.

## 2. Coding conventions (non-standard Go)

These conventions apply to all Go code in this repository and override standard
Go style. `gofmt` output is still expected; violations of the naming rules below
must be enforced.

### 2.1 Identifier naming

| Scope | Rule | Examples |
|-------|------|----------|
| Public / exported identifiers | `Snake_Case` — leading capital letter plus underscore separators | `Scan_directory()`, `Version_Info`, `movie_id` |
| Private identifiers | `snake_case` — all lowercase with underscores | `scan_directory()`, `calc_hash()`, `tmp_dir` |
| Packages | lowercase | `scanner`, `metadata`, `matcher` |
| Files | lowercase | `movie_scan.go`, `open_subtitles_api.go` |
| Constants | same rules as identifiers (public `Snake_Case` / private `snake_case`) | `Default_Port`, `max_result_count` |

Notes:
- Visibility is expressed by the leading capital letter (Go's built-in rule),
  combined with snake_case rest-of-name.
- Do not use camelCase or PascalCase anywhere. Existing standard-library and
  third-party APIs referenced in code keep their own names; only our identifiers
  follow these rules.
- Acronyms follow the same rule: `Imdb_Id`, `tmdb_client`, never `IMDBId`.

### 2.2 Style

- Format with `gofmt` (the output must be gofmt-clean).
- No linting exceptions yet; keep the default `go vet` clean.
- Comments: standard Go doc comments for public identifiers; keep them in the
  same snake_case vocabulary.
- No other formatting deviations for now. Revisit when the codebase exists.

## 3. Tech stack

- Language: Go 1.26 (module `github.com/cybersouris/tomovee`)
- Media metadata extraction: `ffprobe` (external binary, **required**)
- Database: SQLite via a pure-Go driver (no cgo) — e.g. `modernc.org/sqlite`
- Config: YAML (`gopkg.in/yaml.v3`)
- Frontend: Svelte SPA, built to static assets, embedded in the binary with
  `go:embed`
- HTTP: Go standard library (`net/http`) + `httptest` for tests; no heavyweight
  framework
- Structured logging: `log/slog`
- HTTP client for external APIs: standard `net/http` with rate limiting

### 3.1 External dependencies (system level)

- `ffprobe` (and `ffmpeg`) must be installed. At runtime, Tomovee locates
  `ffprobe` on `PATH`. If missing, scanning must fail with a clear message.

## 4. Architecture / project layout

Domain-driven, all under a single module. Proposed layout (adjust during
implementation if a domain grows):

```
cmd/tomovee/          CLI entry point (serve / scan / version)
internal/             all non-public app code lives under internal/
  config/             YAML config load + validation
  scanner/            filesystem walking, file classification, hashing, orchestration
  metadata/           ffprobe wrapper
  sidecar/            .nfo / .txt sidecar parsing and merging
  matcher/            matching pipeline (hash-first, search fallback, manual)
  tmdb/               TMDB API client
  opensubtitles/      OpenSubtitles API client (+ hash computation)
  imdb_datasets/      IMDB dataset consumer (offline enrichment)
  database/           SQLite schema, migrations, repositories
  poster_cache/       local poster image caching
  webserver/          HTTP handlers, REST API, SPA serving, SSE/websocket progress
  webui/              Svelte source + built assets embedded via go:embed
```

Public entry points across packages use `Snake_Case`; everything else that must
stay private inside a package uses `snake_case`.

## 5. Config (`config.yaml`)

Location: `--config` flag; default path `$HOME/.config/tomovee/config.yaml`,
then `./config.yaml`. Sample:

```yaml
database_path: ~/.local/share/tomovee/tomovee.db
poster_cache_dir: ~/.local/share/tomovee/posters
listen: "127.0.0.1:8080"
scan_directories:
  - /mnt/media/movies
  - /mnt/media/shows
api:
  tmdb_key: ""
  opensubtitles_username: ""
  opensubtitles_password: ""
  opensubtitles_user_agent: "Tomovee/0.1 by Cyber Souris"
watch_enabled: false        # global default; per-folder toggle in UI persists to DB
scan:
  quality_subdir_min_size: 2  # not used yet — placeholder for future tidy suggestions
```

Validation rules:
- At least one `scan_directories` entry OR an active watch must be possible.
- API keys may be empty; matching falls back gracefully (filename parsing /
  manual match), but a warning is logged.

## 6. Scanning pipeline

### 6.1 File discovery

- Walk each configured directory **recursively**.
- Accepted extensions (case-insensitive): the broad set of video containers —
  `.mkv`, `.mp4`, `.avi`, `.mov`, `.webm`, `.ts`, `.m2ts`, `.mts`, `.wmv`,
  `.flv`, `.m4v`, `.mpg`, `.mpeg`, `.vob`, `.ogv`, `.divx`.
- Skip hidden files/dirs (dot-prefixed), and anything in an exclusion list.
- Skip **junk**: filenames containing (case-insensitive) common non-content
  markers: `sample`, `trailer`, `extra`, `featurette`, `behind the scenes`,
  `blooper`, `intro`, `credits`, `teaser`, `deleted scene`, `recap`, and
  `-junk-` style names. Also skip files smaller than a sane minimum (e.g.
  `< 50 MB` for movies; confirm during implementation) that are unlikely to be
  real content.
- Series detection: filenames matching `S01E02`, `1x02`, or `season 1 episode 2`
  patterns are treated as episodes. Specials (`S00x`, `special` in name) are
  catalogued but tagged as specials.

### 6.2 Hash (OpenSubtitles movie hash)

- Read 64 KiB at start of file + 64 KiB at end of file; combine with file size
  into the OpenSubtitles 64-bit hash algorithm.
- Used to query the OpenSubtitles API to obtain the IMDb id / canonical name.
- If the file is smaller than 128 KiB, skip hash matching (fall through to
  filename search).

### 6.3 Metadata extraction (ffprobe)

Run `ffprobe` (JSON output) per file and extract:

- Container format
- Duration (seconds), and total bitrate if available
- Video stream: codec, width × height, resolution label (e.g. 1080p, 4K),
  HDR/BT.2020 hints, bit depth, frame rate
- Audio streams: language, codec, channels
- Subtitle streams: language, format
- File size (from filesystem)

Data must tolerate one bad stream without failing the whole file.

### 6.4 Sidecar metadata files (.nfo / .txt)

Video files often sit next to a same-named sidecar text file (Kodi-style `.nfo`,
occasionally `.txt`) holding extra metadata. When a video file is new/changed,
look for sidecars next to it (same basename, `.nfo` then `.txt`; if multiple
siblings exist, read all and merge):

- Kodi `.nfo` files (XML) and plain-text `.txt` sidecars are both supported.
- Extract and merge, preferring sidecar data when present:
  - **Track metadata**: audio track language, subtitle language, video/audio
    codec, channels, resolution, bitrate — useful when ffprobe reports
    undetectable or missing values, or as additional confidence data.
  - **Content metadata**: movie/show title, original title, year, plot/overview,
    genres, runtime, ratings, and `imdbid` when present. This feeds the matching
    step (§6.5) as a hint and disambiguation source.
- ffprobe remains the authoritative technical source; sidecar data augments and,
  for fields ffprobe cannot report, fills them in. Conflicting sources are
  marked or resolved in order: ffprobe > sidecar for technical fields, sidecar >
  parsed filename for content fields.
- Sidecar files are read from disk per video when the video is new/changed;
  content is stored with the version/catalog entry so re-scans are cheap.

### 6.5 Matching

Priority order:

1. **OpenSubtitles hash lookup** → canonical title + `imdb_id`.
2. **TMDB search** — parse a best-guess title/year from the filename (clean
   off release-group tags, quality tags, brackets), search TMDB, pick highest
   confidence match. Fetch full metadata: overview, original title, year,
   rating/vote count, genres, runtime, poster.
3. **IMDB datasets** (offline) — local enrichment/verification of titles,
   release years, and ids; primarily to disambiguate or fill gaps when APIs are
   down or keys missing.
4. **Manual match / leave unmatched** — if nothing confident (< threshold),
   store as unmatched and surface in the web UI for a manual re-match.

Series matching: match the series by cleaned show name, then attach specific
season/episode metadata (episode title, airdate if available).

### 6.6 External API etiquette

- Cache external lookups in SQLite (hash → imdb id, tmdb search queries) so
  re-scans need no network.
- Rate-limit requests to TMDB and OpenSubtitles (configurable; sensible
  defaults, e.g. TMDB ~4 rps, OS ~1 rps) and respect backoff/retry.
- Never block the whole scan when external services are unreachable; store what
  we have locally and mark the entry as "needs matching".

### 6.7 Re-scan detection

- File identity = absolute path + size + mtime. If unchanged and already
  catalogued with full metadata, skip cheaply (no ffprobe, no network).
- Detect removed files and mark entries as missing (do not delete the DB row;
  flag it `missing`).

### 6.8 Version grouping

- The same movie (or the same episode) present in multiple files — different
  qualities, e.g. 1080p and 4K — is grouped under one catalog entry.
- Grouping key after successful matching: same `imdb_id` (or same TMDB id). For
  identical single instance, fall back to matching by (title, year).
- Each catalog entry has a list of `versions`; each version references one file
  with its technical metadata. Selection picks the best/largest version for
  display, with all versions browsable.

## 7. Data model (SQLite)

Tables (names in `snake_case`; expand during migration work):

- `config` (key, value) — persisted runtime settings incl. per-folder watch state
- `catalog_entry`
  - `id`, `media_type` (`movie` | `series`)
  - `title`, `original_title`, `release_year`, `overview`, `runtime_minutes`
  - `rating`, `vote_count`, `genres` (comma list or join table)
  - `imdb_id`, `tmdb_id`
  - `poster_path` (local cached path)
  - `status` (`matched` | `needs_lookup` | `missing`)
  - `is_series` + series-specific columns
- `series_metadata` — show-level details for series entries
- `episode`
  - `id`, `catalog_entry_id`, `season_number`, `episode_number`, `title`,
    `overview`, `airdate`, `is_special`
- `version`
  - `id`, `catalog_entry_id` OR `episode_id`, `file_path`, `size_bytes`,
    `mtime`, `duration_seconds`, `container`, `resolution` (w × h + label),
    `video_codec`, `hdr`, `frame_rate`, `bit_depth`
  - track data may be augmented from sidecar files; provenance is tracked
    (`ffprobe` | `sidecar`) so conflicts can be reviewed
- `audio_track` (`version_id`, `language`, `codec`, `channels`, `source`)
- `subtitle_track` (`version_id`, `language`, `format`, `source`)
- `lookup_cache` (`kind`, `key`, `payload`, `created_at`) — cached external API results
- `watch_folder` (`path`, `enabled`, `last_scan`)

Mappings: movies→versions via `catalog_entry_id`; episodes→versions via
`episode_id`. Unique constraints protect against duplicate file paths.

## 8. Runtime modes

### 8.1 Daemon (`tomovee serve`)

- Loads config + DB, starts the HTTP server, serves API + SPA on `listen`.
- Provides scan triggers, job progress reporting, watch loops.
- **Folder watching**: off by default. A user can enable watching per folder in
  the UI; the daemon then polls (e.g. every 5 min) or watches file events and
  only scans new/changed files. State persisted in `watch_folder` table.

### 8.2 One-shot CLI (`tomovee scan`)

- Runs the full scanning pipeline once against configured directories, writes
  results to the DB, then exits. Prints a summary (n found, n new, n matched,
  n unmatched, errors).
- `--help` and `version` subcommands.

## 9. Web UI (Svelte SPA)

Built with Svelte + Vite to static files, embedded via `go:embed` and served by
the daemon. Pages:

- **Browse** — catalog grid/list with posters, sort (title/year/rating), filter
  (media type, genre, year, resolution, language), search box, series expandable
  to episodes.
- **Detail** — poster, overview, metadata, all versions with their track
  breakdown (audio/subtitle languages, resolution, codec), file paths, and
  status.
- **Scan** — trigger a full scan or incremental scan of chosen folders, watch
  live progress (files scanned, matched, unmatched, errors) over SSE/WebSocket.
- **Unmatched** — list of entries that failed matching; manual re-match against
  TMDB and/or edit title, with an option to retry hash-based lookup.
- **Settings** — folder list, per-folder watch toggle, API key status, poster
  cache management.

API surface (JSON under `/api/v1/`): catalog list/search, detail, scan job
control + status stream, unmatched list + manual match, settings read/write,
poster image serving (from local cache).

## 10. Workflow for development

1. Scaffold the module, config package, database schema + migrations.
2. Implement ffprobe metadata extraction + tests with fixture files.
3. Implement `.nfo`/`.txt` sidecar parsing and metadata merging + tests with fixture files.
4. Implement file discovery/classification + OpenSubtitles hash.
5. Implement matching (hash → TMDB → datasets → manual).
6. Implement version grouping, scanning orchestration, re-scan detection.
7. Implement the SPA (browse, detail, scan, unmatched, settings).
8. Watch folders, caching polish, docs, packaging.

Each step lands as gradual semantic commits (see §11).

## 11. Git workflow

- **Conventional commits**: `feat`, `fix`, `docs`, `style`, `refactor`,
  `test`, `chore`, `perf`, `build`, `ci` with optional scope, e.g.
  `feat(database): add catalog schema`. One concern per commit, small diffs.
- Repository identity: Cyber Souris <cybersouris@proton.me> (already configured).
- Default branch: `main`.

## 12. Non-goals (v1)

- No file moving/renaming/reorganizing without explicit consent (catalog only;
  an optional `tidy --dry-run` suggestion feature is a possible later add-on).
- No transcoding or playback.
- No user accounts / auth (single-user, localhost or trusted LAN).
- No watched/rating-state tracking of the user's own viewing (possible later).
- No mobile app (responsive web UI only).