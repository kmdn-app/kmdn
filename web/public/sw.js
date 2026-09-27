// kmdn service worker: shows browser pushes and opens their page on click.
self.addEventListener("push", (event) => {
  let m = { title: "kmdn", body: "", url: "/" };
  try {
    m = { ...m, ...event.data.json() };
  } catch {
    /* not JSON */
  }
  event.waitUntil(self.registration.showNotification(m.title, { body: m.body, tag: m.tag || undefined, data: { url: m.url }, icon: "/favicon.svg", badge: "/favicon.svg" }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = new URL(event.notification.data?.url || "/", self.location.origin);
  if (url.origin !== self.location.origin) return;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
      for (const c of list) {
        if ("focus" in c) {
          c.navigate(url.href);
          return c.focus();
        }
      }
      return self.clients.openWindow(url.href);
    }),
  );
});
