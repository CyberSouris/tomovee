//
// e2e/smoke/* run against a live `tomovee serve` process (fixture config, empty
// database). This is the full-stack check that the web UI actually works: the
// Svelte build embedded in the binary is served to a real browser and talks to
// the real REST API.
//

describe('tomovee serve smoke test', () => {
  it('serves the web UI over the real backend', () => {
    cy.visit('/');
    cy.contains('.brand', 'Tomovee');
    cy.contains('nav a', 'Browse');
    cy.contains('nav a', 'Settings');
  });

  it('loads the browse page against the real API', () => {
    // corrections.cy.js scans the fixture before this runs, so the count the
    // page shows has to come from the API rather than being spelled out here.
    cy.request('/api/v1/catalog?limit=1').then((api) => {
      const total = api.body.total;
      cy.visit('/#/browse');
      cy.contains('p', `${total} title${total === 1 ? '' : 's'}`, { timeout: 10000 });
    });
  });

  it('exposes the settings API', () => {
    cy.request('/api/v1/settings').then((response) => {
      expect(response.status).to.eq(200);
      expect(response.body).to.have.property('libraries');
      expect(response.body).to.have.property('tmdb_configured');
    });
    cy.request('/api/v1/catalog?limit=10&offset=0').then((response) => {
      expect(response.status).to.eq(200);
      expect(response.body).to.have.property('entries');
    });
  });
});