<script>
  import { onMount, onDestroy } from 'svelte';
  import { api_get, api_send } from '../api.js';

  let progress = null;
  let result = null;
  let running = false;
  let error = '';
  let source = null;

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

  async function start() {
    error = '';
    result = null;
    try {
      await api_send('/api/v1/scan', 'POST', {});
      running = true;
    } catch (err) {
      error = err.message;
    }
  }

  onMount(() => {
    refresh();
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
    ['matched', 'Matched'],
    ['unmatched', 'Unmatched'],
    ['skipped', 'Skipped'],
    ['errors', 'Errors'],
  ];
</script>

<section>
  <button on:click={start} disabled={running}>{running ? 'Scanning…' : 'Start scan'}</button>
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
      {result.found} found, {result.new} new, {result.matched} matched,
      {result.unmatched} unmatched, {result.skipped} skipped, {result.missing} missing.
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
  .phase {
    margin-top: 1.5rem;
  }

  .muted {
    color: var(--muted);
  }

  .error {
    color: var(--bad);
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

  .errors {
    color: var(--bad);
    font-size: 0.85rem;
  }
</style>
