/* Run against an isolated Pageport instance. Requires Playwright on the test machine.
 * PAGEPORT_URL=http://127.0.0.1:18087 node tests/browser.cjs
 * PLAYWRIGHT_MODULE and PLAYWRIGHT_BROWSER may point to existing test installations.
 */
'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');

const base = process.env.PAGEPORT_URL || 'http://127.0.0.1:18087';
const output = path.resolve(process.env.PAGEPORT_QA_DIR || '.qa');

async function main() {
  await fs.mkdir(output, { recursive: true });
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_BROWSER ? { executablePath: process.env.PLAYWRIGHT_BROWSER } : {})
  });
  const errors = [];
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  try {
    await page.goto(base);
    await page.waitForFunction(() => document.querySelector('#server-status-text').textContent === 'Local server is running');
    let state = await (await page.request.get(base + '/api/state')).json();
    assert.equal(state.files.length, 0, 'Use an empty, isolated data directory for browser tests.');
    await page.screenshot({ path: path.join(output, 'desktop-empty.png'), fullPage: true });

    const samples = [
      { name: 'A chapter.epub', mimeType: 'application/epub+zip', buffer: Buffer.from('Synthetic EPUB transport fixture; not a real ebook.') },
      { name: 'Z field guide.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4\nSynthetic PDF transport fixture; not for import.\n%%EOF') },
      { name: 'Archive notes.txt', mimeType: 'text/plain', buffer: Buffer.from('A small local-network transfer test.') }
    ];
    await page.locator('#file-input').setInputFiles(samples);
    await page.waitForFunction(() => document.querySelectorAll('#book-list tr').length === 3);
    await page.waitForFunction(() => !document.querySelector('#dismiss-uploads').hidden);
    assert.match(await page.locator('#upload-title').textContent(), /3 books added/);
    await page.locator('#dismiss-uploads').click();
    await page.screenshot({ path: path.join(output, 'desktop-library.png'), fullPage: true });

    await page.locator('[data-filter="pdf"]').click();
    assert.equal(await page.locator('#book-list tr').count(), 1);
    assert.match(await page.locator('#book-list').textContent(), /Z field guide.pdf/);
    await page.locator('[data-filter="all"]').click();
    await page.locator('#search').fill('chapter');
    assert.equal(await page.locator('#book-list tr').count(), 1);
    await page.locator('#search').fill('missing book');
    assert.equal(await page.locator('#no-results').isVisible(), true);
    await page.locator('#clear-filters').click();
    assert.equal(await page.locator('#book-list tr').count(), 3);
    await page.locator('#sort').selectOption('name');
    assert.equal(await page.locator('.book-name').first().textContent(), 'A chapter.epub');
    await page.locator('#sort').selectOption('size');
    assert.equal(await page.locator('.book-name').first().textContent(), 'Z field guide.pdf');

    await page.locator('#file-input').setInputFiles({ name: 'library-loan.acsm', mimeType: 'application/octet-stream', buffer: Buffer.from('license') });
    await page.waitForFunction(() => document.querySelector('#upload-list').textContent.includes('DRM license'));
    assert.equal(await page.locator('#book-list tr').count(), 3);
    await page.locator('#dismiss-uploads').click();

    state = await (await page.request.get(base + '/api/state')).json();
    const readerBase = state.addresses.length ? new URL(state.addresses[0].url).origin : base;
    const readerContext = await browser.newContext({ javaScriptEnabled: false, acceptDownloads: true, viewport: { width: 600, height: 800 } });
    const reader = await readerContext.newPage();
    if (readerBase !== base) {
      await reader.goto(readerBase);
      await reader.getByLabel('Six-digit pairing code').fill(state.sessionCode);
      await reader.getByRole('button', { name: 'Open shared library' }).click();
      assert.equal((await reader.request.get(readerBase + '/api/state')).status(), 403, 'LAN cannot access desktop API');
    } else {
      await reader.goto(base + '/read/' + state.sessionCode);
    }
    assert.equal(await reader.locator('script').count(), 0, 'Reader must not require scripts');
    assert.equal(await reader.locator('.download').count(), 3);
    const epubLink = reader.getByRole('link', { name: 'Download EPUB', exact: true });
    const downloadURL = new URL(await epubLink.getAttribute('href'), readerBase).href;
    assert.ok(downloadURL.endsWith('.epub'));
    const downloadPromise = reader.waitForEvent('download');
    await epubLink.click();
    const download = await downloadPromise;
    assert.equal(download.suggestedFilename(), 'A chapter.epub');
    const downloadedBytes = await fs.readFile(await download.path());
    assert.deepEqual(downloadedBytes, samples[0].buffer);
    await reader.screenshot({ path: path.join(output, 'reader-no-javascript.png'), fullPage: true });
    await reader.getByLabel('Find a book').fill('guide');
    await reader.getByRole('button', { name: 'Find', exact: true }).click();
    assert.equal(await reader.locator('.download').count(), 1);
    assert.match(await reader.locator('h2').textContent(), /field guide/);

    await page.locator('[data-view="connect"]').click();
    await page.locator('.sharing-toggle').click();
    await page.waitForFunction(() => document.querySelector('#server-status-text').textContent === 'Sharing is paused');
    assert.equal((await reader.request.get(downloadURL)).status(), 503, 'Paused sharing must block downloads');
    await page.locator('.sharing-toggle').click();
    await page.waitForFunction(() => document.querySelector('#server-status-text').textContent === 'Local server is running');
    const oldCode = state.sessionCode;
    await page.locator('#reset-code').click();
    await page.waitForFunction(code => document.querySelector('.session-code').textContent !== code, oldCode);
    assert.equal((await reader.request.get(readerBase + '/read/' + oldCode)).status(), 403, 'Rotated code must invalidate old reader links');
    await page.screenshot({ path: path.join(output, 'desktop-connect.png'), fullPage: true });
    await readerContext.close();

    await page.locator('[data-view="library"]').click();
    await page.getByRole('button', { name: 'Remove Archive notes.txt', exact: true }).click();
    await page.getByRole('button', { name: 'Remove book', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('#book-list tr').length === 2);
    await page.getByRole('button', { name: 'Remove A chapter.epub', exact: true }).click();
    await page.keyboard.press('Escape');
    await page.waitForTimeout(250);
    state = await (await page.request.get(base + '/api/state')).json();
    assert.equal(state.files.length, 2, 'Escape must cancel deletion even after a confirmed deletion');
    await page.getByRole('button', { name: 'Remove A chapter.epub', exact: true }).click();
    await page.getByRole('button', { name: 'Keep book', exact: true }).click();

    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(output, 'mobile-library.png'), fullPage: true });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'Mobile page must not overflow horizontally');
    await page.locator('[data-view="connect"]').click();
    await page.screenshot({ path: path.join(output, 'mobile-connect.png'), fullPage: true });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'Mobile connection view must not overflow');
    await page.locator('[data-view="guide"]').click();
    await page.locator('#view-guide').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#view-guide').isVisible(), true);
    await page.setViewportSize({ width: 320, height: 740 });
    await page.locator('[data-view="library"]').click();
    await page.locator('#view-library').waitFor({ state: 'visible' });
    await page.screenshot({ path: path.join(output, 'mobile-320-library.png'), fullPage: true });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), '320px library view must not overflow');
    await page.locator('[data-view="connect"]').click();
    await page.locator('#view-connect').waitFor({ state: 'visible' });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), '320px connection view must not overflow');

    assert.deepEqual(errors, [], 'No desktop browser JavaScript, CSP, or console errors');
    console.log('PASS: desktop/mobile UI, upload, rejection, search/filter/sort, no-JS reader download, remote API isolation, pause, code rotation, and deletion cancellation.');
    console.log('Screenshots: ' + output);
  } finally {
    await browser.close();
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
