// The admin UI's service worker: what lets it install as an app and open
// without a network. vite.config.ts builds it into the UI's dist as /admin/sw.js,
// filling in VERSION and PRECACHE from the build.
//
// It keeps exactly one kind of thing: the app's own static files — the page,
// its script and stylesheet, its icons. Those hold no data. Everything else
// goes to the network untouched, and two things are never even looked at:
// the JSON API (/admin/api/), which carries the relay's state and the
// session's answers, and a token's QR image, which is a live credential. No
// group, token or audit entry is ever written to a cache on this device.
//
// Network first, cache second: an admin always sees the relay's current
// build when it can be reached, and the last one it saw when it cannot. A
// signed-out or offline app then says so itself.

const VERSION = "__VERSION__";
const PRECACHE = __PRECACHE__;
const CACHE = `tpp-admin-${VERSION}`;

// The page is the same for every screen, so one copy serves them all.
const SHELL = "/admin/__shell__";

self.addEventListener("install", (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE);
      await cache.addAll(PRECACHE);
      // The sign-in screen is public, so the page can be fetched without a
      // session. With one, the relay redirects it to the dashboard, which
      // serves the same page; either way what is kept is the page itself,
      // copied so that it is not marked as the end of a redirect.
      const page = await fetch("/admin/login", { credentials: "same-origin" });
      if (page.ok) await cache.put(SHELL, new Response(await page.blob(), { headers: page.headers }));
      // Nothing is kept across builds, so an update takes over at once.
      await self.skipWaiting();
    })(),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      for (const name of await caches.keys()) {
        if (name.startsWith("tpp-admin-") && name !== CACHE) await caches.delete(name);
      }
      await self.clients.claim();
    })(),
  );
});

/** private is everything the worker must not touch. */
function isPrivate(path) {
  return path.startsWith("/admin/api/") || (path.startsWith("/admin/tokens/") && path.endsWith("/qr.png"));
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin || !url.pathname.startsWith("/admin/") || isPrivate(url.pathname)) return;

  if (request.mode === "navigate") {
    // A redirect (to sign in, say) goes back to the browser as it is; only a
    // network failure falls back to the kept page.
    event.respondWith(fetch(request).catch(() => caches.match(SHELL).then((r) => r ?? Response.error())));
    return;
  }

  if (!PRECACHE.includes(url.pathname)) return;
  event.respondWith(
    fetch(request)
      .then((response) => {
        if (response.ok) {
          const copy = response.clone();
          event.waitUntil(caches.open(CACHE).then((cache) => cache.put(url.pathname, copy)));
        }
        return response;
      })
      .catch(() => caches.match(url.pathname).then((r) => r ?? Response.error())),
  );
});
