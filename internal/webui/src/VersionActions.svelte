<script>
  import { createEventDispatcher } from 'svelte';

  export let version;
  export let series = false;
  export let playing = false;
  export let busy = false;

  const dispatch = createEventDispatcher();

  let open = false;
  let season = '1';
  let episode = '';

  function file_name(file) {
    return (file.file_path || 'video').split('/').pop();
  }

  function toggle() {
    open = !open;
  }

  function submit() {
    const season_number = Number(season);
    const episode_number = Number(episode);
    if (!(season_number >= 0) || !(episode_number >= 1)) return;
    dispatch('assign', { version, season: season_number, episode: episode_number });
    open = false;
  }

  function enter(event) {
    if (event.key === 'Enter') submit();
  }
</script>

<div class="actions">
  <div class="row">
    <button class="secondary" on:click={() => dispatch('play', version)} disabled={busy}>
      {playing ? 'Playing…' : 'Play'}
    </button>
    <a class="download" href={version.file_url} download={file_name(version)}>Download</a>
    <button class="secondary" on:click={() => dispatch('split', version)} disabled={busy}>
      Split
    </button>
    {#if series}
      <button class="secondary" on:click={toggle} disabled={busy}>
        {open ? 'Cancel' : 'Episode…'}
      </button>
    {/if}
  </div>
  {#if open && series}
    <div class="row">
      <input
        class="num"
        type="number"
        min="0"
        aria-label="Season"
        placeholder="S"
        bind:value={season}
        on:keydown={enter}
        disabled={busy}
      />
      <input
        class="num"
        type="number"
        min="1"
        aria-label="Episode"
        placeholder="E"
        bind:value={episode}
        on:keydown={enter}
        disabled={busy}
      />
      <button class="secondary" on:click={submit} disabled={busy || !(Number(episode) >= 1)}>
        Set episode
      </button>
    </div>
  {/if}
</div>

<style>
  .actions {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.35rem;
  }

  .row {
    display: flex;
    gap: 0.4rem;
    align-items: center;
  }

  .row button {
    padding: 0.25rem 0.5rem;
    font-size: 0.8rem;
  }

  .download {
    font-size: 0.8rem;
  }

  .num {
    width: 4rem;
    padding: 0.25rem 0.4rem;
    font-size: 0.8rem;
  }
</style>
