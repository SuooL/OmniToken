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

  // The bearer token, for a server that is reachable from other machines
  // (ADR-0016). A loopback-only server needs none and this stays empty — which
  // is why the single-machine panel keeps working with nothing configured.
  //
  // Deliberately the SAME key the settings page already used for writes:
  // `cfg.Token` is one shared secret, so a second box to fill in with the same
  // string would only be a way to get them out of step.
  //
  // localStorage rather than a cookie: the token belongs to this browser, and
  // the server is stateless about who is reading.
  token: "",
  TOKEN_KEY: "omnitoken.token",

  loadToken() {
    try {
      this.token = localStorage.getItem(this.TOKEN_KEY) || "";
    } catch (e) {
      // Private mode or storage disabled; the panel still works against a
      // loopback server.
      this.token = "";
    }
    return this.token;
  },

  saveToken(t) {
    this.token = t || "";
    try {
      if (this.token) localStorage.setItem(this.TOKEN_KEY, this.token);
      else localStorage.removeItem(this.TOKEN_KEY);
    } catch (e) { /* nothing we can do, and nothing that should break a render */ }
  },

  url(path) {
    return this.base + path;
  },

  headers(extra) {
    const h = Object.assign({}, extra);
    if (this.token) h["Authorization"] = "Bearer " + this.token;
    return h;
  },

  async get(path) {
    const res = await fetch(this.url(path), { headers: this.headers() });
    // Every caller sits inside a try/catch that surfaces the message, so
    // failing loudly here beats letting res.json() throw a parse error on
    // whatever the server returned instead of JSON. 401 is named: "wrong
    // token" and "server not running" are different problems with different
    // fixes, and a bare status number does not tell them apart.
    if (res.status === 401) throw new Error(`${path} → 401 未授权:设置页填写读取 token`);
    if (!res.ok) throw new Error(`${path} → HTTP ${res.status}`);
    return res.json();
  },

  // Returns the raw Response rather than parsed JSON: the settings page
  // distinguishes 401 from other failures and reads the body as text for its
  // error note.
  put(path, body, token) {
    return fetch(this.url(path), {
      method: "PUT",
      headers: this.headers({
        "Content-Type": "application/json",
        // The write token is passed explicitly by the settings page and wins:
        // it is what the user just typed into the box.
        ...(token ? { Authorization: "Bearer " + token } : {}),
      }),
      body: JSON.stringify(body),
    });
  },

  // EventSource cannot carry a header, so the token rides as a query parameter
  // when there is one. Same credential, only channel the API allows — and the
  // server accepts it on this endpoint alone.
  stream(path) {
    const url = this.token
      ? this.url(path) + (path.includes("?") ? "&" : "?") + "access_token=" + encodeURIComponent(this.token)
      : this.url(path);
    return new EventSource(url);
  },
};
