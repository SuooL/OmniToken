"use strict";
// The single place this frontend talks to the server through.
//
// Two consumers share these files (ADR-0008). The browser is served from the
// same origin as the API. The desktop client is not: its webview runs on
// tauri://localhost while the server sits elsewhere, and the API deliberately
// sends no CORS headers — the read endpoints are unauthenticated, so allowing
// arbitrary origins would let any visited page read the user's usage data.
//
// The desktop build therefore swaps the bodies of get / put / stream for calls
// into its Rust side, which is not bound by the same-origin policy. Nothing
// outside this file needs to know which transport is in use.
const Api = {
  // Empty means same origin, which is what the browser wants. The desktop
  // client sets an absolute base such as "http://192.0.2.1:8787".
  base: "",

  url(path) {
    return this.base + path;
  },

  async get(path) {
    const res = await fetch(this.url(path));
    // Every caller sits inside a try/catch that surfaces the message, so
    // failing loudly here beats letting res.json() throw a parse error on
    // whatever the server returned instead of JSON.
    if (!res.ok) throw new Error(`${path} → HTTP ${res.status}`);
    return res.json();
  },

  // Returns the raw Response rather than parsed JSON: the settings page
  // distinguishes 401 from other failures and reads the body as text for its
  // error note.
  put(path, body, token) {
    const headers = { "Content-Type": "application/json" };
    if (token) headers["Authorization"] = "Bearer " + token;
    return fetch(this.url(path), {
      method: "PUT",
      headers,
      body: JSON.stringify(body),
    });
  },

  stream(path) {
    return new EventSource(this.url(path));
  },
};
