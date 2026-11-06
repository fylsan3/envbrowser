"use strict";
importScripts("/collect.js");
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));
self.addEventListener("message", (e) => {
  collectFP("Service Worker").then((r) => e.ports[0].postMessage(r));
});
