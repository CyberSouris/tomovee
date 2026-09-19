<script>
  import { onMount } from 'svelte';
  import { api_get, api_send } from '../api.js';

  let settings = null;
  let error = '';
  let saving = false;
  let saved = false;
  let prune_message = '';

  async function load() {
    try {
      settings = normalize(await api_get('/api/v1/settings'));
    } catch (err) {
      error = err.message;
    }
  }

  function normalize(data) {
    data.libraries = data.libraries || [];
    return data;
  }

  async function prune() {
    prune_message = '';
    error = '';
    try {
      const data = await api_send('/api/v1/posters/prune', 'POST', {});
      prune_message = `Removed ${data.removed} cached poster${data.removed === 1 ? '' : 's'}.`;
    } catch (err) {
      error = err.message;
    }
  }

  async function save() {
    saving = true;
    saved = false;
    error = '';
    try {
      settings = normalize(
        await api_send('/api/v1/settings', 'PUT', {
          watch_enabled: settings.watch_enabled,
          libraries: settings.libraries.map((library) => ({
            name: library.name,
            path: library.path,
            enabled: library.enabled,
          })),
        }),
      );
      saved = true;
    } catch (err) {
      error = err.message;
    } finally {
      saving = false;
    }
  }

  onMount(load);
</script>

<section>
  {#if error}<p class="error">{error}</p>{/if}
  {#if !settings}
    <p>Loading…</p>
  {:else}
    <h2>Library</h2>
    <table>
      <tbody>
        <tr><th>Database</th><td>{settings.database_path}</td></tr>
        <tr><th>Poster cache</th><td>{settings.poster_cache_dir}</td></tr>
        <tr><th>Listen</th><td>{settings.listen}</td></tr>
        <tr>
          <th>IMDb datasets</th>
          <td>
            {settings.imdb_datasets_path || '—'}
            <div class="muted help">
              Download <a href="https://datasets.imdbws.com/title.basics.tsv.gz" target="_blank" rel="noreferrer">title.basics.tsv.gz</a>
              and set <code>imdb_datasets_path</code> to it
              (<a href="https://developer.imdb.com/non-commercial-datasets/" target="_blank" rel="noreferrer">dataset details</a>).
            </div>
          </td>
        </tr>
      </tbody>
    </table>

    <h2>APIs</h2>
    <p>
      TMDB:
      <span class:good={settings.tmdb_configured} class:bad={!settings.tmdb_configured}>
        {settings.tmdb_configured ? 'configured' : 'not configured'}
      </span>
      · OpenSubtitles:
      <span class:good={settings.opensubtitles_configured} class:bad={!settings.opensubtitles_configured}>
        {settings.opensubtitles_configured ? 'configured' : 'not configured'}
      </span>
      · Matching:
      <span class:good={settings.matching_ready} class:bad={!settings.matching_ready}>
        {settings.matching_ready ? 'ready' : 'not ready'}
      </span>
    </p>
    {#if !settings.matching_ready}
      <ul class="help">
        {#if settings.datasets && settings.datasets.state === 'building'}
          <li>
            The local IMDb index is <strong>building in the background</strong>
            <span class="progress">
              {settings.datasets.percent}%{#if settings.datasets.dataset} — importing {settings.datasets.dataset}{/if}
            </span>.
            Matching becomes available automatically once it finishes; no action needed.
          </li>
        {:else if settings.imdb_datasets_path && settings.datasets && settings.datasets.state === 'error'}
          <li>
            The local IMDb index <strong>failed to load</strong>
            ({settings.datasets.message || 'see server log'}). Check the <code>tomovee serve</code> log.
          </li>
        {:else if settings.imdb_datasets_path}
          <li>
            The local IMDb index is <strong>paused or not loaded</strong>. Matching becomes
            available once it finishes loading; no action needed.
          </li>
        {/if}
        <li>
          Matching has no sources until at least one of the above is configured
          and <strong>ready</strong>. After changing the config file, restart <code>tomovee serve</code>.
        </li>
      </ul>
    {/if}
    <ul class="help">
      <li>
        <strong>TMDB</strong> — request a key at
        <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">themoviedb.org → Settings → API</a>,
        then set <code>api.tmdb_key</code> in the config file.
      </li>
      <li>
        <strong>OpenSubtitles</strong> — create a key under
        <a href="https://www.opensubtitles.com/en/consumers" target="_blank" rel="noreferrer">opensubtitles.com → Account → API consumers</a>,
        then set <code>api.opensubtitles_api_key</code>, <code>api.opensubtitles_username</code> and
        <code>api.opensubtitles_password</code>.
      </li>
    </ul>

    <h2>Watching</h2>
    <label class="toggle">
      <input type="checkbox" bind:checked={settings.watch_enabled} />
      Enable folder watching
    </label>

    {#if settings.libraries.length}
      <table>
        <thead>
          <tr><th>Name</th><th>Path</th><th>Enabled</th><th>Last scan</th></tr>
        </thead>
        <tbody>
          {#each settings.libraries as library}
            <tr>
              <td>{library.name}</td>
              <td>{library.path}</td>
              <td><input type="checkbox" bind:checked={library.enabled} /></td>
              <td class="muted">{library.last_scan || '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      <p class="muted help">
        Libraries and their paths come from the <code>libraries</code> config map.
      </p>
    {:else}
      <p class="muted">
        No libraries yet. Add a <code>libraries</code> map to the config file.
      </p>
    {/if}

    <p>
      <button on:click={save} disabled={saving}>Save</button>
      {#if saved}<span class="good"> Saved.</span>{/if}
    </p>

    <h2>Poster cache</h2>
    <p class="muted">Remove cached images that no longer belong to a catalogue entry.</p>
    <p>
      <button class="secondary" on:click={prune}>Prune poster cache</button>
      {#if prune_message}<span class="good"> {prune_message}</span>{/if}
    </p>
  {/if}
</section>

<style>
  .muted {
    color: var(--muted);
  }

  .error {
    color: var(--bad);
  }

  .good {
    color: var(--good);
  }

  .bad {
    color: var(--bad);
  }

  .toggle {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0.5rem 0;
  }

  .help {
    color: var(--muted);
    font-size: 0.9rem;
    padding-left: 1.2rem;
  }

  .help li {
    margin: 0.3rem 0;
  }

  code {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 0 0.25rem;
    font-size: 0.85em;
  }

  h2 {
    margin-top: 1.8rem;
  }
</style>