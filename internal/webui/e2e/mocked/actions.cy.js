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

  it('force rescan sends a force scan for the selected libraries', () => {
    cy.intercept('POST', '/api/v1/scan', { statusCode: 202, body: { job: null } }).as('scan');
    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.contains('button', 'Select all').click();
    cy.contains('button', 'Force rescan').click();
    cy.wait('@scan').its('request.body').should('deep.equal', {
      libraries: ['Movies', 'Series'],
      force: true,
    });
  });

  it('a plain scan omits the force flag', () => {
    cy.intercept('POST', '/api/v1/scan', { statusCode: 202, body: { job: null } }).as('scan');
    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.contains('button', 'Select all').click();
    cy.contains('button', 'Scan libraries').click();
    cy.wait('@scan').its('request.body').should('deep.equal', {
      libraries: ['Movies', 'Series'],
      force: false,
    });
  });

  it('force rematch asks the rematch endpoint to drop existing matches', () => {
    cy.intercept('POST', '/api/v1/match/rematch', { statusCode: 202, body: { job: null } }).as('rematch');
    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.contains('button', 'Select all').click();
    cy.contains('button', 'Force rematch').click();
    cy.wait('@rematch').its('request.body').should('deep.equal', {
      libraries: ['Movies', 'Series'],
      force: true,
    });
  });

  it('match only asks for unmatched entries', () => {
    cy.intercept('POST', '/api/v1/match', { statusCode: 202, body: { job: null } }).as('match');
    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.contains('button', 'Select all').click();
    cy.contains('button', 'Match').click();
    cy.wait('@match').its('request.body').should('deep.equal', {
      libraries: ['Movies', 'Series'],
    });
  });
});

describe('actions page result panels', () => {
  it('triggering a match hides the previous scan results', () => {
    cy.intercept('GET', '/api/v1/scan/status', {
      body: {
        job: {
          id: '1',
          running: false,
          progress: {
            phase: 'done',
            files_found: 3,
            files_scanned: 3,
            new_files: 1,
            skipped: 0,
            errors: 0,
          },
          result: { found: 3, new: 1, skipped: 0, missing: 0, errors: [] },
        },
      },
    }).as('scanStatus');
    cy.intercept('POST', '/api/v1/match', { statusCode: 202, body: { job: null } }).as('match');

    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.contains('button', 'Select all').click();
    cy.contains('h2', 'Scan result').should('exist');
    cy.contains('h2', /^Scan$/).should('exist');

    cy.contains('button', 'Match').click();
    cy.wait('@match');

    cy.contains('h2', 'Scan result').should('not.exist');
    cy.contains('h2', /^Scan$/).should('not.exist');
  });

  it('triggering a scan hides the previous match results', () => {
    cy.intercept('GET', '/api/v1/match/status', {
      body: {
        job: {
          id: '1',
          running: false,
          progress: { phase: 'done', total: 4, done: 4, matched: 3, unmatched: 1, errors: 0 },
          result: { total: 4, matched: 3, unmatched: 1, errors: [] },
        },
      },
    }).as('matchStatus');
    cy.intercept('POST', '/api/v1/scan', { statusCode: 202, body: { job: null } }).as('scan');

    cy.visit('/#/scan');
    cy.wait('@settings');
    cy.contains('button', 'Select all').click();
    cy.contains('h2', 'Match result').should('exist');
    cy.contains('h2', /^Match$/).should('exist');

    cy.contains('button', 'Scan libraries').click();
    cy.wait('@scan');

    cy.contains('h2', 'Match result').should('not.exist');
    cy.contains('h2', /^Match$/).should('not.exist');
  });
});