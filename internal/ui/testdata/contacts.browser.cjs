// Optional browser check against TestContactBrowserFixture's disposable server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {chromium} = require(process.env.COMSTAC_PLAYWRIGHT_MODULE || 'playwright-core');
const {url} = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));

(async function () {
  const browser = await chromium.launch({headless: true});
  const context = await browser.newContext({viewport: {width: 1280, height: 900}});
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  async function waitText(text) { await page.getByText(text, {exact: true}).waitFor(); }
  async function contacts() { await page.getByRole('button', {name: 'contacts', exact: true}).click(); await page.getByRole('button', {name: 'add contact', exact: true}).waitFor(); }
  async function suggestions(input, query) {
    await input.fill(query);
    await page.getByRole('option').first().waitFor();
  }
  try {
    await page.goto(url);
    await contacts();
    await waitText('<img src=x onerror=alert(1)>');
    assert.equal(await page.locator('.contact-list img').count(), 0);
    await page.getByRole('button', {name: 'add contact', exact: true}).click();
    await page.getByLabel('name', {exact: true}).fill('Doe, José');
    await page.getByLabel('email', {exact: true}).fill('invalid');
    await page.getByRole('button', {name: 'save contact', exact: true}).click();
    await page.getByRole('alert').waitFor();
    assert.equal(await page.getByLabel('name', {exact: true}).inputValue(), 'Doe, José');
    await page.getByLabel('email', {exact: true}).fill('jose@example.test');
    await page.getByRole('button', {name: 'save contact', exact: true}).click();
    await waitText('Contact saved.');
    await page.getByRole('button', {name: 'Edit jose@example.test', exact: true}).click();
    await page.getByLabel('name', {exact: true}).fill('Doe, José Updated');
    await page.getByRole('button', {name: 'save contact', exact: true}).click();
    await waitText('Doe, José Updated');
    await page.getByRole('button', {name: 'add contact', exact: true}).click();
    await page.getByLabel('name', {exact: true}).fill('Duplicate');
    await page.getByLabel('email', {exact: true}).fill('jose@EXAMPLE.TEST');
    await page.getByRole('button', {name: 'save contact', exact: true}).click();
    await page.getByRole('alert').waitFor();
    assert.match(await page.getByRole('alert').textContent(), /already saved/);
    await page.getByRole('button', {name: 'cancel', exact: true}).click();
    await page.getByLabel('search contacts', {exact: true}).fill('José');
    await page.locator('.contact-search button').click();
    await waitText('Doe, José Updated');
    await page.waitForFunction(() => document.querySelectorAll('.contact-row').length === 1);

    // Saving a sender opens a prefilled form, without adding it on GET.
    await page.locator('.msg-row').first().click();
    await page.getByRole('button', {name: 'save contact', exact: true}).click();
    await page.getByLabel('email', {exact: true}).waitFor();
    assert.equal(await page.getByLabel('email', {exact: true}).inputValue(), 'sender@example.test');
    let result = await context.request.get(url + '/ui/contacts/search?q=sender');
    assert.deepEqual(await result.json(), []);
    await page.getByRole('button', {name: 'save contact', exact: true}).click();
    await waitText('Contact saved.');

    await page.getByRole('button', {name: 'compose', exact: true}).click();
    const to = page.getByLabel('to', {exact: true});
    await suggestions(to, 'first@example.test, ja');
    await to.press('ArrowDown');
    assert.equal(await to.getAttribute('aria-activedescendant'), 'contacts-recipient-to-0');
    await to.press('Enter');
    assert.equal(await to.inputValue(), 'first@example.test, "Doe, Jane" <jane@example.test>');
    assert.equal(await to.getAttribute('aria-expanded'), 'false');
    await page.locator('.cc-toggle summary').click();
    const cc = page.getByLabel('cc', {exact: true});
    await suggestions(cc, 'jo');
    await cc.press('ArrowUp');
    await cc.press('Enter');
    assert.equal(await cc.inputValue(), '"Doe, José Updated" <jose@example.test>');
    const bcc = page.getByLabel('bcc', {exact: true});
    await suggestions(bcc, 'al');
    await bcc.press('ArrowDown'); await bcc.press('ArrowDown'); await bcc.press('ArrowUp');
    assert.equal(await bcc.getAttribute('aria-activedescendant'), 'contacts-recipient-bcc-0');
    await bcc.press('Escape');
    assert.equal(await bcc.inputValue(), 'al');
    assert.equal(await bcc.getAttribute('aria-expanded'), 'false');
    await suggestions(bcc, 'al');
    await page.getByRole('option', {name: 'Alex Example <alex@example.test>', exact: true}).click();
    assert.equal(await bcc.inputValue(), '"Alex Example" <alex@example.test>');
    await to.fill('manual@example.test');
    await page.waitForTimeout(250);
    assert.equal(await to.inputValue(), 'manual@example.test');
    assert.equal(await to.getAttribute('aria-expanded'), 'false');

    // Late results and failed requests cannot overwrite or reopen a new draft.
    await page.route('**/ui/contacts/search?*', async route => {
      if (new URL(route.request().url()).searchParams.get('q') === 'ja') {
        await page.waitForTimeout(350);
        await route.fulfill({contentType: 'application/json', body: JSON.stringify([{name:'Old',email:'old@example.test',recipient:'old@example.test'}])});
      } else await route.continue();
    });
    await to.fill('ja');
    await page.waitForTimeout(200);
    await suggestions(to, 'alex');
    await page.waitForTimeout(400);
    assert.equal(await page.getByRole('option').first().textContent(), 'Alex Example <alex@example.test>');
    assert.equal(await to.inputValue(), 'alex');
    await to.press('Escape');
    await page.unroute('**/ui/contacts/search?*');
    await page.route('**/ui/contacts/search?*', route => route.fulfill({status: 401, body: 'Sign in'}));
    await to.fill('unknown@example.test');
    await page.waitForTimeout(250);
    assert.equal(await to.inputValue(), 'unknown@example.test');
    await page.unroute('**/ui/contacts/search?*');
    await to.fill('manual@example.test');
    await page.getByLabel('subject', {exact: true}).fill('Browser fixture');
    await page.getByLabel('body', {exact: true}).fill('Preserved body');
    await page.getByRole('button', {name: 'send', exact: true}).click();
    await waitText('message sent');
    result = await context.request.get(url + '/preview/sends');
    const sent = await result.json();
    assert.equal(sent.Calls, 1);
    assert.equal(sent.Message.BodyText, 'Preserved body');
    assert.equal(sent.Message.To.length, 1);
    assert.equal(sent.Message.CC.length, 1);
    assert.equal(sent.Message.BCC.length, 1);

    // A replaced reply form gets the same keyboard autocomplete behavior.
    await page.locator('.msg-row').first().click();
    await page.locator('.dropdown-btn').filter({hasText: 'reply'}).click();
    await page.getByRole('button', {name: 'reply', exact: true}).click();
    await suggestions(to, 'ja');
    await to.press('ArrowDown'); await to.press('Enter');
    assert.equal(await to.inputValue(), '"Doe, Jane" <jane@example.test>');
    await to.fill('ja'); await to.press('Tab'); await page.waitForTimeout(250);
    assert.equal(await to.getAttribute('aria-expanded'), 'false');

    // Mobile viewport and a touch-capable context exercise tap selection.
    const mobile = await browser.newContext({viewport: {width: 390, height: 844}, isMobile: true, hasTouch: true});
    const phone = await mobile.newPage();
    phone.on('pageerror', error => errors.push(error.message));
    await phone.goto(url);
    await phone.getByRole('button', {name: 'compose', exact: true}).tap();
    const phoneTo = phone.getByLabel('to', {exact: true});
    assert.equal(await phone.locator('.cc-toggle').getAttribute('open'), null);
    await phoneTo.fill('ja');
    await phone.getByRole('option', {name: 'Doe, Jane <jane@example.test>', exact: true}).tap();
    assert.equal(await phoneTo.inputValue(), '"Doe, Jane" <jane@example.test>');
    assert.equal(await phoneTo.getAttribute('aria-expanded'), 'false');
    assert.equal(await phone.locator('.cc-toggle').getAttribute('open'), null, 'tap must not click a control under the removed option');
    await phone.screenshot({path: process.argv[2].replace(/\.json$/, '-mobile.png')});
    await mobile.close();

    await contacts();
    await page.getByRole('button', {name: 'Edit jose@example.test', exact: true}).click();
    page.once('dialog', dialog => dialog.accept());
    await page.getByRole('button', {name: 'delete contact', exact: true}).click();
    await waitText('Contact deleted.');
    result = await context.request.get(url + '/ui/contacts/search?q=jose');
    assert.deepEqual(await result.json(), []);
    assert.deepEqual(errors, []);
    console.log('PASS: CRUD, sender save, search, quoted names, To/CC/BCC, keyboard/Escape/Tab, stale/failed searches, draft preservation, reply swaps and mobile taps.');
  } finally {
    await context.request.post(url + '/preview/stop');
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
