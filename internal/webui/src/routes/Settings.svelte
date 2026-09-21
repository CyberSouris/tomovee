<script>
  import { onMount } from 'svelte';
  import { api_get, api_send } from '../api.js';

  let settings = null;
  let error = '';
  let saving = false;
  let reloading = false;
  let reload_message = '';
  let saved = false;
  let downloading = false;
  let download_message = '';
  let prune_message = '';
  let new_name = '';
  let new_path = '';

  async function load() {
    try {
      settings = normalize(await api_get('/api/v1/settings'));
    } catch (err) {
      error = err.message;
    }
  }

  function normalize(data) {
    data.libraries = data.libraries || [];
    data.tmdb_key = data.tmdb_key || '';
    data.opensubtitles_api_key = data.opensubtitles_api_key || '';
    data.opensubtitles_username = data.opensubtitles_username || '';
    data.opensubtitles_password = data.opensubtitles_password || '';
    return data;
  }

  function building() {
    return !!settings && !!settings.datasets && settings.datasets.state === 'building';
  }

  function datasets_label(d) {
    if (d.step === 'fts') return 'Building full-text search index…';
    if (d.step === 'stale' || d.step === 'import')
      return d.dataset ? `Downloading & importing ${d.dataset}…` : 'Downloading & importing IMDb data…';
    if (d.dataset) return `Importing ${d.dataset}…`;
    return 'Building the IMDb index…';
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

  async function download_datasets() {
    downloading = true;
    download_message = '';
    error = '';
    try {
      const data = await api_send('/api/v1/datasets', 'POST', {});
      download_message = `Import started into ${data.path}.`;
      await load();
    } catch (err) {
      error = err.message;
    } finally {
      downloading = false;
    }
  }

  async function add_library() {
    const name = new_name.trim();
    const path = new_path.trim();
    if (!name || !path) return;
    settings.libraries = [...settings.libraries, { name, path, enabled: true }];
    new_name = '';
    new_path = '';
    await save();
  }

  async function remove_library(name) {
    error = '';
    try {
      await api_send(`/api/v1/libraries/${encodeURIComponent(name)}`, 'DELETE', {});
    } catch (err) {
      error = err.message;
    } finally {
      await load();
    }
  }

  function enter_add(event) {
    if (event.key === 'Enter') add_library();
  }

  async function save() {
    saving = true;
    saved = false;
    error = '';
    try {
      settings = normalize(
        await api_send('/api/v1/settings', 'PUT', {
          watch_enabled: settings.watch_enabled,
          tmdb_key: settings.tmdb_key,
          opensubtitles_api_key: settings.opensubtitles_api_key,
          opensubtitles_username: settings.opensubtitles_username,
          opensubtitles_password: settings.opensubtitles_password,
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

  async function reload_sources() {
    reloading = true;
    reload_message = '';
    error = '';
    try {
      settings = normalize(await api_send('/api/v1/settings/reload', 'POST', {}));
      reload_message = 'Sources applied to the running server.';
    } catch (err) {
      error = err.message;
    } finally {
      reloading = false;
    }
  }

  onMount(() => {
    load();
    const interval = setInterval(() => {
      if (building()) load();
    }, 1000);
    return () => clearInterval(interval);
  });
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
      </tbody>
    </table>

    <h2>Sources</h2>
    <p class="muted">
      Source settings are saved to the database and applied the next time
      <code>tomovee serve</code> starts; no need to edit the config file by hand.
    </p>
    <div class="field">
      <label for="tmdb">TMDB API key</label>
      <input id="tmdb" type="password" bind:value={settings.tmdb_key} placeholder="tmdb key" />
    </div>
    <div class="field">
      <label for="os-key">OpenSubtitles API key</label>
      <input id="os-key" type="password" bind:value={settings.opensubtitles_api_key} placeholder="opensubtitles key" />
    </div>
    <div class="field">
      <label for="os-user">OpenSubtitles username</label>
      <input id="os-user" type="text" bind:value={settings.opensubtitles_username} placeholder="username" />
    </div>
    <div class="field">
      <label for="os-pass">OpenSubtitles password</label>
      <input id="os-pass" type="password" bind:value={settings.opensubtitles_password} placeholder="password" />
    </div>

    <h2>IMDb datasets</h2>
    <p class="muted">
      Stream the official IMDb exports (<code>title.basics</code>,
      <code>title.akas</code>, <code>title.episode</code>, <code>title.ratings</code>)
      into the offline matching index in the background. The exports are imported
      on the fly and never kept on disk; only the built index is stored.
    </p>
    <p class="muted">
      The datasets total several gigabytes even compressed (roughly 10–15 GB
      unpacked), so the first import can take a really long time. Progress is
      shown below and in a corner notification.
    </p>
    {#if settings.datasets && settings.datasets.state === 'building'}
      <div class="datasets-status">
        <p>{datasets_label(settings.datasets)}</p>
        <div class="bar"><div class="fill" style={`width: ${settings.datasets.percent}%`}></div></div>
        <p class="muted">
          {settings.datasets.percent}%{#if settings.datasets.dataset} — {settings.datasets.dataset}{/if}
        </p>
      </div>
    {:else if settings.datasets && settings.datasets.state === 'error'}
      <p class="error">
        The IMDb datasets failed to load ({settings.datasets.message || 'see server log'}).
      </p>
    {:else if !settings.datasets || settings.datasets.state === 'idle'}
      <p class="muted">
        The IMDb index is not built yet. Pressing the button below streams the
        dataset exports from IMDb and imports them into the index in the
        background.
      </p>
    {:else}
      <p class="good">The local IMDb index is ready.</p>
    {/if}
    <p>
      <button on:click={download_datasets} disabled={downloading || building()}>
        {building() ? 'Importing / building…' : 'Download and import IMDb datasets'}
      </button>
      {#if download_message}<span class="good"> {download_message}</span>{/if}
    </p>
    <p class="help attribution">
      Information courtesy of IMDb (<a href="https://www.imdb.com" target="_blank" rel="noreferrer">https://www.imdb.com</a>).
      Used with permission. IMDb data is for non-commercial use only.
    </p>

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
    <ul class="help">
      <li>
        Save stores the sources; Reload applies them to the running server
        immediately, no restart needed.
      </li>
      <li>
        <strong>TMDB</strong> — request a key at
        <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">themoviedb.org → Settings → API</a>,
        then paste it into the TMDB field above.
      </li>
      <li>
        <strong>OpenSubtitles</strong> — create a key under
        <a href="https://www.opensubtitles.com/en/consumers" target="_blank" rel="noreferrer">opensubtitles.com → Account → API consumers</a>,
        then paste the key, username and password above.
      </li>
    </ul>
    <p>
      <button class="secondary" on:click={reload_sources} disabled={reloading}>
        {reloading ? 'Reloading…' : 'Reload sources'}
      </button>
      {#if reload_message}<span class="good"> {reload_message}</span>{/if}
    </p>

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
              <td class="actions"></td>
              <td class="actions">
                <button class="remove" on:click={() => remove_library(library.name)} aria-label={`Remove ${library.name}`}>Remove</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="muted">No libraries yet — add the first one below.</p>
    {/if}

    <div class="add-library">
      <input type="text" placeholder="Name" bind:value={new_name} on:keydown={enter_add} />
      <input type="text" placeholder="/path/to/movies" bind:value={new_path} on:keydown={enter_add} />
      <button class="secondary" on:click={add_library} disabled={!new_name.trim() || !new_path.trim()}>
        Add library
      </button>
    </div>
    <p class="muted help">
      New libraries are enabled (watched) by default and are scanned by the
      folder watcher. Click Save to persist; enabled libraries are picked up on
      the next watch scan. Initial libraries from the config file stay listed here.
    </p>

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

  .add-library {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.8rem;
  }

  .add-library input[type='text'] {
    flex: 1;
    max-width: 18rem;
  }

  .actions {
    white-space: nowrap;
  }
  
  .remove {
    color: var(--bad);
    border-color: var(--bad);
    font-size: 0.8rem;
    padding: 0.2rem 0.6rem;
  }
  
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    margin: 0.5rem 0;
    max-width: 36rem;
  }

  .field label {
    font-size: 0.85rem;
    color: var(--muted);
  }

  input[type='text'],
  input[type='password'] {
    padding: 0.3rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--panel);
    color: var(--text);
  }

  .datasets-status {
    margin: 0.5rem 0;
  }

  .bar {
    height: 0.6rem;
    border-radius: 999px;
    background: var(--panel);
    border: 1px solid var(--border);
    overflow: hidden;
    max-width: 36rem;
  }

  .fill {
    height: 100%;
    background: var(--accent);
    transition: width 0.4s ease;
  }

  .help {
    color: var(--muted);
    font-size: 0.9rem;
    padding-left: 1.2rem;
  }

  .help li {
    margin: 0.3rem 0;
  }

  .attribution {
    padding-left: 0;
    margin: 0.5rem 0 0;
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