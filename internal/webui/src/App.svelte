<script>
  import { onMount } from 'svelte';
  import Browse from './routes/Browse.svelte';
  import Detail from './routes/Detail.svelte';
  import Unmatched from './routes/Unmatched.svelte';
  import Scan from './routes/Scan.svelte';
  import Settings from './routes/Settings.svelte';

  function parse_hash() {
    const raw = window.location.hash.replace(/^#\/?/, '');
    const [name, ...rest] = raw.split('/');
    return { name: name || 'browse', param: rest.join('/') };
  }

  let route = parse_hash();

  onMount(() => {
    const on_change = () => {
      route = parse_hash();
    };
    window.addEventListener('hashchange', on_change);
    if (!window.location.hash) {
      window.location.hash = '#/browse';
    }
    return () => window.removeEventListener('hashchange', on_change);
  });

  const links = [
    ['browse', 'Browse'],
    ['unmatched', 'Unmatched'],
    ['scan', 'Scan'],
    ['settings', 'Settings'],
  ];
</script>

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
  {:else if route.name === 'settings'}
    <Settings />
  {:else}
    <p>Not found. <a href="#/browse">Go to browse</a>.</p>
  {/if}
</main>

<style>
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
