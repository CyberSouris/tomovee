# Notes for coding agents

## Local tool caches

This machine's home directory is a 1 GB tmpfs, so the default cache locations
run out of space. `~/go/pkg/mod` (Go modules), `~/.cache/go-build` (build cache)
and `~/.npm` (npm cache) are all on it. The repository itself lives on a
filesystem with room to spare, so local runs should point the caches at
`tmp/` (gitignored):

```sh
export GOMODCACHE=$PWD/tmp/_gomod
export GOCACHE=$PWD/tmp/_gocache
export GOTMPDIR=$PWD/tmp/_gotmp
export npm_config_cache=$PWD/tmp/_npm
export CYPRESS_CACHE_FOLDER=$PWD/tmp/_cypress
```

`GOCACHE` and `GOMODCACHE` create themselves on demand, `GOTMPDIR` does not, so
`mkdir -p` the `tmp/_*` directories once before the first `go` command.

Do NOT put these in the `Makefile`. CI (`actions/setup-go`,
`actions/setup-node`) caches the default paths, so redirecting them there makes
the workflow silently lose its cache and write into the runner workspace
instead. Override them in the shell only; leave the build environment-neutral.

If a cache does fill up, `go clean -cache` is the fix. Never use `/opt` for
scratch space.

## Linting

`golangci-lint` is not installed and is not an Ubuntu package. CI pins
`v2.13.2` (`.github/workflows/lint.yaml`), so match that:

```sh
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
  | sh -s -- -b "$(go env GOPATH)/bin" v2.13.2
golangci-lint run --timeout=5m
```

If the binary is unavailable, say so rather than claiming the lint passed.
`make fmt-check` and `go vet ./...` are always available.

## End-to-end tests

`node`, `npm`, `internal/webui/node_modules` and Xvfb are all installed, and
`tmp/_cypress` holds Cypress 16.1.0 (802 MB) — do not re-download it. `make e2e`
runs green: 6 mocked-API rendering specs plus 21 real-binary smoke specs.
`ffmpeg` is installed because the server needs `ffprobe` to read media, not
because the tests generate any: the end-to-end fixtures are committed sample
files under `testdata/` (see `testdata/README.adoc`), copied into a per-run
temporary directory by `e2e/run.mjs`. Do not reintroduce code that synthesises
fixtures at run time — a missing fixture should fail loudly, which is why
`run.mjs` checks the source directory exists before copying.

Cypress starts and stops its own Xvfb (`node_modules/cypress/dist/xvfb-*.js`
calls `@cypress/xvfb`, which spawns the `Xvfb` binary directly), so `xauth` and
`xvfb-run` are not needed. Leave `DISPLAY` unset: Cypress skips its own Xvfb
when `DISPLAY` is set, and only recovers from a broken one with a warning.

`/usr/bin/chromium-browser` is a snap shim and is irrelevant: the specs run
Cypress' bundled Electron (`e2e/run.mjs` passes no `--browser`). The Electron
binary's 95 shared libraries all resolve on this image, so `npx cypress verify`
should only ever complain about Xvfb.

`sudo` does not work in this container (`The "no new privileges" flag is set`),
so system packages have to be installed by the user. Report the missing
dependency and stop rather than trying to work around it.

`make e2e` needs a built binary (`make build`, or set `TOMOVEE_BIN`). Avoid
`make frontend` casually: it runs `npm ci`, which deletes and reinstalls
`node_modules`.

`make coverage` merges unit coverage with the coverage the real binary collects
during `e2e:smoke`. Together they are worth +4.3 points overall (78.5% → 82.8%),
and most of it is in `cmd/tomovee` (43.8% → 74.8%), which no unit test can
reach because `main` and its `attach_datasets`/`load_datasets`/`stream_datasets`
closures only run in the real process. `internal/webui` goes 0% → 75% for the
same reason, `internal/database/read.go` 69.1% → 83.4% and
`internal/database/versions.go` 45.1% → 63.4% because the correction specs drive
real splits and episode assignments, and `internal/webserver/settings.go`
68.7% → 83.6% because of the settings spec.

Its `rm -rf tmp/coverage` cleanup only runs on success, so a failed run leaves
`tmp/coverage` behind; the next run clears it first.

Coverage numbers come in two flavours and must not be compared across them:
`make coverage` and `go test -cover` count only the statements a package's own
tests reach (78.5% unit-only, 82.8% with e2e), while `go test
-coverpkg=./...` counts every statement any package's test can reach.

Watch out when measuring that second flavour: pass
`-coverpkg=github.com/cybersouris/tomovee/...`, never `-coverpkg=./...`. Go
expands a directory-based pattern by walking the filesystem, so with
`GOMODCACHE` pointing inside the repository `./...` also matches every module in
`tmp/_gomod` and the reported total collapses to ~27%.

The single biggest remaining gap is `cmd/tomovee`'s `cmd_serve`, which blocks on
`select { case err := <-errs: case <-stop: }`. Its happy path is covered by the
smoke run, but the bind-failure and signal branches are not: covering them in a
unit test would mean sending `SIGINT` to the test process, which is why they are
left alone.

Two Cypress traps worth remembering: a command's URL is built when the command
is *queued*, so anything interpolated from a previous response has to be chained
inside a `.then()`; and specs in a directory run in filename order, which is why
the browse spec derives its expected count from the API instead of hardcoding
"0 titles".
