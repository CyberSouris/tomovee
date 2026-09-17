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
    data.scan_directories = data.scan_directories || [];
    data.watch_folders = data.watch_folders || [];
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
          folders: settings.watch_folders.map((folder) => ({
            path: folder.path,
            enabled: folder.enabled,
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
          <th>Scan directories</th>
          <td>
            {#if settings.scan_directories.length}
              {#each settings.scan_directories as dir}
                <div>{dir}</div>
              {/each}
            {:else}
              <span class="muted">none configured</span>
            {/if}
          </td>
        </tr>
        <tr>
          <th>IMDb datasets</th>
          <td>{settings.imdb_datasets_path || '—'}</td>
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
    </p>

    <h2>Watching</h2>
    <label class="toggle">
      <input type="checkbox" bind:checked={settings.watch_enabled} />
      Enable folder watching
    </label>

    {#if settings.watch_folders.length}
      <table>
        <thead>
          <tr><th>Folder</th><th>Enabled</th><th>Last scan</th></tr>
        </thead>
        <tbody>
          {#each settings.watch_folders as folder}
            <tr>
              <td>{folder.path}</td>
              <td><input type="checkbox" bind:checked={folder.enabled} /></td>
              <td class="muted">{folder.last_scan || '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="muted">
        No watch folders yet. They are registered when a scan discovers files.
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

  h2 {
    margin-top: 1.8rem;
  }
</style>
