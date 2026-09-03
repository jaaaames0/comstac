(function () {
  'use strict';

  function csrfToken() {
    try {
      return JSON.parse(document.body.getAttribute('hx-headers'))['X-CSRF-Token'] || '';
    } catch (_) {
      return '';
    }
  }

  function urlBase64ToUint8Array(base64String) {
    var padding = '='.repeat((4 - base64String.length % 4) % 4);
    var base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/');
    var rawData = atob(base64);
    var outputArray = new Uint8Array(rawData.length);
    for (var i = 0; i < rawData.length; i++) {
      outputArray[i] = rawData.charCodeAt(i);
    }
    return outputArray;
  }

  function arrayBufferToBase64(buffer) {
    var bytes = new Uint8Array(buffer);
    var binary = '';
    for (var i = 0; i < bytes.byteLength; i++) {
      binary += String.fromCharCode(bytes[i]);
    }
    return btoa(binary);
  }

  async function checkedFetch(url, options) {
    var response = await fetch(url, options);
    if (!response.ok) {
      throw new Error('request failed');
    }
    return response;
  }

  async function togglePush(btn) {
    var status = document.getElementById('push-status');
    var errMsg = document.getElementById('push-error');
    var vapidKey = btn.dataset.vapidKey || '';
    if (!status || !errMsg || !vapidKey) return;

    errMsg.style.display = 'none';
    btn.disabled = true;
    try {
      var reg = await navigator.serviceWorker.register('/sw.js');
      var sub = await reg.pushManager.getSubscription();
      if (sub) {
        await checkedFetch('/ui/push/unsubscribe', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken()},
          body: JSON.stringify({endpoint: sub.endpoint})
        });
        await sub.unsubscribe();
        status.textContent = 'not subscribed';
        status.style.color = 'var(--muted)';
        btn.textContent = 'enable';
      } else {
        sub = await reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(vapidKey)
        });
        await checkedFetch('/ui/push/subscribe', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken()},
          body: JSON.stringify({
            endpoint: sub.endpoint,
            p256dh: arrayBufferToBase64(sub.getKey('p256dh')),
            auth: arrayBufferToBase64(sub.getKey('auth'))
          })
        });
        status.textContent = 'push notifications enabled (1 device)';
        status.style.color = 'var(--accent)';
        btn.textContent = 'disable';
      }
    } catch (err) {
      errMsg.textContent = 'push notification update failed';
      errMsg.style.display = 'block';
      console.error('push notification update failed');
    } finally {
      btn.disabled = false;
    }
  }

  document.addEventListener('click', function (event) {
    var btn = event.target.closest('#push-toggle');
    if (btn) togglePush(btn);
  });

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js');
  }
})();
