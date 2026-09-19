<script>
  import { onMount, onDestroy } from 'svelte';
  import { api_get, api_send } from '../api.js';

  let progress = null;
  let result = null;
  let running = false;
  let error = '';
  let source = null;
  let libraries = [];
  let selected = [];

  async function refresh() {
    try {
      const data = await api_get('/api/v1/scan/status');
      if (data.job) {
        progress = data.job.progress;
        running = data.job.running;
        result = data.job.result || null;
      }
    } catch (err) {
      error = err.message;
    }
  }

  async function load_libraries() {
    try {
      const data = await api_get('/api/v1/settings');
      libraries = data.libraries || [];
      selected = libraries.filter((library) => library.enabled).map((library) => library.name);
    } catch (err) {
      error = err.message;
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

  async function start() {
    error = '';
    result = null;
    try {
      await api_send('/api/v1/scan', 'POST', { libraries: selected.slice() });
      running = true;
    } catch (err) {
      error = err.message;
    }
  }

  onMount(() => {
    refresh();
    load_libraries();
    source = new EventSource('/api/v1/scan/stream');
    source.addEventListener('status', (event) => {
      const data = JSON.parse(event.data);
      if (data.job) {
        progress = data.job.progress;
        running = data.job.running;
        result = data.job.result || null;
      }
    });
    source.addEventListener('progress', (event) => {
      progress = JSON.parse(event.data).progress;
      running = true;
    });
    source.addEventListener('done', () => {
      running = false;
      refresh();
    });
    return () => source && source.close();
  });

  onDestroy(() => source && source.close());

  const counters = [
    ['files_found', 'Found'],
    ['files_scanned', 'Scanned'],
    ['new_files', 'New'],
    ['skipped', 'Skipped'],
    ['errors', 'Errors'],
  ];
</script>

<section>
  {#if libraries.length}
    <div class="controls">
      <button class="secondary" on:click={toggle_all} disabled={running || libraries.length === 0}>
        {all_selected() ? 'Select none' : 'Select all'}
      </button>
      <span class="spacer"></span>
      <button on:click={start} disabled={running || selected.length === 0}>
        {running ? 'Scanning…' : `Scan ${selected.length === 1 ? 'library' : 'libraries'}`}
      </button>
    </div>
    <div class="list">
      {#each libraries as library}
        <label class="lib" class:checked={is_selected(library.name)}>
          <input type="checkbox" checked={is_selected(library.name)} on:change={() => toggle(library.name)} disabled={running} />
          <span class="name">{library.name}</span>
          <span class="path muted">{library.path}</span>
          {#if library.enabled}<span class="badge">watched</span>{/if}
        </label>
      {/each}
    </div>
    <p class="muted help">Only the checked libraries are scanned. Watched libraries are updated by the folder watcher automatically.</p>
  {:else}
    <p class="muted">No libraries configured — add one on the Settings page.</p>
  {/if}
  {#if error}<p class="error">{error}</p>{/if}

  {#if progress}
    <p class="phase">Phase: <strong>{progress.phase}</strong></p>
    <div class="counters">
      {#each counters as [key, label]}
        <div class="counter">
          <span class="value">{progress[key] ?? 0}</span>
          <span class="muted">{label}</span>
        </div>
      {/each}
    </div>
    {#if progress.path}
      <p class="path muted">{progress.path}</p>
    {/if}
  {/if}

  {#if result}
    <h2>Result</h2>
    <p>
      {result.found} found, {result.new} new, {result.skipped} skipped,
      {result.missing} missing.
    </p>
    {#if result.errors && result.errors.length}
      <ul class="errors">
        {#each result.errors as message}
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
    margin-top: 1.5rem;
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

  .path {
    font-size: 0.85rem;
    word-break: break-all;
  }
</style>