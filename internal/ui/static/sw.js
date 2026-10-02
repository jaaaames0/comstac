// comstac service worker — handles Web Push events and shows notifications.

self.addEventListener('push', function(event) {
  var data = {};
  if (event.data) {
    try { data = event.data.json(); } catch(e) {}
  }
  var title = data.title || 'comstac';
  var options = {
    body: data.body || 'New message',
    icon: '/static/icon-192.png',
    badge: '/static/icon-192.png',
    tag: data.tag || 'comstac',
    renotify: true,
    data: { messageId: data.message_id || null, url: data.url || null }
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', function(event) {
  event.notification.close();
  var data = event.notification.data || {};
  // Only same-origin relative paths are honoured.
  var target = (typeof data.url === 'string' && /^\/(?![\/\\])/.test(data.url)) ? data.url
    : (data.messageId ? '/?msg=' + data.messageId : '/');
  event.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function(list) {
      for (var i = 0; i < list.length; i++) {
        var c = list[i];
        if (c.url.indexOf(self.location.origin) === 0 && 'navigate' in c) {
          return c.focus().then(function() { return c.navigate(target); });
        }
      }
      return clients.openWindow(target);
    })
  );
});

// The push service rotated or expired the subscription. Re-subscribe with the
// same server key; the page re-registers the new endpoint with the server on
// its next load (the worker has no CSRF token to do so itself).
self.addEventListener('pushsubscriptionchange', function(event) {
  var old = event.oldSubscription;
  if (!old || !old.options || !old.options.applicationServerKey) return;
  event.waitUntil(self.registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: old.options.applicationServerKey
  }));
});
