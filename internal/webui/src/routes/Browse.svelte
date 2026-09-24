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
  let media_type = '';
  let status = '';
  let sort = 'added';

  let categories = { genres: [], unmatched: 0 };
  let genre = '';
  let category = 'all';
  let libraries = [];
  let library = '';

  async function load_libraries() {
    try {
      libraries = await api_get('/api/v1/libraries');
    } catch (err) {
      libraries = [];
    }
  }

  async function load_categories() {
    try {
      categories = await api_get('/api/v1/categories');
    } catch {
      categories = { genres: [], unmatched: 0 };
    }
  }

  function pick_category(cat, genre_name = '') {
    category = cat;
    genre = genre_name;
    status = cat === 'unmatched' ? 'needs_lookup' : cat === 'genre' ? 'matched' : '';
    load(true);
  }

  async function load(reset = true) {
    loading = true;
    error = '';
    try {
      const params = new URLSearchParams();
      if (q) params.set('q', q);
      if (media_type) params.set('media_type', media_type);
      if (status) params.set('status', status);
      if (genre) params.set('genre', genre);
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
  }

  function submit(event) {
    event.preventDefault();
    load(true);
  }

  onMount(() => {
    load(true);
    load_libraries();
    load_categories();
  });
</script>

<section>
  <div class="chips" role="list" aria-label="Categories">
    <button
      class:active={category === 'all'}
      on:click={() => pick_category('all')}
    >All</button>
    <button
      class:active={category === 'unmatched'}
      on:click={() => pick_category('unmatched')}
    >Unmatched ({categories.unmatched})</button>
    {#each categories.genres as g}
      <button
        class:active={category === 'genre' && genre === g.name}
        on:click={() => pick_category('genre', g.name)}
      >{g.name} ({g.count})</button>
    {/each}
  </div>

  <form on:submit={submit}>
    <input placeholder="Search title…" bind:value={q} />
    <select bind:value={media_type}>
      <option value="">All types</option>
      <option value="movie">Movies</option>
      <option value="series">Series</option>
    </select>
    <select bind:value={library}>
      <option value="">All libraries</option>
      {#each libraries as lib}
        <option value={lib.name}>{lib.name}</option>
      {/each}
    </select>
    <button type="submit" disabled={loading}>Search</button>
  </form>

  <div class="toolbar">
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
        <option value="missing">Missing</option>
      </select>
    </label>
    <label class="toolbar-field">
      <span class="muted">Sort</span>
      <select bind:value={sort} on:change={() => load(true)}>
        <option value="added">Recently added</option>
        <option value="title">Title</option>
        <option value="year">Year</option>
        <option value="rating">Rating</option>
      </select>
    </label>
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
  form {
    display: flex;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin-bottom: 1rem;
  }

  form input {
    flex: 1;
    min-width: 12rem;
  }

  .toolbar {
    display: flex;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin-bottom: 1rem;
  }

  .toolbar-field {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin-bottom: 0.9rem;
  }

  .chips button {
    font-size: 0.82rem;
    padding: 0.3rem 0.75rem;
    border-radius: 999px;
    background: var(--panel);
    border: 1px solid var(--border);
    color: var(--muted);
    cursor: pointer;
  }

  .chips button:hover {
    border-color: var(--accent);
    color: var(--text);
  }

  .chips button.active {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--text);
    font-weight: 600;
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
</style>
