// OmniBox AI service worker: it makes the panel installable on a phone. Nothing is cached, so an
// update reaches the phone at once and no request text stays on it; when the PC does not answer
// (asleep, no WireGuard), the page explains it instead of a browser error.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', e => {
  if (e.request.mode !== 'navigate') return;
  e.respondWith(fetch(e.request).catch(() => new Response(
    '<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>OMNIBOX AI</title>' +
    '<body style="background:#000;color:#DEE4EE;font:16px/1.5 system-ui,sans-serif;padding:24px">' +
    '<h1 style="font-size:22px">ПК недоступен</h1><p>Панель OmniBox AI не отвечает: ПК спит или телефон не в домашней сети (проверьте WireGuard).</p>' +
    '<p>Разбудите ПК кнопкой в Home Assistant и обновите страницу.</p></body></html>',
    {status: 503, headers: {'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store'}})));
});
