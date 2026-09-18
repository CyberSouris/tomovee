<script>
  import { onMount, onDestroy, createEventDispatcher } from 'svelte';
  import { api_get } from './api.js';

  export let query = '';
  export let year = '';
  export let media_type = '';
  export let placeholder = 'Search by title…';

  const dispatch = createEventDispatcher();

  let suggestions = [];
  let open = false;
  let busy = false;
  let error = '';
  let highlight = -1;
  let timer;
  let root;

  function id_suggestion(q) {
    if (/^tt\d+$/i.test(q)) {
      return { tmdb_id: 0, imdb_id: q.toLowerCase(), media_type, title: q, is_id: true };
    }
    if (/^\d+$/.test(q)) {
      return { tmdb_id: Number(q), imdb_id: '', media_type, title: q, is_id: true };
    }
    return null;
  }

  async function search() {
    const q = query.trim();
    if (q.length < 2) {
      suggestions = [];
      open = false;
      return;
    }
    busy = true;
    error = '';
    try {
      const params = new URLSearchParams({ q });
      if (media_type) params.set('media_type', media_type);
      if (year) params.set('year', String(year));
      const data = await api_get('/api/v1/search?' + params.toString());
      const direct = id_suggestion(q);
      suggestions = data.results || [];
      if (direct) suggestions.unshift(direct);
      highlight = -1;
      open = true;
    } catch (err) {
      error = err.message;
      suggestions = [];
      open = false;
    } finally {
      busy = false;
    }
  }

  function on_input() {
    clearTimeout(timer);
    timer = setTimeout(search, 250);
  }

  function choose(item) {
    dispatch('select', {
      tmdb_id: item.tmdb_id || 0,
      imdb_id: item.imdb_id || '',
      media_type: item.media_type || media_type,
      title: item.title || '',
      year: item.year || 0,
      is_id: !!item.is_id,
    });
    open = false;
  }

  function on_keydown(event) {
    if (event.key === 'Escape') {
      event.preventDefault();
      open = false;
      return;
    }
    if (!open || suggestions.length === 0) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      highlight = (highlight + 1) % suggestions.length;
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      highlight = (highlight - 1 + suggestions.length) % suggestions.length;
    } else if (event.key === 'Enter') {
      event.preventDefault();
      if (highlight >= 0 && suggestions[highlight]) choose(suggestions[highlight]);
    }
  }

  function close_outside(event) {
    if (root && !root.contains(event.target)) open = false;
  }

  onMount(() => {
    document.addEventListener('click', close_outside);
    return () => document.removeEventListener('click', close_outside);
  });

  onDestroy(() => clearTimeout(timer));
</script>

<div class="wrap" bind:this={root}>
  <div class="fields">
    <input
      class="query"
      bind:value={query}
      on:input={on_input}
      on:keydown={on_keydown}
      placeholder={placeholder}
      autocomplete="off"
    />
    <input
      class="year"
      type="text"
      bind:value={year}
      on:input={on_input}
      on:keydown={on_keydown}
      placeholder="Year"
      autocomplete="off"
    />
  </div>

  {#if error}<p class="note error">{error}</p>{/if}

  {#if open && suggestions.length > 0}
<ul class="list">
        {#each suggestions as item, i (item.tmdb_id + '|' + item.imdb_id)}
          <li>
            <button
              type="button"
              class="option"
              class:active={i === highlight}
              on:mouseenter={() => (highlight = i)}
              on:click={() => choose(item)}
            >
              {#if item.poster_url}
                <img src={item.poster_url} alt="" loading="lazy" />
              {:else}
                <span class="ph"></span>
              {/if}
              <span class="meta">
                <strong>
                  {item.title}
                  {#if item.year}<span class="muted">({item.year})</span>{/if}
                </strong>
                <span class="muted">
                  {#if item.is_id}
                    Use directly
                  {:else}
                    {item.media_type === 'series' ? 'Series' : 'Movie'}
                    {#if item.vote_average > 0}· ★ {item.vote_average.toFixed(1)}{/if}
                  {/if}
                </span>
              </span>
            </button>
          </li>
        {/each}
      </ul>
  {/if}

  {#if busy}<p class="note">Searching…</p>{/if}
</div>

<style>
  .wrap {
    position: relative;
    max-width: 26rem;
  }

  .fields {
    display: flex;
    gap: 0.5rem;
  }

  .query {
    flex: 1;
    min-width: 0;
  }

  .year {
    width: 4.5rem;
  }

  .list {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    z-index: 10;
    margin: 0;
    padding: 0.25rem;
    list-style: none;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 8px;
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.4);
    max-height: 22rem;
    overflow-y: auto;
  }

  .list li {
    padding: 0;
  }

  .list li .option {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    width: 100%;
    padding: 0.35rem 0.5rem;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text);
    font-family: inherit;
    font-size: inherit;
    text-align: left;
    cursor: pointer;
  }

  .list li .option.active,
  .list li .option:hover {
    background: var(--panel-2);
  }

  .list img,
  .list .ph {
    width: 34px;
    height: 50px;
    border-radius: 4px;
    flex: none;
  }

  .list .ph {
    background: var(--panel-2);
    border: 1px solid var(--border);
  }

  .meta {
    display: flex;
    flex-direction: column;
    min-width: 0;
    font-size: 0.85rem;
  }

  .meta strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .muted {
    color: var(--muted);
  }

  .note {
    margin: 0.3rem 0 0;
    font-size: 0.8rem;
    color: var(--muted);
  }

  .note.error {
    color: var(--bad);
  }
</style>