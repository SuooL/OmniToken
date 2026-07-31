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
class APIError extends Error {
  constructor(path, status) {
    const detail = status === 0
      ? "网络连接失败"
      : status === 401
        ? "401 未授权:设置页填写读取 token"
        : `HTTP ${status}`;
    super(`${path} → ${detail}`);
    this.name = "APIError";
    this.status = status;
  }
}

function isCurrentGeneration(current, captured) {
  return current === captured;
}

function canCommitRevision(current, sent) {
  return current === sent;
}

function classifyAPIError(error) {
  if (error instanceof APIError && error.status === 401) {
    return {
      kind: "unauthorized",
      title: "需要访问令牌",
      detail: "请到设置页填写读取 token 后重试。",
    };
  }
  if (error instanceof APIError && error.status === 0) {
    return {
      kind: "error",
      title: "服务不可达",
      detail: "请检查服务地址与网络连接后重试。",
    };
  }
  return {
    kind: "error",
    title: "加载失败",
    detail: error && error.message ? error.message : "发生未知错误,请稍后重试。",
  };
}

function renderState(container, { kind, title, detail = "", action = null }) {
  let region = container.querySelector(".view-state");
  if (kind === "ready") {
    if (region) region.remove();
    container.removeAttribute("aria-busy");
    return;
  }
  if (!region) {
    region = document.createElement("section");
    region.setAttribute("aria-live", "polite");
    region.setAttribute("aria-atomic", "true");
    container.prepend(region);
  }
  region.className = `view-state view-state-${kind}`;
  region.replaceChildren();

  const heading = document.createElement("strong");
  heading.textContent = title;
  region.appendChild(heading);
  if (detail) {
    const copy = document.createElement("span");
    copy.textContent = detail;
    region.appendChild(copy);
  }
  if (action && action.label && action.run) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "ghost-btn";
    button.textContent = action.label;
    button.addEventListener("click", action.run);
    region.appendChild(button);
  }
  container.toggleAttribute("aria-busy", kind === "loading");
}

async function apiFetch(path, init = {}) {
  const request = Object.assign({}, init, {
    headers: Api.headers(init.headers),
  });
  let res;
  try {
    res = await fetch(Api.url(path), request);
  } catch (error) {
    if (error instanceof TypeError) throw new APIError(path, 0);
    throw error;
  }
  if (!res.ok) throw new APIError(path, res.status);
  return res;
}

function downloadFilename(filename) {
  return String(filename || "download")
    .replace(/[\\/\\:*?"<>|]/g, "-")
    .trim() || "download";
}

async function downloadAPI(path, filename) {
  const res = await apiFetch(path);
  const objectURL = URL.createObjectURL(await res.blob());
  const link = document.createElement("a");
  link.href = objectURL;
  link.download = downloadFilename(filename);
  document.body.appendChild(link);
  try {
    link.click();
  } finally {
    link.remove();
    URL.revokeObjectURL(objectURL);
  }
}

const Api = {
  // Empty means same origin, which is what the browser wants. The desktop
  // client sets an absolute base such as "http://192.0.2.1:8787".
  base: "",

  // `token` remains the read credential and retains its legacy storage key so
  // existing browsers migrate without losing access. Admin writes use a
  // separate credential. localStorage rather than a cookie keeps both scoped
  // credentials browser-local and leaves the server stateless.
  token: "",
  adminToken: "",
  TOKEN_KEY: "omnitoken.token",
  ADMIN_TOKEN_KEY: "omnitoken.admin_token",

  loadToken() {
    try {
      this.token = localStorage.getItem(this.TOKEN_KEY) || "";
      const storedAdmin = localStorage.getItem(this.ADMIN_TOKEN_KEY);
      // Missing means an old single-token browser and falls back for migration.
      // An explicitly stored empty string means "no admin credential".
      this.adminToken = storedAdmin == null ? this.token : storedAdmin;
    } catch (e) {
      // Private mode or storage disabled; the panel still works against a
      // loopback server.
      this.token = "";
      this.adminToken = "";
    }
    return this.token;
  },

  saveTokens(readToken, adminToken) {
    this.token = readToken || "";
    this.adminToken = adminToken || "";
    try {
      if (this.token) localStorage.setItem(this.TOKEN_KEY, this.token);
      else localStorage.removeItem(this.TOKEN_KEY);
      // Store even the empty value: absence is reserved for legacy fallback.
      localStorage.setItem(this.ADMIN_TOKEN_KEY, this.adminToken);
      return true;
    } catch (e) {
      // Both in-memory credentials remain useful for this browser session even
      // when private mode or policy blocks persistence.
      return false;
    }
  },

  url(path) {
    return this.base + path;
  },

  headers(extra) {
    const h = new Headers(extra);
    if (this.token) h.set("Authorization", "Bearer " + this.token);
    return h;
  },

  async get(path) {
    const res = await apiFetch(path);
    // Every caller sits inside a try/catch that surfaces the message, so
    // failing loudly here beats letting res.json() throw a parse error on
    // whatever the server returned instead of JSON. 401 is named: "wrong
    // token" and "server not running" are different problems with different
    // fixes, and a bare status number does not tell them apart.
    return res.json();
  },

  // Returns the raw Response rather than parsed JSON: the settings page
  // distinguishes 401 from other failures and reads the body as text for its
  // error note.
  put(path, body) {
    const headers = new Headers({ "Content-Type": "application/json" });
    if (this.adminToken) {
      headers.set("Authorization", "Bearer " + this.adminToken);
    }
    return fetch(this.url(path), {
      method: "PUT",
      headers,
      body: JSON.stringify(body),
    });
  },

  // post is put's sibling for administrative actions that are not idempotent —
  // today the device identity merge (ADR-0019). It carries the admin credential
  // only, for the same reason: the read token buys queries, never a rewrite of
  // stored attribution. Like put it returns the raw Response so the caller can
  // tell 401 from 400 and show the server's message verbatim.
  post(path, body) {
    const headers = new Headers({ "Content-Type": "application/json" });
    if (this.adminToken) {
      headers.set("Authorization", "Bearer " + this.adminToken);
    }
    return fetch(this.url(path), {
      method: "POST",
      headers,
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
