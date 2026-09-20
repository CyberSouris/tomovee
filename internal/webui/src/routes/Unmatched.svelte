<script>
  import { onMount } from 'svelte';
  import { api_get, api_send } from '../api.js';
  import TitleSearch from '../TitleSearch.svelte';

  const PAGE = 100;

  let entries = [];
  let total = 0;
  let offset = 0;
  let error = '';
  let loading = true;
  let busy = 0;
  let row_error = '';
  let inputs = {};

  async function load(reset = false) {
    if (reset) {
      offset = 0;
      entries = [];
    }
    loading = true;
    error = '';
    try {
      const data = await api_get('/api/v1/unmatched?limit=' + PAGE + '&offset=' + offset);
      const page = data.entries || [];
      entries = entries.concat(page);
      total = data.total || 0;
      offset += page.length;
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  async function submit_match(entry, body) {
    busy = entry.id;
    row_error = '';
    try {
      await api_send('/api/v1/catalog/' + entry.id + '/match', 'POST', body);
      await load(true);
    } catch (err) {
      row_error = entry.title + ': ' + err.message;
    } finally {
      busy = 0;
    }
  }

  function match(entry) {
    const value = (inputs[entry.id] || '').trim();
    if (!value || busy === entry.id) return;
    submit_match(entry, value.startsWith('tt')
      ? { imdb_id: value, media_type: entry.media_type }
      : { tmdb_id: Number(value), media_type: entry.media_type });
  }

  function match_candidate(entry, event) {
    const candidate = event.detail;
    const body = candidate.tmdb_id
      ? { tmdb_id: candidate.tmdb_id, media_type: candidate.media_type }
      : candidate.imdb_id
        ? { imdb_id: candidate.imdb_id, media_type: candidate.media_type }
        : null;
    if (body) submit_match(entry, body);
  }

  function pick(entry, candidate) {
    const body = candidate.tmdb_id
      ? { tmdb_id: candidate.tmdb_id, media_type: candidate.media_type }
      : candidate.imdb_id
        ? { imdb_id: candidate.imdb_id, media_type: candidate.media_type }
        : null;
    if (body) submit_match(entry, body);
  }

  onMount(() => load(true));
</script>

<section>
  <p class="muted">
    {total} unmatched title{total === 1 ? '' : 's'}. Search by title and pick a
    suggestion, or enter a TMDB id / IMDb id (tt…) and confirm.
  </p>

  {#if error}<p class="error">{error}</p>{/if}
  {#if row_error}<p class="error">{row_error}</p>{/if}
  {#if loading && entries.length === 0}
    <p>Loading…</p>
  {:else if entries.length === 0}
    <p>Nothing to review.</p>
  {:else}
    <table>
      <thead>
        <tr>
          <th>Title</th>
          <th>Type</th>
          <th>Year</th>
          <th>Search / id</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#each entries as entry (entry.id)}
          <tr>
            <td><a href={'#/title/' + entry.id}>{entry.title}</a></td>
            <td>{entry.media_type}</td>
            <td>{entry.release_year || '—'}</td>
            <td>
              <TitleSearch
                bind:query={inputs[entry.id]}
                year={entry.release_year || ''}
                media_type={entry.media_type}
                placeholder="Search title…"
                on:select={(event) => match_candidate(entry, event)}
              />
              {#if entry.candidates && entry.candidates.length}
                <p class="chooser-label">Suggestions:</p>
                <ul class="chooser">
                  {#each entry.candidates as candidate}
                    <li>
                      <button on:click={() => pick(entry, candidate)} disabled={busy === entry.id}>
                        {candidate.title}{candidate.year ? ` (${candidate.year})` : ''}
                      </button>
                    </li>
                  {/each}
                </ul>
              {/if}
            </td>
            <td>
              <button on:click={() => match(entry)} disabled={busy === entry.id}>Match</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if entries.length < total}
      <p class="more">
        <button on:click={() => load(false)} disabled={loading}>Load more</button>
      </p>
    {/if}
  {/if}
</section>

<style>
  .muted {
    color: var(--muted);
  }

  .error {
    color: var(--bad);
  }

  .chooser-label {
    margin: 0.5rem 0 0.25rem;
    font-size: 0.75rem;
    color: var(--muted);
  }

  .chooser {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
  }

  .chooser button {
    font-size: 0.8rem;
    padding: 0.2rem 0.55rem;
  }
</style>
