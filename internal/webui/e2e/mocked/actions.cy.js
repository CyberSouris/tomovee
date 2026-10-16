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
    { id: 1, name: 'Movies', path: '/media/movies', enabled: false, last_scan: '' },
    { id: 2, name: 'Series', path: '/media/series', enabled: false, last_scan: '' },
  ],
};

beforeEach(() => {
  cy.intercept('GET', '/api/v1/settings', { body: settings }).as('settings');
  cy.intercept('GET', '/api/v1/background', {
    body: { scan: null, match: null, datasets: null },
  }).as('background');
  cy.intercept('GET', '/api/v1/scan/status', { body: { job: null } }).as('scanStatus');
  cy.intercept('GET', '/api/v1/match/status', { body: { job: null } }).as('matchStatus');
});

describe('actions page library selection', () => {
  it('select all checks every library checkbox', () => {
    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.get('input[type="checkbox"]').should('have.length', 2);
    cy.get('input[type="checkbox"]').should('not.be.checked');
    cy.contains('button', 'Select all').click();
    cy.get('input[type="checkbox"]').should('be.checked');
    cy.contains('button', 'Select none').should('exist');
  });

  it('select none unchecks every library checkbox', () => {
    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.contains('button', 'Select all').click();
    cy.get('input[type="checkbox"]').should('be.checked');
    cy.contains('button', 'Select none').click();
    cy.get('input[type="checkbox"]').should('not.be.checked');
    cy.contains('button', 'Select all').should('exist');
  });
});