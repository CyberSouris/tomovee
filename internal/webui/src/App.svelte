<script>
  import { onMount } from 'svelte';
  import { api_get } from './api.js';
  import Browse from './routes/Browse.svelte';
  import Detail from './routes/Detail.svelte';
  import Unmatched from './routes/Unmatched.svelte';
  import Scan from './routes/Scan.svelte';
  import Match from './routes/Match.svelte';
  import Settings from './routes/Settings.svelte';

  function parse_hash() {
    const raw = window.location.hash.replace(/^#\/?/, '');
    const [name, ...rest] = raw.split('/');
    return { name: name || 'browse', param: rest.join('/') };
  }

  let route = parse_hash();
  let background = null;

  onMount(() => {
    const on_change = () => {
      route = parse_hash();
    };
    window.addEventListener('hashchange', on_change);
    if (!window.location.hash) {
      window.location.hash = '#/browse';
    }
    const poll = async () => {
      try {
        background = await api_get('/api/v1/background');
      } catch {
        // keep the previous status; the backend may be starting up
      }
    };
    poll();
    const timer = setInterval(poll, 1000);
    return () => {
      window.removeEventListener('hashchange', on_change);
      clearInterval(timer);
    };
  });

  const links = [
    ['browse', 'Browse'],
    ['unmatched', 'Unmatched'],
    ['scan', 'Scan'],
    ['match', 'Match'],
    ['settings', 'Settings'],
  ];

  const busy = () =>
    background &&
    ((background.scan && background.scan.running) ||
      (background.match && background.match.running) ||
      (background.datasets && background.datasets.state === 'building'));

  $: active_task = background && tasks(background).find((t) => t.active);
  $: active_percent = active_task ? active_task.percent() : 0;
  $: active_label = active_task ? active_task.label : '';

  function tasks(bg) {
    const items = [];
    if (bg.scan && bg.scan.running) {
      const p = bg.scan.progress || {};
      items.push({
        name: 'scan',
        active: true,
        get label() {
          return `Scanning — ${p.phase || 'scanning'}`;
        },
        percent: () => (p.files_found ? Math.round((100 * (p.files_scanned || 0)) / p.files_found) : 0),
      });
    }
    if (bg.match && bg.match.running) {
      const p = bg.match.progress || {};
      items.push({
        name: 'match',
        active: true,
        get label() {
          return p.title ? `Matching — ${p.title}` : 'Matching…';
        },
        percent: () => (p.total ? Math.round((100 * p.done) / p.total) : 0),
      });
    }
    if (bg.datasets && bg.datasets.state === 'building') {
      const d = bg.datasets;
      items.push({
        name: 'datasets',
        active: true,
        get label() {
          return d.step === 'fts'
            ? 'Building IMDb index — full-text search'
            : `Building IMDb index — ${d.dataset || 'importing'}`;
        },
        percent: () => d.percent || 0,
      });
    }
    return items;
  }
</script>

{#if busy()}
  <div class="activity" class:visible={true}>
    <div class="activity-label">{active_label}</div>
    <div class="activity-bar">
      <div class="activity-fill" style={`width: ${active_percent}%`}></div>
    </div>
    <div class="activity-percent">{active_percent}%</div>
  </div>
{/if}

<header>
  <div class="brand">Tomovee</div>
  <nav>
    {#each links as [name, label]}
      <a class:active={route.name === name} href={'#/' + name}>{label}</a>
    {/each}
  </nav>
</header>

<main>
  {#if route.name === 'browse'}
    <Browse />
  {:else if route.name === 'title'}
    {#key route.param}
      <Detail id={route.param} />
    {/key}
  {:else if route.name === 'unmatched'}
    <Unmatched />
  {:else if route.name === 'scan'}
    <Scan />
  {:else if route.name === 'match'}
    <Match />
  {:else if route.name === 'settings'}
    <Settings />
  {:else}
    <p>Not found. <a href="#/browse">Go to browse</a>.</p>
  {/if}
</main>

<style>
  .activity {
    display: flex;
    align-items: center;
    gap: 0.8rem;
    padding: 0.5rem 1.5rem;
    background: var(--panel-2);
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    z-index: 10;
  }

  .activity-label {
    flex: 0 1 auto;
    font-size: 0.85rem;
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .activity-bar {
    flex: 1 1 auto;
    height: 0.5rem;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
  }

  .activity-fill {
    height: 100%;
    width: 0;
    background: var(--accent);
    transition: width 0.4s ease;
  }

  .activity-percent {
    flex: 0 0 auto;
    font-size: 0.85rem;
    color: var(--muted);
    min-width: 3rem;
    text-align: right;
  }

  header {
    display: flex;
    align-items: center;
    gap: 2rem;
    padding: 0.8rem 1.5rem;
    background: var(--panel);
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
  }

  .brand {
    font-weight: 700;
    letter-spacing: 0.02em;
  }

  nav {
    display: flex;
    gap: 1.2rem;
  }

  nav a {
    color: var(--muted);
  }

  nav a.active {
    color: var(--text);
    font-weight: 600;
  }

  main {
    max-width: 1200px;
    margin: 0 auto;
    padding: 1.5rem;
  }
</style>
