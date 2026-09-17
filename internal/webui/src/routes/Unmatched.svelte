<script>
  import { onMount } from 'svelte';
  import { api_get, api_send } from '../api.js';

  let entries = [];
  let error = '';
  let loading = true;
  let busy = 0;
  let row_error = '';
  let inputs = {};

  async function load() {
    loading = true;
    error = '';
    try {
      const data = await api_get('/api/v1/unmatched');
      entries = data.entries || [];
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  async function match(entry) {
    const value = (inputs[entry.id] || '').trim();
    if (!value) return;
    busy = entry.id;
    row_error = '';
    const body = value.startsWith('tt')
      ? { imdb_id: value, media_type: entry.media_type }
      : { tmdb_id: Number(value), media_type: entry.media_type };
    try {
      await api_send('/api/v1/catalog/' + entry.id + '/match', 'POST', body);
      await load();
    } catch (err) {
      row_error = entry.title + ': ' + err.message;
    } finally {
      busy = 0;
    }
  }

  onMount(load);
</script>

<section>
  <p class="muted">
    These titles could not be matched automatically. Enter a TMDB id or an IMDb id
    (tt…) and confirm to attach full metadata.
  </p>

  {#if error}<p class="error">{error}</p>{/if}
  {#if row_error}<p class="error">{row_error}</p>{/if}
  {#if loading}
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
          <th>TMDB / IMDb id</th>
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
              <input
                placeholder="603 or tt0133093"
                bind:value={inputs[entry.id]}
                on:keydown={(event) => event.key === 'Enter' && match(entry)}
              />
            </td>
            <td>
              <button on:click={() => match(entry)} disabled={busy === entry.id}>Match</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>

<style>
  .muted {
    color: var(--muted);
  }

  .error {
    color: var(--bad);
  }
</style>
