<script>
  import { onMount } from 'svelte';
  import { api_get, poster_image } from '../api.js';

  let entries = [];
  let count = 0;
  let loading = false;
  let error = '';

  let q = '';
  let media_type = '';
  let status = '';
  let sort = 'added';

  async function load() {
    loading = true;
    error = '';
    try {
      const params = new URLSearchParams();
      if (q) params.set('q', q);
      if (media_type) params.set('media_type', media_type);
      if (status) params.set('status', status);
      if (sort) params.set('sort', sort);
      const data = await api_get('/api/v1/catalog?' + params.toString());
      entries = data.entries || [];
      count = data.count || 0;
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  function submit(event) {
    event.preventDefault();
    load();
  }

  onMount(load);
</script>

<section>
  <form on:submit={submit}>
    <input placeholder="Search title…" bind:value={q} />
    <select bind:value={media_type}>
      <option value="">All types</option>
      <option value="movie">Movies</option>
      <option value="series">Series</option>
    </select>
    <select bind:value={status}>
      <option value="">Any status</option>
      <option value="matched">Matched</option>
      <option value="needs_lookup">Needs lookup</option>
      <option value="missing">Missing</option>
    </select>
    <select bind:value={sort}>
      <option value="added">Recently added</option>
      <option value="title">Title</option>
      <option value="year">Year</option>
      <option value="rating">Rating</option>
    </select>
    <button type="submit" disabled={loading}>Search</button>
  </form>

  {#if error}
    <p class="error">{error}</p>
  {/if}

  <p class="muted">{count} title{count === 1 ? '' : 's'}</p>

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
