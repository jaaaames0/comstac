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
    tag: 'comstac-mail',
    renotify: true,
    data: { url: '/' }
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', function(event) {
  event.notification.close();
  // Use a cache-busting query param so navigate() triggers an actual reload
  // even when already at '/'.
  var target = '/?n=' + Date.now();
  event.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function(list) {
      for (var i = 0; i < list.length; i++) {
        var c = list[i];
        if (c.url.indexOf(self.location.origin) === 0 && 'navigate' in c) {
          return c.focus().then(function() { return c.navigate(target); });
        }
      }
      return clients.openWindow('/');
    })
  );
});
