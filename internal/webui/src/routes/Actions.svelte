<script>
  import { onMount, onDestroy } from 'svelte';
  import { api_get, api_send } from '../api.js';

  let libraries = [];
  let selected = [];

  let scan_progress = null;
  let scan_result = null;
  let scan_running = false;
  let scan_error = '';
  let scan_source = null;

  let match_progress = null;
  let match_result = null;
  let match_running = false;
  let match_error = '';
  let match_source = null;

  let chain_match = false;

  $: busy = scan_running || match_running;

  async function refresh_scan() {
    try {
      const data = await api_get('/api/v1/scan/status');
      if (data.job) {
        scan_progress = data.job.progress;
        scan_running = data.job.running;
        scan_result = data.job.result || null;
      }
    } catch (err) {
      scan_error = err.message;
    }
  }

  async function refresh_match() {
    try {
      const data = await api_get('/api/v1/match/status');
      if (data.job) {
        match_progress = data.job.progress;
        match_running = data.job.running;
        match_result = data.job.result || null;
      }
    } catch (err) {
      match_error = err.message;
    }
  }

  async function load_libraries() {
    try {
      const data = await api_get('/api/v1/settings');
      libraries = data.libraries || [];
      selected = libraries.filter((library) => library.enabled).map((library) => library.name);
    } catch (err) {
      scan_error = err.message;
    }
  }

  function is_selected(name) {
    return selected.includes(name);
  }

  function toggle(name) {
    selected = is_selected(name)
      ? selected.filter((item) => item !== name)
      : [...selected, name];
  }

  function all_selected() {
    return libraries.length > 0 && selected.length === libraries.length;
  }

  function toggle_all() {
    selected = all_selected() ? [] : libraries.map((library) => library.name);
  }

  async function start_scan() {
    scan_error = '';
    scan_result = null;
    try {
      await api_send('/api/v1/scan', 'POST', { libraries: selected.slice() });
      scan_running = true;
    } catch (err) {
      scan_error = err.message;
    }
  }

  async function start_match() {
    chain_match = false;
    match_error = '';
    match_result = null;
    try {
      await api_send('/api/v1/match', 'POST', { libraries: selected.slice() });
      match_running = true;
    } catch (err) {
      match_error = err.message;
    }
  }

  async function start_chain() {
    chain_match = true;
    scan_error = '';
    scan_result = null;
    try {
      await api_send('/api/v1/scan', 'POST', { libraries: selected.slice() });
      scan_running = true;
    } catch (err) {
      chain_match = false;
      scan_error = err.message;
    }
  }

  onMount(() => {
    refresh_scan();
    refresh_match();
    load_libraries();

    scan_source = new EventSource('/api/v1/scan/stream');
    scan_source.addEventListener('status', (event) => {
      const data = JSON.parse(event.data);
      if (data.job) {
        scan_progress = data.job.progress;
        scan_running = data.job.running;
        scan_result = data.job.result || null;
      }
    });
    scan_source.addEventListener('progress', (event) => {
      scan_progress = JSON.parse(event.data).progress;
      scan_running = true;
    });
    scan_source.addEventListener('done', () => {
      scan_running = false;
      refresh_scan();
      if (chain_match) {
        start_match();
      }
    });

    match_source = new EventSource('/api/v1/match/stream');
    match_source.addEventListener('status', (event) => {
      const data = JSON.parse(event.data);
      if (data.job) {
        match_progress = data.job.progress;
        match_running = data.job.running;
        match_result = data.job.result || null;
      }
    });
    match_source.addEventListener('progress', (event) => {
      match_progress = JSON.parse(event.data).progress;
      match_running = true;
    });
    match_source.addEventListener('done', () => {
      match_running = false;
      refresh_match();
    });

    return () => {
      scan_source && scan_source.close();
      match_source && match_source.close();
    };
  });

  onDestroy(() => {
    scan_source && scan_source.close();
    match_source && match_source.close();
  });

  const scan_counters = [
    ['files_found', 'Found'],
    ['files_scanned', 'Scanned'],
    ['new_files', 'New'],
    ['skipped', 'Skipped'],
    ['errors', 'Errors'],
  ];

  const match_counters = [
    ['total', 'Entries'],
    ['done', 'Processed'],
    ['matched', 'Matched'],
    ['unmatched', 'Unmatched'],
    ['errors', 'Errors'],
  ];
</script>

<section>
  {#if libraries.length}
    <div class="controls">
      <button class="secondary" on:click={toggle_all} disabled={busy || libraries.length === 0}>
        {all_selected() ? 'Select none' : 'Select all'}
      </button>
      <span class="spacer"></span>
      <button on:click={start_match} disabled={busy || selected.length === 0}>
        {match_running ? 'Matching…' : 'Match'}
      </button>
      <button on:click={start_chain} disabled={busy || selected.length === 0}>
        {chain_match ? 'Scan & Match…' : 'Scan & Match'}
      </button>
      <button on:click={start_scan} disabled={busy || selected.length === 0}>
        {scan_running ? 'Scanning…' : `Scan ${selected.length === 1 ? 'library' : 'libraries'}`}
      </button>
    </div>
    <div class="list">
      {#each libraries as library}
        <label class="lib" class:checked={is_selected(library.name)}>
          <input type="checkbox" checked={is_selected(library.name)} on:change={() => toggle(library.name)} disabled={busy} />
          <span class="name">{library.name}</span>
          <span class="path muted">{library.path}</span>
          {#if library.enabled}<span class="badge">watched</span>{/if}
        </label>
      {/each}
    </div>
    <p class="muted help">Only the checked libraries are affected. Watched libraries are updated by the folder watcher automatically.</p>
  {:else}
    <p class="muted">No libraries configured — add one on the Settings page.</p>
  {/if}
  {#if scan_error}<p class="error">{scan_error}</p>{/if}
  {#if match_error}<p class="error">{match_error}</p>{/if}

  {#if scan_progress}
    <h2>Scan</h2>
    <p class="phase">Phase: <strong>{scan_progress.phase}</strong></p>
    <div class="counters">
      {#each scan_counters as [key, label]}
        <div class="counter">
          <span class="value">{scan_progress[key] ?? 0}</span>
          <span class="muted">{label}</span>
        </div>
      {/each}
    </div>
    {#if scan_progress.path}
      <p class="path muted">{scan_progress.path}</p>
    {/if}
  {/if}

  {#if scan_result}
    <h2>Scan result</h2>
    <p>
      {scan_result.found} found, {scan_result.new} new, {scan_result.skipped} skipped,
      {scan_result.missing} missing.
    </p>
    {#if scan_result.errors && scan_result.errors.length}
      <ul class="errors">
        {#each scan_result.errors as message}
          <li>{message}</li>
        {/each}
      </ul>
    {/if}
  {/if}

  {#if match_progress && match_progress.total > 0}
    <h2>Match</h2>
    <p class="phase">Phase: <strong>{match_progress.phase}</strong></p>
    <div class="counters">
      {#each match_counters as [key, label]}
        <div class="counter">
          <span class="value">{match_progress[key] ?? 0}</span>
          <span class="muted">{label}</span>
        </div>
      {/each}
    </div>
    {#if match_progress.title}
      <p class="title muted">{match_progress.done} / {match_progress.total} — {match_progress.title}</p>
    {/if}
  {/if}

  {#if match_result}
    <h2>Match result</h2>
    <p>
      {match_result.total} entries, {match_result.matched} matched, {match_result.unmatched} unmatched.
    </p>
    {#if match_result.errors && match_result.errors.length}
      <ul class="errors">
        {#each match_result.errors as message}
          <li>{message}</li>
        {/each}
      </ul>
    {/if}
  {/if}
</section>

<style>
  .controls {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    margin-bottom: 0.8rem;
    flex-wrap: wrap;
  }

  .controls .spacer {
    flex: 1;
  }

  .list {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    overflow: hidden;
    max-width: 48rem;
  }

  .lib {
    display: flex;
    gap: 0.7rem;
    align-items: center;
    padding: 0.6rem 0.9rem;
    border-bottom: 1px solid var(--border);
    cursor: pointer;
    font-size: 0.9rem;
  }

  .lib:last-child {
    border-bottom: none;
  }

  .lib.checked {
    background: color-mix(in srgb, var(--accent) 6%, transparent);
  }

  .lib .name {
    font-weight: 600;
    min-width: 10rem;
  }

  .lib .path {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .badge {
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 0.1rem 0.45rem;
    font-size: 0.72rem;
    color: var(--muted);
  }

  .help {
    margin-top: 0.5rem;
    font-size: 0.85rem;
  }

  .phase {
    margin-top: 0.75rem;
  }

  .muted {
    color: var(--muted);
  }

  .error {
    color: var(--bad);
  }

  .errors {
    color: var(--bad);
    font-size: 0.85rem;
  }

  .counters {
    display: flex;
    gap: 1.5rem;
    flex-wrap: wrap;
    margin: 1rem 0;
  }

  .counter {
    display: flex;
    flex-direction: column;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.7rem 1rem;
    min-width: 5rem;
  }

  .value {
    font-size: 1.5rem;
    font-weight: 600;
  }

  .path,
  .title {
    font-size: 0.85rem;
    word-break: break-all;
  }
</style>