const VERSION = "__VERSION__";
const SHELL = "tt-shell-" + VERSION;
const ASSETS = ["/static/app.css", "/static/app.js", "/static/theme.js", "/static/img/icon.svg", "/static/img/placeholder.svg", "/offline"];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(SHELL).then((c) => c.addAll(ASSETS)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== SHELL).map((k) => caches.delete(k)))).then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== location.origin || url.pathname.startsWith("/api/")) return;
  if (url.pathname.startsWith("/static/")) {
    event.respondWith(caches.match(req.url.split("?")[0]).then((hit) => hit || fetch(req)));
    return;
  }
  if (req.mode === "navigate") {
    event.respondWith(fetch(req).catch(() => caches.match("/offline")));
  }
});

self.addEventListener("push", (event) => {
  let data = {};
  if (event.data) {
    try {
      data = event.data.json();
    } catch (err) {
      data = { title: "TVT", body: event.data.text() };
    }
  }
  const title = data.title || "TVT";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clients) => {
      clients.forEach((client) => {
        if (client.visibilityState !== "visible") client.postMessage({ type: "tvt-badge", unreadCount: data.unread_count });
      });
      if (clients.some((client) => client.visibilityState === "visible")) return undefined;
      return self.registration.showNotification(title, {
        body: data.body || "",
        icon: "/static/img/icon.svg",
        badge: "/static/img/icon.svg",
        data: { link: data.link || "/" },
      });
    })
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const link = (event.notification.data && event.notification.data.link) || "/";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clients) => {
      for (const client of clients) {
        if ("focus" in client) {
          client.navigate(link);
          return client.focus();
        }
      }
      return self.clients.openWindow(link);
    })
  );
});
