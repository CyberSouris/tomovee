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
  cy.intercept('GET', '/api/v1/categories*', {
    body: {
      categories: [
        { key: 'type:movie', name: 'Movies', kind: 'type', value: 'movie', count: 1, poster_url: '' },
        { key: 'type:series', name: 'Series', kind: 'type', value: 'series', count: 1, poster_url: '' },
        { key: 'genre:Action', name: 'Action', kind: 'genre', value: 'Action', count: 1, poster_url: '' },
      ],
      unmatched: 1,
    },
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

  it('shows category folders with counts and the unmatched chip', () => {
    cy.visit('/');
    cy.wait('@categories');
    cy.contains('.folder', 'Movies');
    cy.contains('.folder', 'Series');
    cy.contains('.folder', 'Action');
    cy.contains('.chip', 'Unmatched (1)');
  });

  it('intersects media type and genre when folders are combined', () => {
    cy.visit('/');
    cy.wait('@catalog');
    cy.wait('@categories');
    cy.contains('.folder', 'Action').click();
    cy.wait('@catalog').its('request.url').should('include', 'genre=Action');
    cy.contains('.folder', 'Movies').click();
    cy.wait('@catalog')
      .its('request.url')
      .should('include', 'media_type=movie')
      .and('include', 'genre=Action');
    cy.contains('.pill', 'Action');
    cy.contains('.pill', 'Movies');
  });
});