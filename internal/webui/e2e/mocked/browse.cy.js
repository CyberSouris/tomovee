//
// e2e/mocked/* run against the built SPA served statically (vite preview).
// The API is stubbed with cy.intercept so the rendering specs are
// deterministic and need no live backend.
//

const entries = [
  { id: 1, media_type: 'movie', title: 'The Matrix', release_year: 1999, status: 'matched', poster_url: '' },
  {
    id: 2,
    media_type: 'series',
    title: 'Breaking Bad',
    release_year: 2008,
    status: 'needs_lookup',
    poster_url: '',
  },
];

beforeEach(() => {
  cy.intercept('GET', '/api/v1/catalog*', {
    body: { total: entries.length, count: entries.length, entries },
  }).as('catalog');
  cy.intercept('GET', '/api/v1/libraries', { body: [] }).as('libraries');
  cy.intercept('GET', '/api/v1/categories', {
    body: { genres: [{ name: 'Action', count: 1 }], unmatched: 1 },
  }).as('categories');
  cy.intercept('GET', '/api/v1/background', {
    body: { scan: null, match: null, datasets: null },
  }).as('background');
});

describe('browse page', () => {
  it('renders the app shell', () => {
    cy.visit('/');
    cy.contains('.brand', 'Tomovee');
    cy.get('nav a').should('contain', 'Browse');
    cy.get('nav a').should('contain', 'Settings');
  });

  it('lists catalog entries in the grid', () => {
    cy.visit('/');
    cy.wait('@catalog');
    cy.contains('.card', 'The Matrix');
    cy.contains('.card', 'Breaking Bad');
    cy.get('.card').should('have.length', 2);
    cy.get('.card[href="#/title/1"]').should('exist');
    cy.contains('p', '2 titles');
  });

  it('shows the unmatched chip count from the categories endpoint', () => {
    cy.visit('/');
    cy.contains('.chips button', 'Unmatched (1)');
    cy.contains('.chips button', 'Action (1)');
  });
});