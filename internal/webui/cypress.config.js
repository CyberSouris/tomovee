import { defineConfig } from 'cypress';

export default defineConfig({
  e2e: {
    baseUrl: process.env.CYPRESS_BASE_URL || 'http://127.0.0.1:4173',
    specPattern: 'e2e/**/*.cy.js',
    supportFile: 'e2e/support/e2e.js',
    video: false,
    screenshotOnRunFailure: false,
  },
});