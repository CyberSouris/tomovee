<script>
  import { onMount } from 'svelte';
  import { api_get, poster_image } from '../api.js';

  const PAGE = 100;

  let entries = [];
  let total = 0;
  let offset = 0;
  let loading = false;
  let error = '';

  let q = '';
  let status = '';
  let sort = 'added';

  let categories = { categories: [], unmatched: 0 };
  let selected_genres = [];
  let selected_type = '';
  let libraries = [];
  let library = '';

  async function load_libraries() {
    try {
      libraries = await api_get('/api/v1/libraries');
    } catch (err) {
      libraries = [];
    }
  }

  // Categories are scoped to the current filter so that "entering" a folder
  // only offers the categories still reachable inside that subset.
  async function load_categories() {
    const params = new URLSearchParams();
    if (q) params.set('q', q);
    if (selected_type) params.set('media_type', selected_type);
    if (status) params.set('status', status);
    for (const name of selected_genres) params.append('genre', name);
    if (library) params.set('library', library);
    const query = params.toString();
    try {
      categories = await api_get('/api/v1/categories' + (query ? '?' + query : ''));
    } catch {
      categories = { categories: [], unmatched: 0 };
    }
  }

  function is_selected(cat) {
    return cat.kind === 'type' ? selected_type === cat.value : selected_genres.includes(cat.value);
  }

  // Clicking a type folder picks that single media type; clicking a genre folder
  // toggles it, so a second genre intersects the current one instead of
  // replacing it.
  function toggle_category(cat) {
    if (cat.kind === 'type') {
      selected_type = selected_type === cat.value ? '' : cat.value;
    } else if (selected_genres.includes(cat.value)) {
      selected_genres = selected_genres.filter((name) => name !== cat.value);
    } else {
      selected_genres = [...selected_genres, cat.value];
    }
    load(true);
  }

  function remove_genre(name) {
    selected_genres = selected_genres.filter((one) => one !== name);
    load(true);
  }

  function remove_type() {
    selected_type = '';
    load(true);
  }

  function clear_categories() {
    selected_genres = [];
    selected_type = '';
    load(true);
  }

  function toggle_unmatched() {
    status = status === 'needs_lookup' ? '' : 'needs_lookup';
    load(true);
  }

  async function load(reset = true) {
    loading = true;
    error = '';
    try {
      const params = new URLSearchParams();
      if (q) params.set('q', q);
      if (selected_type) params.set('media_type', selected_type);
      if (status) params.set('status', status);
      for (const name of selected_genres) params.append('genre', name);
      if (library) params.set('library', library);
      if (sort) params.set('sort', sort);
      params.set('limit', String(PAGE));
      params.set('offset', String(reset ? 0 : offset));
      const data = await api_get('/api/v1/catalog?' + params.toString());
      const page = data.entries || [];
      entries = reset ? page : entries.concat(page);
      total = data.total || 0;
      offset += page.length;
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
    if (reset) await load_categories();
  }

  function submit(event) {
    event.preventDefault();
    load(true);
  }

  onMount(() => {
    load(true);
    load_libraries();
  });
</script>

<section>
  <div class="toolbar">
    <form on:submit={submit}>
      <input placeholder="Search title…" bind:value={q} />
      <button type="submit" disabled={loading}>Search</button>
    </form>
    <label class="toolbar-field">
      <span class="muted">Library</span>
      <select bind:value={library} on:change={() => load(true)}>
        <option value="">All libraries</option>
        {#each libraries as lib}
          <option value={lib.name}>{lib.name}</option>
        {/each}
      </select>
    </label>
    <label class="toolbar-field">
      <span class="muted">Status</span>
      <select bind:value={status} on:change={() => load(true)}>
        <option value="">Any status</option>
        <option value="matched">Matched</option>
        <option value="needs_lookup">Needs lookup</option>
      </select>
    </label>
    <label class="toolbar-field">
      <span class="muted">Sort</span>
      <select bind:value={sort} on:change={() => load(true)}>
        <option value="added">Recently added</option>
        <option value="title">Title</option>
        <option value="year">Year</option>
      </select>
    </label>
    <button type="button" class="chip" class:active-chip={status === 'needs_lookup'} on:click={toggle_unmatched}>
      Unmatched ({categories.unmatched})
    </button>
    {#if selected_genres.length || selected_type}
      <button type="button" class="chip" on:click={clear_categories}>Clear categories</button>
    {/if}
  </div>

  {#if selected_genres.length || selected_type}
    <div class="selection">
      <span class="muted">Filtered by</span>
      {#if selected_type}
        <button type="button" class="pill" on:click={remove_type}>
          {selected_type === 'movie' ? 'Movies' : 'Series'} <span aria-hidden="true">×</span>
        </button>
      {/if}
      {#each selected_genres as name (name)}
        <button type="button" class="pill" on:click={() => remove_genre(name)}>
          {name} <span aria-hidden="true">×</span>
        </button>
      {/each}
    </div>
  {/if}

  <h2 class="section-title">Categories</h2>
  <div class="folders" role="list" aria-label="Categories">
    {#each categories.categories as cat (cat.key)}
      {#if cat.count > 0 && !is_selected(cat)}
        <button
          type="button"
          class="folder"
          on:click={() => toggle_category(cat)}
        >
          <span class="folder-stack" aria-hidden="true">
            <span class="sheet sheet-1"></span>
            <span class="sheet sheet-2"></span>
            <span class="cover">
              {#if cat.poster_url}
                <img src={cat.poster_url} alt="" loading="lazy" />
              {:else}
                <span class="cover-fallback">{cat.name}</span>
              {/if}
            </span>
          </span>
          <span class="folder-label">
            <strong>{cat.name}</strong>
            <span class="muted">{cat.count}</span>
          </span>
        </button>
      {/if}
    {/each}
  </div>

  {#if error}
    <p class="error">{error}</p>
  {/if}

  <p class="muted">
    {total} title{total === 1 ? '' : 's'}{#if entries.length < total} · showing {entries.length}{/if}
  </p>

  <div class="grid">
    {#each entries as entry (entry.id)}
      <a class="card" href={'#/title/' + entry.id}>
        <div class="poster">
          {#if entry.poster_url}
            <img src={poster_image(entry)} alt={entry.title} loading="lazy" />
          {:else}
            <span>{entry.title}</span>
          {/if}
        </div>
        <div class="meta">
          <strong>{entry.title}</strong>
          <span class="muted">
            {entry.media_type === 'series' ? 'Series' : 'Movie'}
            {#if entry.release_year}· {entry.release_year}{/if}
          </span>
          {#if entry.status !== 'matched'}
            <span class="tag warn">{entry.status}</span>
          {/if}
        </div>
      </a>
    {/each}
  </div>

  {#if entries.length < total}
    <p class="more">
      <button on:click={() => load(false)} disabled={loading}>Load more</button>
    </p>
  {/if}
</section>

<style>
  .section-title {
    font-size: 0.8rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--muted);
    margin: 0 0 0.7rem;
  }

  .folders {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 1.4rem 1.1rem;
    margin-bottom: 1.6rem;
  }

  .folder {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0;
    background: transparent;
    border: 0;
    color: var(--text);
    text-align: left;
    cursor: pointer;
  }

  /* The stack sits inset from the tile box so the two "papers" behind the
     cover can peek out at the top-right corner without being clipped. */
  .folder-stack {
    position: relative;
    display: block;
    aspect-ratio: 2 / 3;
    margin: 11px 11px 0 0;
  }

  .sheet {
    position: absolute;
    inset: 0;
    border-radius: 10px;
    background: var(--panel-2);
    border: 1px solid var(--border);
  }

  .sheet-1 {
    transform: translate(10px, -10px) rotate(2.4deg);
  }

  .sheet-2 {
    transform: translate(5px, -5px) rotate(1.2deg);
  }

  .cover {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    border-radius: 10px;
    background: var(--panel);
    border: 1px solid var(--border);
    box-shadow: 0 8px 20px rgba(0, 0, 0, 0.45);
  }

  .cover img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }

  .cover-fallback {
    padding: 0.5rem;
    text-align: center;
    font-weight: 600;
    color: var(--muted);
  }

  .folder:hover .cover {
    border-color: var(--accent);
  }

  .folder-label {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
    font-size: 0.85rem;
  }

  .folder-label strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .toolbar {
    display: flex;
    gap: 0.6rem;
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: 0.9rem;
  }

  .toolbar form {
    display: flex;
    gap: 0.6rem;
    flex: 1 1 18rem;
  }

  .toolbar form input {
    flex: 1;
    min-width: 10rem;
  }

  .toolbar-field {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .chip {
    font-size: 0.82rem;
    padding: 0.4rem 0.75rem;
    border-radius: 999px;
    background: var(--panel);
    border: 1px solid var(--border);
    color: var(--muted);
    cursor: pointer;
  }

  .chip:hover {
    border-color: var(--accent);
    color: var(--text);
  }

  .chip.active-chip {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
    font-weight: 600;
  }

  .selection {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 0.9rem;
  }

  .pill {
    font-size: 0.8rem;
    padding: 0.2rem 0.6rem;
    border-radius: 999px;
    background: var(--panel-2);
    border: 1px solid var(--accent);
    color: var(--text);
    cursor: pointer;
  }

  .muted {
    color: var(--muted);
  }

  .error {
    color: var(--bad);
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 1rem;
  }

  .card {
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .card:hover {
    text-decoration: none;
    border-color: var(--accent);
  }

  .poster {
    aspect-ratio: 2 / 3;
    background: var(--panel-2);
    display: flex;
    align-items: center;
    justify-content: center;
    text-align: center;
    padding: 0.5rem;
  }

  .poster img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }

  .meta {
    padding: 0.6rem;
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.85rem;
  }

  .meta strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .tag {
    align-self: flex-start;
    font-size: 0.7rem;
    padding: 0.1rem 0.4rem;
    border-radius: 4px;
  }

  .tag.warn {
    background: rgba(210, 153, 34, 0.2);
    color: var(--warn);
  }

  .more {
    margin-top: 1rem;
  }
</style>
