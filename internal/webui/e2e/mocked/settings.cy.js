const settings = {
  listen: '127.0.0.1:8080',
  database_path: '/var/lib/tomovee/tomovee.db',
  poster_cache_dir: '/var/cache/tomovee/posters',
  watch_enabled: false,
  tmdb_key: '',
  tmdb_configured: false,
  opensubtitles_api_key: '',
  opensubtitles_username: '',
  opensubtitles_password: '',
  opensubtitles_configured: false,
  matching_ready: false,
  libraries: [
    { id: 1, name: 'Movies', path: '/media/movies', enabled: true, last_scan: '2026-01-01T00:00:00Z' },
    { id: 2, name: 'Series', path: '/media/series', enabled: true, last_scan: '2026-01-02T00:00:00Z' },
  ],
};

beforeEach(() => {
  cy.intercept('GET', '/api/v1/settings', { body: settings }).as('settings');
  cy.intercept('GET', '/api/v1/background', {
    body: { scan: null, match: null, datasets: null },
  }).as('background');
});

describe('settings page', () => {
  it('renders server info', () => {
    cy.visit('/#/settings');
    cy.wait('@settings');
    cy.contains('h2', 'Library');
    cy.contains('td', '/var/lib/tomovee/tomovee.db');
    cy.contains('td', '/media/movies');
  });

  it('lists registered libraries with their last scans', () => {
    cy.visit('/#/settings');
    cy.wait('@settings');
    cy.contains('tbody tr', 'Movies');
    cy.contains('tbody tr', '/media/series');
    cy.contains('tbody tr', '2026-01-02T00:00:00Z');
  });

  it('shows the API readiness indicators', () => {
    cy.visit('/#/settings');
    cy.wait('@settings');
    cy.contains('TMDB:');
    cy.contains('li', 'Reload applies them to the running server');
  });
});