//
// e2e/smoke/corrections.cy.js exercises the per-file correction endpoints
// against the real binary: a real scan over real (tiny) videos, a real SQLite
// database, real jobs queued through the matching job manager. No TMDB key and
// no IMDb index are configured, so every correction lands as "needs_lookup"
// with no candidates — the point here is the state transition the endpoint
// performs, not what a metadata provider would say about it.
//
// The specs are a narrative and depend on each other in the order written:
// a file can only be put on an episode once its entry is a series, and a file
// can only be split out of a title that still has files in it.
//

// Ids discovered by the scan in before(), filled in as the commands settle.
let entry_id = 0;
let first_version = 0;
let second_version = 0;
let catalog_total = 0;

function wait_for_job(attempts = 200) {
  return cy.request('/api/v1/scan/status').then((response) => {
    const job = response.body.job;
    if (!job || !job.running) return job;
    if (attempts <= 0) throw new Error('the scan never finished');
    return cy.wait(250).then(() => wait_for_job(attempts - 1));
  });
}

describe('per-file corrections over the real API', () => {
  before(function () {
    cy.request({ method: 'POST', url: '/api/v1/scan', body: {}, failOnStatusCode: false });
    wait_for_job().then((job) => {
      // No ffprobe, no media, or everything filtered out by the size floor:
      // there is nothing to correct, and skipping beats reporting a failure
      // that says more about the machine than about the code.
      if (!job || !job.result || job.result.new === 0) {
        this.skip();
      }
    });

    cy.request('/api/v1/catalog?limit=50').then((response) => {
      catalog_total = response.body.total;
      const movie = response.body.entries.find((entry) => entry.media_type === 'movie');
      expect(movie, 'the seeded scan should produce a movie entry').to.exist;
      entry_id = movie.id;
      cy.request(`/api/v1/catalog/${entry_id}`).then((detail) => {
        // Two files of one title, so a split has something to split away from.
        expect(detail.body.versions).to.have.length.of.at.least(2);
        first_version = detail.body.versions[0].id;
        second_version = detail.body.versions[1].id;
      });
    });
  });

  it('refuses a correction for a version that does not exist', () => {
    cy.request({ method: 'POST', url: '/api/v1/versions/nonsense/split', failOnStatusCode: false })
      .its('status').should('eq', 400);
    cy.request({ method: 'POST', url: '/api/v1/versions/999999/split', failOnStatusCode: false })
      .its('status').should('eq', 404);
    cy.request({ method: 'POST', url: '/api/v1/versions/999999/episode', body: { season: 1, episode: 1 }, failOnStatusCode: false })
      .its('status').should('eq', 404);
  });

  it('rejects impossible episode numbers', () => {
    cy.request({
      method: 'POST',
      url: `/api/v1/versions/${first_version}/episode`,
      body: { season: -1, episode: 0 },
      failOnStatusCode: false,
    }).its('status').should('eq', 400);
  });

  it('refuses to put a movie file on an episode', () => {
    // The entry is still a movie here, so there is no season for the file to
    // belong to and the request has to fail rather than invent one.
    cy.request({
      method: 'POST',
      url: `/api/v1/versions/${first_version}/episode`,
      body: { season: 1, episode: 1 },
      failOnStatusCode: false,
    }).its('status').should('eq', 500);
  });

  it('rejects a reclassification that is neither movie nor series', () => {
    cy.request({
      method: 'POST',
      url: `/api/v1/catalog/${entry_id}/reclassify`,
      body: { media_type: 'album' },
      failOnStatusCode: false,
    }).its('status').should('eq', 400);
    cy.request({ method: 'POST', url: '/api/v1/catalog/999999/reclassify', body: { media_type: 'series' }, failOnStatusCode: false })
      .its('status').should('eq', 404);
  });

  it('retypes a movie as a series and numbers its files', () => {
    cy.request({
      method: 'POST',
      url: `/api/v1/catalog/${entry_id}/reclassify`,
      body: { media_type: 'series' },
    }).its('status').should('eq', 200);

    cy.request(`/api/v1/catalog/${entry_id}`).then((response) => {
      expect(response.body.entry.media_type).to.eq('series');
      // Files whose names carry no episode number are numbered in order, so a
      // retyped movie is immediately browsable as a show.
      const numbers = response.body.episodes.map((episode) => [episode.season_number, episode.episode_number]);
      expect(numbers).to.deep.eq([[1, 1], [1, 2]]);
      const on_first = response.body.episodes.flatMap((episode) => episode.versions.map((version) => version.id));
      expect(on_first).to.have.members([first_version, second_version]);
    });
  });

  it('puts a series file on the episode the user picked', () => {
    cy.request({
      method: 'POST',
      url: `/api/v1/versions/${second_version}/episode`,
      body: { season: 2, episode: 5 },
    }).then((response) => {
      expect(response.status).to.eq(200);
      expect(response.body.ok).to.eq(true);
      expect(response.body.episode_id).to.be.greaterThan(0);
    });

    cy.request(`/api/v1/catalog/${entry_id}`).then((response) => {
      const episode = response.body.episodes.find((one) => one.season_number === 2 && one.episode_number === 5);
      expect(episode, 'the chosen episode should exist').to.exist;
      expect(episode.versions.map((version) => version.id)).to.deep.eq([second_version]);
    });
  });

  it('splits a file out of its title as a movie of its own', () => {
    // Everything below hangs off the response, because a command's URL is
    // built when the command is queued, not when it runs.
    cy.request({ method: 'POST', url: `/api/v1/versions/${first_version}/split` }).then((response) => {
      expect(response.status).to.eq(200);
      // Nothing to match against offline, so the new title waits for a lookup.
      expect(response.body.applied).to.eq(false);
      expect(response.body.candidates).to.have.lengthOf(0);
      expect(response.body.entry.media_type).to.eq('movie');
      const new_entry_id = response.body.entry.id;
      expect(new_entry_id).to.not.eq(entry_id);

      cy.request('/api/v1/catalog?limit=50').then((list) => {
        expect(list.body.total).to.eq(catalog_total + 1);
      });
      // The file moved; the title it left behind kept the other one.
      cy.request(`/api/v1/catalog/${new_entry_id}`).then((detail) => {
        expect(detail.body.entry.media_type).to.eq('movie');
        expect(detail.body.versions.map((version) => version.id)).to.deep.eq([first_version]);
      });
      cy.request(`/api/v1/catalog/${entry_id}`).then((detail) => {
        const remaining = detail.body.episodes.flatMap((episode) => episode.versions.map((version) => version.id));
        expect(remaining).to.deep.eq([second_version]);
      });
    });
  });
});
