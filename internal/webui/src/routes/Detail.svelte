<script>
  import { onMount } from 'svelte';
  import { api_get, format_bytes, format_duration } from '../api.js';

  export let id;

  let data = null;
  let error = '';
  let loading = true;

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
