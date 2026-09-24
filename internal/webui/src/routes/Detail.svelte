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
  let rematch_busy = false;
  let rematch_error = '';
  let reclassify_busy = false;
  let reclassify_error = '';
  let playing = null;
  let open_episodes = new Set();

  async function load() {
    loading = true;
    error = '';
    try {
      const detail = await api_get('/api/v1/catalog/' + encodeURIComponent(id));
      detail.entry.genres = detail.entry.genres || [];
      detail.entry.candidates = detail.entry.candidates || [];
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

  function file_name(version) {
    return (version.file_path || 'video').split('/').pop();
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


  async function rematch() {
    rematch_busy = true;
    rematch_error = '';
    try {
      await api_send('/api/v1/catalog/' + data.entry.id + '/rematch', 'POST', {});
      await load();
    } catch (err) {
      rematch_error = err.message;
    } finally {
      rematch_busy = false;
    }
  }

  async function reclassify(media_type) {
    reclassify_busy = true;
    reclassify_error = '';
    try {
      await api_send('/api/v1/catalog/' + data.entry.id + '/reclassify', 'POST', { media_type });
      manual_ok = '';
      await load();
    } catch (err) {
      reclassify_error = err.message;
    } finally {
      reclassify_busy = false;
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

  function pick_candidate(candidate) {
    const body = candidate.tmdb_id
      ? { tmdb_id: candidate.tmdb_id, media_type: candidate.media_type }
      : candidate.imdb_id
        ? { imdb_id: candidate.imdb_id, media_type: candidate.media_type }
        : null;
    if (body) match_body(body);
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

  function toggle_episode(id) {
    const next = new Set(open_episodes);
    if (next.has(id)) {
      next.delete(id);
    } else {
      next.add(id);
    }
    open_episodes = next;
  }

  function episode_label(episode) {
    return 'S' + String(episode.season_number).padStart(2, '0') + 'E' + String(episode.episode_number).padStart(2, '0');
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
    <button on:click={rematch} disabled={rematch_busy}>
      {rematch_busy ? 'Retrying automatch…' : 'Retry automatch'}
    </button>
    {#if data.entry.candidates && data.entry.candidates.length}
      <p class="divider">Ambiguous — pick one:</p>
      <ul class="candidates">
        {#each data.entry.candidates as candidate}
          <li>
            <button on:click={() => pick_candidate(candidate)}>
              {candidate.title}{candidate.year ? ` (${candidate.year})` : ''}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
    {#if rematch_error}<p class="result error">{rematch_error}</p>{/if}

    <p class="divider">Type is wrong?</p>
    {#if data.entry.media_type === 'series'}
      <button class="secondary" on:click={() => reclassify('movie')} disabled={reclassify_busy}>
        {reclassify_busy ? 'Reclassifying…' : 'This is a movie, not a series'}
      </button>
    {:else}
      <button class="secondary" on:click={() => reclassify('series')} disabled={reclassify_busy}>
        {reclassify_busy ? 'Reclassifying…' : 'This is a series, not a movie'}
      </button>
    {/if}
    {#if reclassify_error}<p class="result error">{reclassify_error}</p>{/if}

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
          <button
            class="episode-head"
            on:click={() => toggle_episode(episode.id)}
            disabled={episode.versions.length === 0}
            title={episode.versions.length === 0 ? 'No versions for this episode' : 'Show files'}
          >
            <span class="chevron">{episode.versions.length === 0 ? '' : open_episodes.has(episode.id) ? '▾' : '▸'}</span>
            <strong>{episode_label(episode)}</strong>
            <span class="title">{episode.title || ''}</span>
            <span class="muted">{episode.versions.length} version{episode.versions.length === 1 ? '' : 's'}</span>
            {#if episode.status !== 'matched'}
              <span class="badge warn">{episode.status}</span>
            {/if}
          </button>
          {#if open_episodes.has(episode.id)}
            <table>
              <thead>
                <tr>
                  <th>Library</th>
                  <th>File</th>
                  <th>Resolution</th>
                  <th>Video</th>
                  <th>Size</th>
                  <th>Duration</th>
                  <th>Audio</th>
                  <th>Subtitles</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {#each episode.versions as version (version.id)}
                  <tr>
                    <td class="muted">{version.library_name || '—'}</td>
                    <td class="path" title={version.file_path}>{file_name(version)}</td>
                    <td>
                      {version.resolution_label || ''}
                      {#if version.hdr}<span class="badge">HDR</span>{/if}
                    </td>
                    <td>{version.video_codec || '—'}</td>
                    <td>{format_bytes(version.size_bytes)}</td>
                    <td>{format_duration(version.duration_seconds)}</td>
                    <td>{tracks_label(version.audio)}</td>
                    <td>{tracks_label(version.subtitles)}</td>
                    <td class="actions">
                      <button class="secondary" on:click={() => (playing = playing === version ? null : version)}>
                        {playing === version ? 'Playing…' : 'Play'}
                      </button>
                      <a class="download" href={version.file_url} download={file_name(version)}>Download</a>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/each}
    {/if}
    <h2>Series versions ({data.versions.length})</h2>
  {:else}
    <h2>Versions ({data.versions.length})</h2>
  {/if}

  {#if playing}
    <section class="player">
      <div class="player-head">
        <strong>{file_name(playing)}</strong>
        <button class="secondary" on:click={() => (playing = null)}>Close</button>
      </div>
      <!-- svelte-ignore a11y_media_has_caption -->
      <video controls autoplay src={playing.file_url}></video>
      <p class="muted player-note">
        Played in the browser only if the container and codecs are supported
        (MP4/H.264 works in every browser; MKV usually needs either an
        H.264/HEVC+ACC stream or a player like VLC). Otherwise use Download.
      </p>
    </section>
  {/if}

  {#if data.versions.length > 0}
    <table>
      <thead>
        <tr>
          <th>Library</th>
          <th>File</th>
          <th>Resolution</th>
          <th>Video</th>
          <th>Size</th>
          <th>Duration</th>
          <th>Audio</th>
          <th>Subtitles</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        {#each data.versions as version (version.id)}
          <tr>
            <td class="muted">{version.library_name || '—'}</td>
            <td class="path" title={version.file_path}>{file_name(version)}</td>
            <td>
              {version.resolution_label || ''}
              {#if version.hdr}<span class="badge">HDR</span>{/if}
            </td>
            <td>{version.video_codec || '—'}</td>
            <td>{format_bytes(version.size_bytes)}</td>
            <td>{format_duration(version.duration_seconds)}</td>
            <td>{tracks_label(version.audio)}</td>
            <td>{tracks_label(version.subtitles)}</td>
            <td class="actions">
              <button class="secondary" on:click={() => (playing = playing === version ? null : version)}>
                {playing === version ? 'Playing…' : 'Play'}
              </button>
              <a class="download" href={version.file_url} download={file_name(version)}>Download</a>
            </td>
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

  .manual .muted {
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
    border-bottom: 1px solid var(--border);
  }

  .episode-head {
    display: flex;
    gap: 0.8rem;
    align-items: center;
    width: 100%;
    padding: 0.45rem 0.25rem;
    background: none;
    border: none;
    cursor: pointer;
    text-align: left;
    font: inherit;
    font-size: 0.9rem;
    color: inherit;
  }

  .episode-head:disabled {
    cursor: default;
    color: var(--muted);
  }

  .chevron {
    width: 0.8rem;
    color: var(--muted);
    flex: none;
  }

  .episode-head .title {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .episode table {
    margin: 0.2rem 0 0.8rem 1.8rem;
    width: calc(100% - 1.8rem);
  }

  .path {
    max-width: 18rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .player {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 0.8rem 1rem;
    margin-bottom: 1.5rem;
  }

  .player-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.6rem;
  }

  .player video {
    width: 100%;
    max-height: 70vh;
    background: #000;
    border-radius: 6px;
  }

  .player-note {
    margin: 0.6rem 0 0;
    font-size: 0.8rem;
  }

  .actions {
    white-space: nowrap;
  }

  .actions .download {
    margin-left: 0.7rem;
    font-size: 0.85rem;
  }
</style>
