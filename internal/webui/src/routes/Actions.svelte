<script>
  import { onMount, onDestroy } from 'svelte';
  import { api_get, api_send } from '../api.js';

  let libraries = [];
  let selected_set = {};
$: selected = Object.keys(selected_set).filter((name) => selected_set[name]);
$: all_selected = libraries.length > 0 && libraries.every((library) => selected_set[library.name]);

  let scan_progress = null;
  let scan_result = null;
  let scan_running = false;
  let scan_force = false;
  let scan_error = '';
  let scan_source = null;

  let match_progress = null;
  let match_result = null;
  let match_running = false;
  let match_force = false;
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
      selected_set = {};
      for (const library of libraries) {
        if (library.enabled) selected_set[library.name] = true;
      }
      selected_set = { ...selected_set };
    } catch (err) {
      scan_error = err.message;
    }
  }

  function toggle_all() {
    if (all_selected) {
      selected_set = {};
      return;
    }
    const next = {};
    for (const library of libraries) next[library.name] = true;
    selected_set = next;
  }

  async function start_scan() {
    run_scan(false);
  }

  async function start_force_scan() {
    run_scan(true);
  }

  function clear_scan_panel() {
    scan_progress = null;
    scan_result = null;
    scan_error = '';
  }

  function clear_match_panel() {
    match_progress = null;
    match_result = null;
    match_error = '';
  }

  // Starting a new action hides the previous action's progress and results, so
  // the page only shows the run the user just triggered. The chain's internal
  // match is not a new action — it belongs to the Scan & Match run already on
  // screen — so it clears only its own panel and keeps the chain's scan results.
  function clear_previous_action() {
    clear_scan_panel();
    clear_match_panel();
  }

  async function run_scan(force) {
    clear_previous_action();
    scan_force = force;
    try {
      await api_send('/api/v1/scan', 'POST', { libraries: selected.slice(), force });
      scan_running = true;
    } catch (err) {
      scan_force = false;
      scan_error = err.message;
    }
  }

  // from_chain marks the match kicked off by Scan & Match. That run already
  // showed its scan results, so they are kept; a directly triggered match hides
  // them instead (see clear_previous_action).
  async function start_match(from_chain) {
    chain_match = false;
    if (from_chain) {
      clear_match_panel();
    } else {
      clear_previous_action();
    }
    match_force = false;
    try {
      await api_send('/api/v1/match', 'POST', { libraries: selected.slice() });
      match_running = true;
    } catch (err) {
      match_error = err.message;
    }
  }

  async function start_force_rematch() {
    clear_previous_action();
    match_force = true;
    try {
      await api_send('/api/v1/match/rematch', 'POST', { libraries: selected.slice(), force: true });
      match_running = true;
    } catch (err) {
      match_force = false;
      match_error = err.message;
    }
  }

  async function start_chain() {
    clear_previous_action();
    chain_match = true;
    scan_force = false;
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
    scan_source.addEventListener('done', async () => {
      scan_force = false;
      // Per-library scans each emit their own "done"; re-derive the running
      // state from the aggregated status so we only clear busy (and chain the
      // match) once the last selected library has finished.
      await refresh_scan();
      if (!scan_running && chain_match) {
        start_match(true);
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
    match_source.addEventListener('done', async () => {
      match_force = false;
      // Like the scan stream, each per-library match emits its own "done"; the
      // aggregated status clears busy only once the last library finishes.
      await refresh_match();
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
        {all_selected ? 'Select none' : 'Select all'}
      </button>
      <span class="spacer"></span>
      <button on:click={() => start_match(false)} disabled={busy || selected.length === 0} title="Look up titles for entries that are not matched yet. Entries that are already matched are left alone.">
        {match_running && !match_force ? 'Matching…' : 'Match'}
      </button>
      <button on:click={start_force_rematch} disabled={busy || selected.length === 0} title="Drop every match found so far for the selected libraries and run the matcher over everything from scratch. Use after a matcher change.">
        {match_running && match_force ? 'Rematching…' : 'Force rematch'}
      </button>
      <button on:click={start_scan} disabled={busy || selected.length === 0}>
        {scan_running && !scan_force ? 'Scanning…' : `Scan ${selected.length === 1 ? 'library' : 'libraries'}`}
      </button>
      <button
        on:click={start_force_scan}
        disabled={busy || selected.length === 0}
        title="Drop all matches and files known for the selected libraries and scan them from scratch. Use when a smart scan got it wrong."
      >
        {scan_running && scan_force ? 'Rebuilding…' : 'Force rescan'}
      </button>
      <button on:click={start_chain} disabled={busy || selected.length === 0} title="Run a smart scan, then match any entries it added, in one go.">
        {chain_match ? 'Scan & Match…' : 'Scan & Match'}
      </button>
    </div>
    <div class="list">
      {#each libraries as library}
        <label class="lib" class:checked={!!selected_set[library.name]}>
          <input type="checkbox" bind:checked={selected_set[library.name]} on:change={() => { selected_set = { ...selected_set }; }} disabled={busy} />
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
    flex: 1 1 33%;
    min-width: 0;
  }

  .lib .path {
    flex: 2 1 67%;
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