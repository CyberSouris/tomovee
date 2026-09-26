// Orchestrates the Cypress e2e runs.
//
//   mocked: build the SPA, serve dist with `vite preview`, run the mocked
//           rendering specs (e2e/mocked) against it, then tear the server down.
//   smoke:  launch the real `tomovee serve` binary against a generated fixture
//           config on a free port, run the full-stack smoke spec
//           (e2e/smoke) against it, then tear the server down.
//
// The smoke mode needs a built binary; point TOMOVEE_BIN at one or run
// `make build` first (default: bin/tomovee at the repository root).

import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:net';
import { setTimeout as sleep } from 'node:timers/promises';
import path from 'node:path';

const here = path.resolve(path.dirname(fileURLToPath(import.meta.url))); // internal/webui/e2e
const webui = path.resolve(here, '..'); // internal/webui
const repo = path.resolve(here, '..', '..', '..'); // repository root

function fail(message) {
  console.error(message);
  process.exit(1);
}

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: 'inherit', ...options });
    child.on('error', reject);
    child.on('close', (code) => resolve(code ?? 1));
  });
}

// spawn_server launches a long-lived server in its own process group so it can
// be torn down together with any children it spawned. The vite CLI is spawned
// directly (not through the npm wrapper) so the group leader is the server
// process itself.
function spawn_server(command, args, options = {}) {
  return spawn(command, args, { stdio: 'inherit', detached: true, ...options });
}

async function stop_server(child) {
  if (!child || child.pid === undefined) return;
  const group = -child.pid;
  const exited = new Promise((resolve) => child.once('exit', resolve));
  try {
    process.kill(group, 'SIGTERM');
  } catch {
    return;
  }
  const done = await Promise.race([exited.then(() => true), sleep(1500).then(() => false)]);
  if (!done) {
    try {
      process.kill(group, 'SIGKILL');
    } catch {
      // already gone
    }
    await exited;
  }
}

async function cypress_run(spec, base_url) {
  return run('npx',
    ['--no-install', 'cypress', 'run', '--spec', spec, '--config', `baseUrl=${base_url}`],
    { cwd: webui });
}

const node_bin = process.execPath;
const vite_cli = path.join(webui, 'node_modules', 'vite', 'bin', 'vite.js');

async function wait_http(url, timeout_ms) {
  const deadline = Date.now() + timeout_ms;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {
      // not up yet
    }
    await sleep(200);
  }
  fail(`timed out waiting for ${url}`);
}

// The correction specs need files in the catalog, and the scan only keeps what
// ffprobe can read, so the fixture has to be real video however small. Without
// ffmpeg the media dir stays empty and corrections.cy.js skips itself.
function seed_media(dir) {
  for (const name of ['Mystery.Film.2020.mkv', 'Mystery.Film.2020.1080p.mkv']) {
    const result = spawnSync('ffmpeg', [
      '-loglevel', 'error', '-f', 'lavfi', '-i', 'color=c=black:s=64x64:r=5:d=1',
      '-c:v', 'mpeg4', '-frames:v', '1', '-y', path.join(dir, name),
    ], { stdio: 'ignore' });
    if (result.status !== 0) {
      console.warn(`Could not seed ${name} (is ffmpeg installed?): the correction specs will skip`);
      return;
    }
  }
}

async function free_port() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

async function run_mocked() {
  console.log('Building the SPA…');
  const build = await run('npm', ['run', 'build'], { cwd: webui });
  if (build !== 0) fail('vite build failed');

  const port = 4173;
  const preview = spawn_server(
    node_bin,
    [vite_cli, 'preview', '--host', '127.0.0.1', '--port', String(port), '--strictPort'],
    { cwd: webui });
  try {
    await wait_http(`http://127.0.0.1:${port}/`, 30000);
    console.log(`Serving built SPA at http://127.0.0.1:${port}`);
    const code = await cypress_run('e2e/mocked/**/*.cy.js', `http://127.0.0.1:${port}`);
    if (code !== 0) process.exitCode = code;
  } finally {
    await stop_server(preview);
  }
}

async function run_smoke() {
  const binary = process.env.TOMOVEE_BIN || path.join(repo, 'bin', 'tomovee');
  if (!existsSync(binary)) {
    fail(`no tomovee binary at ${binary} — set TOMOVEE_BIN or run \`make build\` first`);
  }

  const port = await free_port();
  const tmp = mkdtempSync(path.join(tmpdir(), 'tomovee-e2e-'));
  const media = mkdtempSync(path.join(tmpdir(), 'tomovee-media-'));
  const config_path = path.join(tmp, 'config.yaml');
  writeFileSync(config_path, [
    `listen: "127.0.0.1:${port}"`,
    `database_path: "${path.join(tmp, 'tomovee.db')}"`,
    `poster_cache_dir: "${path.join(tmp, 'posters')}"`,
    'watch_enabled: false',
    // The seeded videos are a few hundred bytes each, well under the 50 MB
    // default, which would filter them out before they ever reach the catalog.
    'scan:',
    '  min_file_size_mb: 0',
    'libraries:',
    `  Movies: "${media}"`,
    '',
  ].join('\n'));
  seed_media(media);

  console.log(`Launching ${binary} on 127.0.0.1:${port}…`);
  const server = spawn_server(binary, ['serve', '--config', config_path, '--log-level', 'warn']);
  try {
    await wait_http(`http://127.0.0.1:${port}/api/v1/settings`, 30000);
    console.log(`tomovee serve is up at http://127.0.0.1:${port}`);
    const code = await cypress_run('e2e/smoke/**/*.cy.js', `http://127.0.0.1:${port}`);
    if (code !== 0) process.exitCode = code;
  } finally {
    await stop_server(server);
    rmSync(tmp, { recursive: true, force: true });
    rmSync(media, { recursive: true, force: true });
  }
}

const mode = process.argv[2];
switch (mode) {
  case 'mocked':
    await run_mocked();
    break;
  case 'smoke':
    await run_smoke();
    break;
  default:
    fail('usage: node e2e/run.mjs <mocked|smoke>');
}