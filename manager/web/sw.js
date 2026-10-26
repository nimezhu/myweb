// The file manager installed as an app: "Share → myweb" on a phone posts
// the files here. They wait in a cache until the page (opened at #share)
// asks where to put them and uploads them as the signed-in user. Nothing
// else is intercepted.
const INBOX = "myweb-share-inbox";

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== "POST" || url.pathname !== new URL("share-target", self.registration.scope).pathname) return;
  e.respondWith((async () => {
    const form = await e.request.formData();
    await caches.delete(INBOX); // one share at a time
    const cache = await caches.open(INBOX);
    let n = 0;
    for (const f of form.getAll("files")) {
      if (typeof f === "string") continue;
      await cache.put(new Request(`share-inbox/${n++}`), new Response(f, {
        headers: { "Content-Type": f.type || "application/octet-stream", "X-Name": encodeURIComponent(f.name || "shared") },
      }));
    }
    const text = ["title", "text", "url"].map((k) => form.get(k)).filter(Boolean).join("\n");
    if (!n && text) {
      await cache.put(new Request("share-inbox/text"), new Response(text + "\n", {
        headers: { "Content-Type": "text/plain; charset=utf-8", "X-Name": "shared.txt" },
      }));
    }
    return Response.redirect(new URL("./#share", self.registration.scope).href, 303);
  })());
});
