<script>
  import { onMount } from 'svelte';
  import { api_get } from './api.js';
  import Browse from './routes/Browse.svelte';
  import Detail from './routes/Detail.svelte';
  import Unmatched from './routes/Unmatched.svelte';
  import Actions from './routes/Actions.svelte';
  import Settings from './routes/Settings.svelte';

  function parse_hash() {
    const raw = window.location.hash.replace(/^#\/?/, '');
    const [name, ...rest] = raw.split('/');
    return { name: name || 'browse', param: rest.join('/') };
  }

  let route = parse_hash();
  let background = null;
  let previous = null;
  let notices = [];
  let notice_seq = 0;

  function add_notice(notice) {
    const id = ++notice_seq;
    notices = [...notices, { id, ...notice }];
    setTimeout(() => {
      notices = notices.filter((n) => n.id !== id);
    }, 10000);
  }

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
        const next = await api_get('/api/v1/background');
        const prev = previous;
        if (prev) {
          if (prev.scan && prev.scan.running && next.scan && !next.scan.running) {
            const r = next.scan.result || {};
            if (next.scan.error) {
              add_notice({ kind: 'error', page: '/scan', text: 'Scan failed: ' + next.scan.error });
            } else {
              const parts = [];
              if (r.new) parts.push(`${r.new} new`);
              if (r.skipped) parts.push(`${r.skipped} skipped`);
              if (r.errors && r.errors.length) parts.push(`${r.errors.length} errors`);
              add_notice({ kind: 'done', page: '/scan', text: parts.length ? 'Scan finished — ' + parts.join(', ') : 'Scan finished' });
            }
          }
          if (
            prev.match &&
            next.match &&
            !next.match.running &&
            (prev.match.running || (prev.match.id && next.match.id && prev.match.id !== next.match.id))
          ) {
            const r = next.match.result || {};
            if (next.match.error) {
              add_notice({ kind: 'error', page: '/match', text: 'Matching failed: ' + next.match.error });
            } else if (r.candidates) {
              const parts = [];
              if (r.matched) parts.push(`${r.matched} matched`);
              if (r.candidates) parts.push(`${r.candidates} candidate${r.candidates === 1 ? '' : 's'} to review`);
              add_notice({ kind: 'done', page: '/unmatched', text: 'Matching finished — ' + parts.join(', ') });
            } else {
              const parts = [];
              if (r.matched) parts.push(`${r.matched} matched`);
              if (r.unmatched) parts.push(`${r.unmatched} unmatched`);
              add_notice({ kind: 'done', page: '/match', text: parts.length ? 'Matching finished — ' + parts.join(', ') : 'Matching finished' });
            }
          }
          if (prev.datasets && prev.datasets.state === 'building' && next.datasets && next.datasets.state !== 'building') {
            if (next.datasets.state === 'ready') {
              add_notice({ kind: 'done', page: '/settings', text: 'IMDb index ready' });
            } else if (next.datasets.state === 'error') {
              add_notice({ kind: 'error', page: '/settings', text: 'IMDb index failed: ' + (next.datasets.message || 'unknown error') });
            }
          }
        }
        previous = next;
        background = next;
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
    ['scan', 'Actions'],
    ['settings', 'Settings'],
  ];

  $: active_tasks = background ? tasks(background).filter((t) => t.active) : [];
  $: active_task = active_tasks[0] || null;
  $: active_percent = active_task ? active_task.percent() : 0;
  $: active_label = active_task ? active_task.label : '';
  $: extra_tasks = active_tasks.length - 1;

  // Dismissing hides the running-job toast; it reappears automatically once a
  // new background task starts (the current set of tasks is only compared so a
  // task that begins while another still runs is not missed).
  let dismissed = false;
  let dismissed_key = '';
  $: task_key = active_tasks
    .map((t) => t.name)
    .sort()
    .join(',');
  $: if (task_key !== dismissed_key) {
    dismissed = false;
    dismissed_key = task_key;
  }

  function go_task(task) {
    if (!task) return;
    window.location.hash = '#' + task.page;
  }

  function go_notice(notice) {
    window.location.hash = '#' + notice.page;
  }

  function tasks(bg) {
    const items = [];
    if (bg.scan && bg.scan.running) {
      const p = bg.scan.progress || {};
      items.push({
        name: 'scan',
        page: '/scan',
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
        page: '/match',
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
        page: '/settings',
        active: true,
        get label() {
          if (d.step === 'fts') return 'Building IMDb index — full-text search';
          if (d.dataset) return `Importing IMDb data — ${d.dataset}`;
          return 'Importing IMDb data (background)';
        },
        percent: () => d.percent || 0,
      });
    }
    return items;
  }
</script>

<div class="toast-stack">
  {#if active_task && !dismissed}
    <div class="notification" role="status" aria-live="polite">
      <button class="notif-main" on:click={() => go_task(active_task)} title={active_label}>
        <span class="notif-dot" aria-hidden="true"></span>
        <span class="notif-label">{active_label}</span>
        {#if extra_tasks > 0}
          <span class="notif-more">+{extra_tasks}</span>
        {/if}
        <span class="notif-percent">{active_percent}%</span>
      </button>
      <div class="notif-bar"><div class="notif-fill" style={`width: ${active_percent}%`}></div></div>
      <button class="notif-close" aria-label="Dismiss notification" title="Dismiss" on:click={() => (dismissed = true)}>
        ×
      </button>
    </div>
  {/if}

  {#each notices as notice (notice.id)}
    <div
      class:notification-error={notice.kind === 'error'}
      class:notification-done={notice.kind === 'done'}
      class="notification notice"
      role="status"
      aria-live="polite"
    >
      <button class="notif-main" on:click={() => go_notice(notice)} title="View">
        <span class="notif-dot" aria-hidden="true"></span>
        <span class="notif-label">{notice.text}</span>
      </button>
      <button
        class="notif-close"
        aria-label="Dismiss notification"
        title="Dismiss"
        on:click={() => (notices = notices.filter((n) => n.id !== notice.id))}
      >
        ×
      </button>
    </div>
  {/each}
</div>

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
  {:else if route.name === 'scan' || route.name === 'match'}
    <Actions />
  {:else if route.name === 'settings'}
    <Settings />
  {:else}
    <p>Not found. <a href="#/browse">Go to browse</a>.</p>
  {/if}
</main>

<style>
  .toast-stack {
    position: fixed;
    right: 1.2rem;
    bottom: 1.2rem;
    z-index: 100;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 0.6rem;
  }

  .notification {
    width: min(360px, calc(100vw - 2rem));
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45);
    overflow: hidden;
    animation: notif-in 0.18s ease-out;
  }

  .notification-done .notif-dot {
    background: var(--ok, #43a047);
    animation: none;
  }

  .notification-error {
    border-color: #b00020;
  }

  .notification-error .notif-dot {
    background: #b00020;
    animation: none;
  }

  .notice .notif-main {
    padding-bottom: 0.75rem;
  }

  .notif-main {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    width: 100%;
    background: transparent;
    color: var(--text);
    border: 0;
    border-radius: 0;
    padding: 0.75rem 2.4rem 0.55rem 0.9rem;
    font-size: 0.88rem;
    text-align: left;
    cursor: pointer;
  }

  .notif-main:hover {
    background: var(--panel-2);
  }

  .notif-dot {
    flex: 0 0 auto;
    width: 0.6rem;
    height: 0.6rem;
    border-radius: 50%;
    background: var(--accent);
    animation: notif-pulse 1.4s ease-in-out infinite;
  }

  .notif-label {
    flex: 1 1 auto;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .notif-more {
    flex: 0 0 auto;
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.72rem;
    color: var(--muted);
  }

  .notif-percent {
    flex: 0 0 auto;
    font-variant-numeric: tabular-nums;
    color: var(--muted);
    font-size: 0.82rem;
  }

  .notif-bar {
    height: 3px;
    background: var(--panel-2);
    margin: 0 0.9rem 0.75rem;
    border-radius: 2px;
    overflow: hidden;
  }

  .notif-fill {
    height: 100%;
    width: 0;
    background: var(--accent);
    transition: width 0.4s ease;
  }

  .notif-close {
    position: absolute;
    top: 0.3rem;
    right: 0.3rem;
    background: transparent;
    color: var(--muted);
    border: 0;
    border-radius: 4px;
    padding: 0.1rem 0.4rem;
    font-size: 1.1rem;
    line-height: 1;
    cursor: pointer;
  }

  .notif-close:hover {
    background: var(--panel-2);
    color: var(--text);
  }

  @keyframes notif-in {
    from {
      opacity: 0;
      transform: translateY(8px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }

  @keyframes notif-pulse {
    0%,
    100% {
      opacity: 1;
      transform: scale(1);
    }
    50% {
      opacity: 0.45;
      transform: scale(0.8);
    }
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
