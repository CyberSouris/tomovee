// The offline IMDb index, built by the real binary at startup from the export
// files e2e/run.mjs writes. Everything here goes through the HTTP API, so it
// exercises the whole import path: parsing the TSVs, filling the index, the FTS
// table and the matcher's offline source.

function wait_for_datasets(attempts = 200) {
  return cy.request('/api/v1/settings').then((response) => {
    const status = response.body.datasets || {};
    if (status.state === 'ready' || status.state === 'error') return status;
    if (attempts <= 0) throw new Error(`datasets never settled: ${JSON.stringify(status)}`);
    return cy.wait(250).then(() => wait_for_datasets(attempts - 1));
  });
}

describe('offline IMDb datasets', () => {
  let datasets = {};

  before(() => {
    wait_for_datasets().then((status) => {
      datasets = status;
    });
  });

  it('builds the index from the configured export directory', () => {
    expect(datasets.state, `datasets state: ${JSON.stringify(datasets)}`).to.eq('ready');
    expect(datasets.has_path).to.eq(true);
    // message is omitempty, so an absent field is the success case.
    expect(datasets.message || '').to.eq('');
  });

  it('serves title searches straight from the index', () => {
    cy.request('/api/v1/search?q=Matrix').then((response) => {
      expect(response.status).to.eq(200);
      const hits = response.body.results;
      expect(hits.map((hit) => hit.imdb_id), JSON.stringify(hits)).to.include('tt0133093');
      const matrix = hits.find((hit) => hit.imdb_id === 'tt0133093');
      expect(matrix.title).to.eq('The Matrix');
      expect(matrix.year).to.eq(1999);
    });
  });

  it('matches a catalog entry by IMDb id without a TMDB key', () => {
    // An index-backed match is the only source configured in the fixture, so
    // this goes through Resolve_by_imdb_offline and the episode enrichment.
    cy.request({ method: 'POST', url: '/api/v1/scan' }).then((response) => {
      expect(response.status).to.eq(202);
      return wait_for_scan();
    });
    cy.request('/api/v1/catalog?limit=50').then((response) => {
      const entry = response.body.entries.find((item) => item.media_type === 'movie');
      if (!entry) {
        cy.log('no movie in the catalog (the media fixture is empty)');
        return null;
      }
      return cy.request({
        method: 'POST',
        url: `/api/v1/catalog/${entry.id}/match`,
        body: { imdb_id: 'tt0133093', media_type: 'movie' },
      }).then((match) => {
        expect(match.status, match.body).to.eq(200);
        expect(match.body.entry.status).to.eq('matched');
        expect(match.body.entry.imdb_id).to.eq('tt0133093');
        expect(match.body.entry.title).to.eq('The Matrix');
        expect(match.body.entry.tmdb_id).to.eq(0);
      });
    });
  });
});

// /api/v1/scan/status always reports the most recent scan job.
function wait_for_scan(attempts = 200) {
  return cy.request('/api/v1/scan/status').then((response) => {
    if (response.body.job && response.body.job.running) {
      if (attempts <= 0) throw new Error('scan never finished');
      return cy.wait(250).then(() => wait_for_scan(attempts - 1));
    }
    return response.body.job;
  });
}
