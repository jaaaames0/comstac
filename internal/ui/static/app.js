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

  // Remembers that this device opted in, so a lost or rotated subscription is
  // re-created on the next visit instead of silently going quiet.
  var PUSH_WANTED = 'comstac.pushWanted';

  function pushSupported() {
    return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
  }

  async function postSubscription(sub) {
    await checkedFetch('/ui/push/subscribe', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken()},
      body: JSON.stringify({
        endpoint: sub.endpoint,
        p256dh: arrayBufferToBase64(sub.getKey('p256dh')),
        auth: arrayBufferToBase64(sub.getKey('auth'))
      })
    });
  }

  // On every app load: re-register this device's current subscription (the
  // server upserts by endpoint), and re-subscribe if the browser dropped it.
  async function syncPush() {
    var vapidKey = document.body.dataset.vapidKey || '';
    if (!pushSupported() || !vapidKey || Notification.permission !== 'granted') return;
    var reg = await navigator.serviceWorker.register('/sw.js');
    await navigator.serviceWorker.ready;
    var sub = await reg.pushManager.getSubscription();
    if (!sub) {
      if (localStorage.getItem(PUSH_WANTED) !== '1') return;
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(vapidKey)
      });
    }
    localStorage.setItem(PUSH_WANTED, '1');
    await postSubscription(sub);
  }

  // Reflect this device's own state on the accounts page; the server count
  // covers all devices.
  async function refreshPushButton() {
    var btn = document.getElementById('push-toggle');
    var state = document.getElementById('push-device-state');
    if (!btn) return;
    if (!pushSupported()) {
      btn.disabled = true;
      if (state) state.textContent = ' · not supported in this browser';
      return;
    }
    var reg = await navigator.serviceWorker.getRegistration('/sw.js');
    var sub = reg ? await reg.pushManager.getSubscription() : null;
    btn.textContent = sub ? 'disable on this device' : 'enable on this device';
    if (state) state.textContent = sub ? ' · this device: on' : ' · this device: off';
  }

  async function togglePush(btn) {
    var errMsg = document.getElementById('push-error');
    var vapidKey = btn.dataset.vapidKey || '';
    if (!errMsg || !vapidKey) return;

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
        localStorage.removeItem(PUSH_WANTED);
      } else {
        sub = await reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(vapidKey)
        });
        await postSubscription(sub);
        localStorage.setItem(PUSH_WANTED, '1');
      }
    } catch (err) {
      errMsg.textContent = 'push notification update failed';
      errMsg.style.display = 'block';
      console.error('push notification update failed');
    } finally {
      btn.disabled = false;
      refreshPushButton();
    }
  }

  document.addEventListener('click', function (event) {
    var btn = event.target.closest('#push-toggle');
    if (btn) togglePush(btn);
  });

  document.body.addEventListener('htmx:afterSettle', function () {
    if (document.getElementById('push-toggle')) refreshPushButton();
  });

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js');
    syncPush().catch(function () { console.error('push subscription sync failed'); });
  }
})();
