<script>
  import { onMount } from 'svelte';
  import { api_get, api_send, format_bytes, format_duration } from '../api.js';
  import TitleSearch from '../TitleSearch.svelte';

  export let id;

  let data = null;
  let error = '';
  let loading = true;
  let search_query = '';
  let manual_id = '';
  let busy = false;
  let manual_error = '';
  let manual_ok = '';

  async function load() {
    loading = true;
    error = '';
    try {
      const detail = await api_get('/api/v1/catalog/' + encodeURIComponent(id));
      detail.entry.genres = detail.entry.genres || [];
      detail.versions = (detail.versions || []).map(normalize_version);
      detail.episodes = (detail.episodes || []).map((episode) => ({
        ...episode,
        versions: (episode.versions || []).map(normalize_version),
      }));
      data = detail;
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  function normalize_version(version) {
    return { ...version, audio: version.audio || [], subtitles: version.subtitles || [] };
  }

  function tracks_label(tracks) {
    if (!tracks || tracks.length === 0) return '—';
    return [...new Set(tracks.map((track) => track.language || '?'))].join(', ');
  }

  async function match_body(body) {
    busy = true;
    manual_error = '';
    manual_ok = '';
    try {
      await api_send('/api/v1/catalog/' + data.entry.id + '/match', 'POST', body);
      manual_ok = 'Matched.';
      await load();
    } catch (err) {
      manual_error = err.message;
    } finally {
      busy = false;
    }
  }

  function match_manual() {
    const value = manual_id.trim();
    if (!value || busy) return;
    const body = value.startsWith('tt')
      ? { imdb_id: value, media_type: data.entry.media_type }
      : { tmdb_id: Number(value), media_type: data.entry.media_type };
    match_body(body);
  }

  function on_candidate(event) {
    const candidate = event.detail;
    const body = candidate.tmdb_id
      ? { tmdb_id: candidate.tmdb_id, media_type: candidate.media_type }
      : candidate.imdb_id
        ? { imdb_id: candidate.imdb_id, media_type: candidate.media_type }
        : null;
    if (body) match_body(body);
  }

  function enter_match(event) {
    if (event.key === 'Enter') match_manual();
  }

  onMount(load);
</script>

{#if loading}
  <p>Loading…</p>
{:else if error}
  <p class="error">{error}</p>
{:else if data}
  <article>
    <div class="poster">
      {#if data.entry.poster_url}
        <img src={data.entry.poster_url} alt={data.entry.title} />
      {/if}
    </div>
    <div class="info">
      <h1>
        {data.entry.title}
        {#if data.entry.release_year}<span class="muted">({data.entry.release_year})</span>{/if}
      </h1>
      {#if data.entry.original_title && data.entry.original_title !== data.entry.title}
        <p class="muted">{data.entry.original_title}</p>
      {/if}
      <p class="badges">
        <span class="badge">{data.entry.media_type === 'series' ? 'Series' : 'Movie'}</span>
        {#if data.entry.rating}<span class="badge">★ {data.entry.rating.toFixed(1)}</span>{/if}
        {#if data.entry.runtime_minutes}
          <span class="badge">{format_duration(data.entry.runtime_minutes * 60)}</span>
        {/if}
        {#if data.entry.status !== 'matched'}
          <span class="badge warn">{data.entry.status}</span>
        {/if}
      </p>
      {#if data.entry.genres && data.entry.genres.length}
        <p class="muted">{data.entry.genres.join(' · ')}</p>
      {/if}
      {#if data.entry.imdb_id || data.entry.tmdb_id}
        <p class="ids muted">
          {#if data.entry.tmdb_id}
            <a href={'https://www.themoviedb.org/' + data.entry.media_type + '/' + data.entry.tmdb_id} target="_blank" rel="noreferrer">TMDB</a>
          {/if}
          {#if data.entry.imdb_id}
            <a href={'https://www.imdb.com/title/' + data.entry.imdb_id} target="_blank" rel="noreferrer">IMDb</a>
          {/if}
        </p>
      {/if}
      {#if data.entry.overview}
        <p class="overview">{data.entry.overview}</p>
      {/if}
    </div>
  </article>

  <section class="manual">
    <h2>{data.entry.status === 'matched' ? 'Wrong match? Re-match' : 'Match manually'}</h2>
    <p class="muted">
      {data.entry.status === 'matched'
        ? 'Automatic matching picked this title but it looks wrong? Search by title and pick a suggestion, or enter a TMDB / IMDb id directly.'
        : 'Automatic matching did not attach metadata to this title. Search by title and pick a suggestion, or enter a TMDB / IMDb id directly.'}
    </p>
    <TitleSearch
      bind:query={search_query}
      year={data.entry.release_year || ''}
      media_type={data.entry.media_type}
      placeholder="Search by title…"
      on:select={on_candidate}
    />
    <p class="divider">or enter a TMDB id (603) or IMDb id (tt0133093)</p>
    <div class="idrow">
      <input
        placeholder="603 or tt0133093"
        bind:value={manual_id}
        disabled={busy}
        on:keydown={enter_match}
      />
      <button on:click={match_manual} disabled={busy || !manual_id.trim()}>
        {busy ? 'Matching…' : 'Match'}
      </button>
    </div>
    {#if manual_error}<p class="result error">{manual_error}</p>{/if}
    {#if manual_ok}<p class="result ok">{manual_ok}</p>{/if}
  </section>

  {#if data.entry.media_type === 'series'}
    <h2>Episodes ({data.episodes.length})</h2>
    {#if data.episodes.length === 0}
      <p class="muted">No episodes recorded.</p>
    {:else}
      {#each data.episodes as episode (episode.id)}
        <div class="episode">
          <strong>S{String(episode.season_number).padStart(2, '0')}E{String(episode.episode_number).padStart(2, '0')}</strong>
          <span>{episode.title || ''}</span>
          <span class="muted">{episode.versions.length} version{episode.versions.length === 1 ? '' : 's'}</span>
          {#if episode.status !== 'matched'}
            <span class="badge warn">{episode.status}</span>
          {/if}
        </div>
      {/each}
    {/if}
    <h2>Series versions ({data.versions.length})</h2>
  {:else}
    <h2>Versions ({data.versions.length})</h2>
  {/if}

  {#if data.versions.length > 0}
    <table>
      <thead>
        <tr>
          <th>File</th>
          <th>Resolution</th>
          <th>Video</th>
          <th>Size</th>
          <th>Duration</th>
          <th>Audio</th>
          <th>Subtitles</th>
        </tr>
      </thead>
      <tbody>
        {#each data.versions as version (version.id)}
          <tr>
            <td class="path" title={version.file_path}>{version.file_path.split('/').pop()}</td>
            <td>
              {version.resolution_label || ''}
              {#if version.hdr}<span class="badge">HDR</span>{/if}
            </td>
            <td>{version.video_codec || '—'}</td>
            <td>{format_bytes(version.size_bytes)}</td>
            <td>{format_duration(version.duration_seconds)}</td>
            <td>{tracks_label(version.audio)}</td>
            <td>{tracks_label(version.subtitles)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{/if}

<style>
  article {
    display: flex;
    gap: 1.5rem;
    margin-bottom: 2rem;
  }

  .poster img {
    width: 200px;
    border-radius: 10px;
  }

  h1 {
    margin: 0 0 0.3rem;
  }

  .muted {
    color: var(--muted);
  }

  .error {
    color: var(--bad);
  }

  .manual {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 1rem 1.2rem;
    margin-bottom: 2rem;
  }

  .manual h2 {
    margin: 0 0 0.3rem;
  }

  .manual h2 + .muted {
    margin: 0;
  }

  .manual .divider {
    margin: 0.9rem 0 0.4rem;
    font-size: 0.8rem;
    color: var(--muted);
  }

  .manual .idrow {
    display: flex;
    gap: 0.6rem;
  }

  .manual .idrow input {
    flex: 1;
    max-width: 16rem;
  }

  .result {
    margin: 0.6rem 0 0;
    font-size: 0.85rem;
  }

  .ok {
    color: var(--good);
  }

  .badges {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .badge {
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 0.1rem 0.45rem;
    font-size: 0.78rem;
  }

  .badge.warn {
    color: var(--warn);
    border-color: var(--warn);
  }

  .ids {
    display: flex;
    gap: 1rem;
  }

  .overview {
    max-width: 65ch;
    line-height: 1.5;
  }

  .episode {
    display: flex;
    gap: 1rem;
    align-items: center;
    padding: 0.4rem 0;
    border-bottom: 1px solid var(--border);
    font-size: 0.9rem;
  }

  .path {
    max-width: 18rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
