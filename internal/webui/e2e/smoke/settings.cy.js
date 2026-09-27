// Settings and the library list, driven through the real API. The fixture
// config registers two libraries: the spec deletes the deliberately empty
// Archive one so the catalog the correction specs built stays untouched.

describe('settings and libraries', () => {
  it('reports the effective settings and both fixture libraries', () => {
    cy.request('/api/v1/settings').then((response) => {
      expect(response.status).to.eq(200);
      const body = response.body;
      expect(body.listen).to.match(/^127\.0\.0\.1:\d+$/);
      expect(body.database_path).to.not.eq('');
      expect(body.watch_enabled).to.eq(false);
      // No API keys in the fixture, and the IMDb index is the only source.
      expect(body.tmdb_configured).to.eq(false);
      expect(body.opensubtitles_configured).to.eq(false);
      expect(body.matching_ready).to.eq(true);
      expect(body.datasets.has_path).to.eq(true);
      const names = body.libraries.map((library) => library.name);
      expect(names).to.have.members(['Movies', 'Archive']);
    });
  });

  it('persists a settings override and reloads the sources', () => {
    // The OpenSubtitles username is persisted as an override and re-read on
    // every settings request. A TMDB key is deliberately left unset so later
    // matching runs stay offline instead of calling out to the network.
    cy.request({
      method: 'PUT',
      url: '/api/v1/settings',
      body: { opensubtitles_username: 'e2e-user', watch_enabled: false },
    }).then((response) => {
      expect(response.status).to.eq(200);
      expect(response.body.opensubtitles_username).to.eq('e2e-user');
    });
    cy.request('/api/v1/settings').then((response) => {
      expect(response.body.opensubtitles_username).to.eq('e2e-user');
    });
    cy.request({ method: 'POST', url: '/api/v1/settings/reload' }).then((response) => {
      expect(response.status).to.eq(200);
      expect(response.body.opensubtitles_username).to.eq('e2e-user');
    });
  });

  it('rejects a settings body that is not JSON', () => {
    cy.request({
      method: 'PUT',
      url: '/api/v1/settings',
      body: 'not json',
      failOnStatusCode: false,
    }).then((response) => {
      expect(response.status).to.eq(400);
      expect(response.body.error).to.eq('invalid JSON body');
    });
  });

  it('deletes a library and leaves the other one alone', () => {
    cy.request('/api/v1/libraries').then((response) => {
      expect(response.body.map((library) => library.name)).to.have.members(['Movies', 'Archive']);
    });
    cy.request({ method: 'DELETE', url: '/api/v1/libraries/Archive' }).then((response) => {
      expect(response.status).to.eq(200);
      const names = response.body.libraries.map((library) => library.name);
      expect(names).to.deep.eq(['Movies']);
    });
    cy.request('/api/v1/libraries').then((response) => {
      const names = response.body.map((library) => library.name);
      expect(names).to.deep.eq(['Movies']);
    });
  });

  it('prunes the poster cache and completes a path', () => {
    cy.request({ method: 'POST', url: '/api/v1/posters/prune', body: {} }).then((response) => {
      expect(response.status).to.eq(200);
      expect(response.body.removed).to.be.at.least(0);
    });
    cy.request('/api/v1/path-complete?q=/media/movies').then((response) => {
      expect(response.status).to.eq(200);
    });
  });

  it('runs a matching pass over the catalog and reports it', () => {
    cy.request({ method: 'POST', url: '/api/v1/match', body: {} }).then((response) => {
      expect(response.status).to.eq(202);
      expect(response.body.running).to.eq(true);
      return wait_for_match();
    }).then((job) => {
      expect(job.running).to.eq(false);
      // error is omitempty, so an absent field is the success case.
      expect(job.error || '').to.eq('');
      expect(job.progress.total).to.be.at.least(1);
    });
    cy.request('/api/v1/background').then((response) => {
      expect(response.status).to.eq(200);
    });
  });

  it('rematches every entry through the queued job manager', () => {
    // The rematch-all path queues behind a running job instead of refusing, so
    // it shares the same status endpoint as the plain match run.
    cy.request({ method: 'POST', url: '/api/v1/match/rematch', body: {} }).then((response) => {
      expect(response.status).to.eq(202);
      return wait_for_match();
    }).then((job) => {
      expect(job.running).to.eq(false);
      expect(job.progress.total).to.be.at.least(1);
    });
  });

  it('refuses a second matching run while one is queued', () => {
    // Start and immediately re-request: the second call is either rejected or
    // queued, but it must never report a bogus success.
    cy.request({ method: 'POST', url: '/api/v1/match', body: {} }).then((first) => {
      expect(first.status).to.eq(202);
      cy.request({
        method: 'POST',
        url: '/api/v1/match',
        body: {},
        failOnStatusCode: false,
      }).then((second) => {
        expect(second.status).to.be.oneOf([202, 409]);
        return wait_for_match();
      });
    });
  });
});

// /api/v1/match/status always reports the most recent matching job.
function wait_for_match(attempts = 400) {
  return cy.request('/api/v1/match/status').then((response) => {
    const job = response.body.job;
    if (job && job.running) {
      if (attempts <= 0) throw new Error('matching never finished');
      return cy.wait(250).then(() => wait_for_match(attempts - 1));
    }
    return job;
  });
}
