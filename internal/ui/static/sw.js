// comstac service worker — handles Web Push events and shows notifications.

// This worker has no page cache. Activate updated notification behavior as
// soon as it is downloaded, including while the app is open.
self.addEventListener('install', function(event) {
  event.waitUntil(self.skipWaiting());
});
self.addEventListener('activate', function(event) {
  event.waitUntil(clients.claim());
});

self.addEventListener('push', function(event) {
  var data = {};
  if (event.data) {
    try { data = event.data.json(); } catch(e) {}
  }
  var title = data.title || 'comstac';
  var options = {
    body: data.body || ((data.kind === 'mail' || data.kind === 'reminder') ? '' : 'New message'),
    icon: '/static/icon-192.png',
    badge: '/static/icon-192.png',
    tag: data.tag || 'comstac',
    renotify: true,
    actions: notificationActions(data),
    data: {
      kind: data.kind || null,
      messageId: data.message_id || null,
      url: data.url || null,
      eventId: data.event_id || null,
      occurrence: data.occurrence || null,
      sourceKey: data.source_key || null
    }
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

function notificationActions(data) {
  var actions = [];
  if (data.kind === 'mail' && data.message_id) {
    actions = [{action: 'trash', title: 'Trash'}, {action: 'read', title: 'Mark read'}];
  } else if (data.kind === 'reminder' && data.message_id) {
    actions = [{action: 'snooze_mail', title: 'Snooze 1 hour'}, {action: 'trash', title: 'Trash'}];
  } else if (data.kind === 'event' && data.event_id && data.occurrence && data.source_key) {
    actions = [{action: 'snooze_event', title: 'Snooze 10 minutes'}];
  }
  var limit = typeof Notification.maxActions === 'number' ? Notification.maxActions
    : ('actions' in Notification.prototype ? 2 : 0);
  return actions.slice(0, Math.max(0, limit));
}

function notificationTarget(data) {
  if (typeof data.url === 'string' && /^\/(?![\/\\])/.test(data.url)) {
    var url = new URL(data.url, self.location.origin);
    if (url.origin === self.location.origin) return url.href;
  }
  return data.messageId ? '/?msg=' + encodeURIComponent(data.messageId) : '/';
}

self.addEventListener('notificationclick', function(event) {
  event.notification.close();
  var data = event.notification.data || {};
  if (event.action) {
    event.waitUntil(applyNotificationAction(event.notification, event.action, data));
    return;
  }
  var target = notificationTarget(data);
  event.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function(list) {
      for (var i = 0; i < list.length; i++) {
        var c = list[i];
        if (new URL(c.url).origin === self.location.origin && 'navigate' in c) {
          return c.focus().then(function() { return c.navigate(target); });
        }
      }
      return clients.openWindow(target);
    })
  );
});

async function applyNotificationAction(notification, action, data) {
  try {
    // Fetch a fresh CSRF token using the HttpOnly session cookie. No page needs
    // to be open, and logging out or expiring the session revokes actions too.
    var tokenResponse = await fetch('/api/push/action-token', {
      credentials: 'same-origin', cache: 'no-store', redirect: 'error'
    });
    if (!tokenResponse.ok) throw {status: tokenResponse.status};
    var token = (await tokenResponse.json()).csrf_token;
    if (!token) throw {status: 401};
    var response = await fetch('/api/push/action', {
      method: 'POST', credentials: 'same-origin', redirect: 'error',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': token},
      body: JSON.stringify({
        action: action, message_id: data.messageId || 0, event_id: data.eventId || 0,
        occurrence: data.occurrence || '', source_key: data.sourceKey || ''
      })
    });
    if (!response.ok) throw {status: response.status};
  } catch (err) {
    var body = (err.status === 401 || err.status === 403)
      ? 'Sign in to Comstac to complete this action.'
      : (err.status === 404 ? 'This message or reminder is no longer available.'
        : 'Action failed. Tap to open Comstac, or retry the action.');
    await self.registration.showNotification(notification.title, {
      body: body, icon: '/static/icon-192.png', badge: '/static/icon-192.png',
      tag: notification.tag, renotify: true, data: data,
      actions: err.status === 404 ? [] : notification.actions
    });
    return;
  }
  // Clear other notifications for the same mail on this device. A failure to
  // refresh windows must not report an already committed action as failed.
  if (data.messageId) {
    try {
      var notifications = await self.registration.getNotifications();
      notifications.forEach(function(n) {
        if (n.data && n.data.messageId === data.messageId) n.close();
      });
      var windows = await clients.matchAll({type: 'window', includeUncontrolled: true});
      windows.forEach(function(c) {
        c.postMessage({type: 'notification-action-complete', messageId: data.messageId, action: action});
      });
    } catch (_) {}
  }
}

// The push service rotated or expired the subscription. Re-subscribe with the
// same server key; the page re-registers the new endpoint with the server on
// its next authenticated load.
self.addEventListener('pushsubscriptionchange', function(event) {
  var old = event.oldSubscription;
  if (!old || !old.options || !old.options.applicationServerKey) return;
  event.waitUntil(self.registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: old.options.applicationServerKey
  }));
});
