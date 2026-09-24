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

import { spawn } from 'node:child_process';
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

async function cypress_run(spec) {
  return run('npx', ['--no-install', 'cypress', 'run', '--spec', spec],
    { cwd: webui });
}

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
  const preview = spawn('npm', ['run', 'preview', '--', '--host', '127.0.0.1', '--port', String(port), '--strictPort'],
    { cwd: webui, stdio: 'inherit' });
  try {
    await wait_http(`http://127.0.0.1:${port}/`, 30000);
    console.log(`Serving built SPA at http://127.0.0.1:${port}`);
    const code = await cypress_run('e2e/mocked/**/*.cy.js');
    if (code !== 0) process.exitCode = code;
  } finally {
    preview.kill('SIGTERM');
    await sleep(200);
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
    'libraries:',
    `  Movies: "${media}"`,
    '',
  ].join('\n'));

  console.log(`Launching ${binary} on 127.0.0.1:${port}…`);
  const server = spawn(binary, ['serve', '--config', config_path, '--log-level', 'warn'],
    { stdio: 'inherit' });
  try {
    await wait_http(`http://127.0.0.1:${port}/api/v1/settings`, 30000);
    console.log(`tomovee serve is up at http://127.0.0.1:${port}`);
    const code = await cypress_run('e2e/smoke/**/*.cy.js');
    if (code !== 0) process.exitCode = code;
  } finally {
    server.kill('SIGTERM');
    await sleep(300);
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