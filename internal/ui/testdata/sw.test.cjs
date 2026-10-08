const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, '../static/sw.js'), 'utf8');
const plain = value => JSON.parse(JSON.stringify(value));

function worker({limit = 2, fetchHandler} = {}) {
  const listeners = {};
  const shown = [];
  const requests = [];
  const opened = [];
  const messages = [];
  const notifications = [];
  const windows = [];
  class Notification {}
  if (limit !== null) Notification.maxActions = limit;
  const clients = {
    claim: async () => {},
    matchAll: async () => windows,
    openWindow: async url => opened.push(url),
  };
  const context = vm.createContext({
    URL, Notification, clients,
    self: {
      skipWaiting: async () => {},
      location: {origin: 'https://comstac.example'},
      addEventListener: (name, callback) => { listeners[name] = callback; },
      registration: {
        showNotification: async (title, options) => shown.push({title, options}),
        getNotifications: async () => notifications,
      },
    },
    fetch: async (url, options) => {
      requests.push({url, options});
      if (fetchHandler) return fetchHandler(url, options);
      return url.endsWith('action-token')
        ? {ok: true, json: async () => ({csrf_token: 'session-csrf'})}
        : {ok: true, status: 204};
    },
  });
  vm.runInContext(source, context);
  async function dispatch(name, event) {
    let work;
    listeners[name]({...event, waitUntil: promise => { work = promise; }});
    await work;
  }
  async function push(payload) {
    await dispatch('push', {data: {json: () => payload}});
    const {title, options} = shown.at(-1);
    return {title, tag: options.tag, body: options.body, data: options.data, actions: options.actions,
      close() { this.closed = true; }};
  }
  return {shown, requests, opened, messages, windows, notifications, push,
    click: (notification, action = '') => dispatch('notificationclick', {notification, action})};
}

test('mail preview and actions survive the push event', async () => {
  const w = worker();
  const n = await w.push({kind: 'mail', title: 'Alex · Delivery', body: 'Arrives Thursday.', message_id: 7, tag: 'mail-7'});
  assert.equal(n.title, 'Alex · Delivery');
  assert.equal(n.body, 'Arrives Thursday.');
  assert.deepEqual(plain(n.actions), [{action: 'trash', title: 'Trash'}, {action: 'read', title: 'Mark read'}]);
  assert.equal(n.data.messageId, 7);
  assert.equal(n.tag, 'mail-7');
});

test('action count adapts to browsers, including browsers without action support', async () => {
  for (const [limit, count] of [[1, 1], [0, 0], [null, 0]]) {
    const w = worker({limit});
    const n = await w.push({kind: 'mail', message_id: 7, body: 'Preview'});
    assert.equal(n.actions.length, count);
    assert.equal(n.body, 'Preview');
    await w.click(n);
    assert.deepEqual(w.opened, ['/?msg=7']);
    assert.equal(w.requests.length, 0);
  }
});

test('resurfaced mail and calendar alarms have different action sets', async () => {
  const w = worker();
  const mail = await w.push({kind: 'reminder', message_id: 7});
  assert.deepEqual(plain(mail.actions), [{action: 'snooze_mail', title: 'Snooze 1 hour'}, {action: 'trash', title: 'Trash'}]);
  const alarm = await w.push({kind: 'event', event_id: 2, occurrence: '2026-10-06T00:45:00Z', source_key: 'offset:60', url: '/?calendar=2026-10-06'});
  assert.deepEqual(plain(alarm.actions), [{action: 'snooze_event', title: 'Snooze 10 minutes'}]);
  await w.click(alarm, 'snooze_event');
  assert.deepEqual(JSON.parse(w.requests[1].options.body), {
    action: 'snooze_event', message_id: 0, event_id: 2, occurrence: '2026-10-06T00:45:00Z', source_key: 'offset:60',
  });
  assert.equal(w.opened.length, 0);
});

test('a successful action uses the session and fresh CSRF without opening the app', async () => {
  const w = worker();
  const n = await w.push({kind: 'mail', message_id: 7, title: 'Delivery'});
  const sibling = {data: {messageId: 7}, close() { this.closed = true; }};
  const other = {data: {messageId: 8}, close() { this.closed = true; }};
  w.notifications.push(sibling, other);
  w.windows.push({postMessage: message => w.messages.push(message)});
  await w.click(n, 'trash');
  assert.equal(w.requests.length, 2);
  assert.equal(w.requests[0].url, '/api/push/action-token');
  assert.equal(w.requests[0].options.cache, 'no-store');
  assert.equal(w.requests[1].url, '/api/push/action');
  assert.equal(w.requests[1].options.method, 'POST');
  assert.equal(w.requests[1].options.headers['X-CSRF-Token'], 'session-csrf');
  for (const request of w.requests) assert.equal(request.options.credentials, 'same-origin');
  assert.equal(JSON.parse(w.requests[1].options.body).action, 'trash');
  assert.equal(n.closed, true);
  assert.equal(sibling.closed, true);
  assert.equal(other.closed, undefined);
  assert.equal(w.opened.length, 0);
  assert.deepEqual(plain(w.messages), [{type: 'notification-action-complete', messageId: 7, action: 'trash'}]);
  assert.equal(w.shown.length, 1);
});

test('expired session leaves a sign-in notification and performs no mutation', async () => {
  const w = worker({fetchHandler: async () => ({ok: false, status: 401})});
  const n = await w.push({kind: 'mail', title: 'Delivery', message_id: 7, tag: 'mail-7'});
  await w.click(n, 'read');
  assert.equal(w.requests.length, 1);
  assert.match(w.shown[1].options.body, /Sign in/);
  assert.equal(w.shown[1].options.tag, 'mail-7');
  await w.click({...n, data: w.shown[1].options.data});
  assert.deepEqual(w.opened, ['/?msg=7']);
});

test('offline and server failures retain actions for retry', async () => {
  for (const status of [0, 500]) {
    const w = worker({fetchHandler: async url => {
      if (url.endsWith('action-token')) return {ok: true, json: async () => ({csrf_token: 'token'})};
      if (status === 0) throw new Error('offline');
      return {ok: false, status};
    }});
    const n = await w.push({kind: 'mail', message_id: 7, title: 'Delivery'});
    await w.click(n, 'trash');
    assert.match(w.shown[1].options.body, /Action failed/);
    assert.deepEqual(plain(w.shown[1].options.actions), plain(n.actions));
    assert.equal(w.opened.length, 0);
  }
});

test('obsolete reminder failure removes ineffective actions', async () => {
  const w = worker({fetchHandler: async url => url.endsWith('action-token')
    ? {ok: true, json: async () => ({csrf_token: 'token'})} : {ok: false, status: 404}});
  const n = await w.push({kind: 'event', event_id: 2, occurrence: '2026-10-06T00:45:00Z', source_key: 'offset:60', title: 'Reminder'});
  await w.click(n, 'snooze_event');
  assert.match(w.shown[1].options.body, /no longer available/);
  assert.equal(w.shown[1].options.actions.length, 0);
});

test('window refresh failure does not report a committed action as failed', async () => {
  const w = worker();
  w.windows.push({postMessage: () => { throw new Error('window closed'); }});
  const n = await w.push({kind: 'mail', message_id: 7});
  await w.click(n, 'read');
  assert.equal(w.requests.length, 2);
  assert.equal(w.shown.length, 1);
});

test('notification click focuses an existing app and honors calendar paths', async () => {
  const w = worker();
  const navigated = [];
  let focused = false;
  w.windows.push({url: 'https://comstac.example/', focus: async () => { focused = true; }, navigate: async url => navigated.push(url)});
  const n = await w.push({url: '/?calendar=2026-10-06'});
  await w.click(n);
  assert.equal(focused, true);
  assert.deepEqual(navigated, ['https://comstac.example/?calendar=2026-10-06']);
  assert.equal(w.opened.length, 0);
});

test('notification targets cannot navigate to another origin', async () => {
  for (const url of ['https://evil.example/', '//evil.example/', '/\\evil.example/', '/\n/evil.example/']) {
    const w = worker();
    const n = await w.push({url, message_id: 7});
    await w.click(n);
    assert.deepEqual(w.opened, ['/?msg=7']);
  }
});
