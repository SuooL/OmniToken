"use strict";
const $ = (id) => document.getElementById(id);
let busy = false;

async function request(path, body) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 15000);
  try {
    const res = await fetch(path, {
      method: body === undefined ? "GET" : "POST",
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin", signal: controller.signal,
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || "验证失败,请重试");
    return data;
  } finally { clearTimeout(timeout); }
}

function message(text, error = false) {
  $("message").textContent = text;
  $("message").classList.toggle("error", error);
}

async function action(fn) {
  if (busy) return;
  busy = true;
  document.querySelectorAll("button").forEach((b) => { b.disabled = true; });
  message("");
  try { await fn(); }
  catch (e) {
    message(e.name === "NotAllowedError" ? "验证已取消或超时,可重新尝试。" : e.name === "AbortError" ? "连接超时,请重试。" : e.message || "操作失败,请重试。", true);
  } finally {
    busy = false;
    document.querySelectorAll("button").forEach((b) => { b.disabled = false; });
  }
}

function decode(value) {
  const raw = atob(value.replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}
function encode(value) {
  return btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
function optionsFromJSON(raw, registration) {
  const p = raw.publicKey;
  p.challenge = decode(p.challenge);
  if (registration) p.user.id = decode(p.user.id);
  for (const key of ["excludeCredentials", "allowCredentials"]) {
    if (p[key]) p[key] = p[key].map((c) => ({ ...c, id: decode(c.id) }));
  }
  return { publicKey: p };
}
function credentialJSON(c) {
  if (c.toJSON) return c.toJSON();
  const response = { clientDataJSON: encode(c.response.clientDataJSON) };
  for (const key of ["attestationObject", "authenticatorData", "signature", "userHandle"]) {
    if (c.response[key]) response[key] = encode(c.response[key]);
  }
  if (c.response.getTransports) response.transports = c.response.getTransports();
  return { id: c.id, rawId: encode(c.rawId), type: c.type, response,
    authenticatorAttachment: c.authenticatorAttachment, clientExtensionResults: c.getClientExtensionResults() };
}
function requirePasskeySupport() {
  if (!window.PublicKeyCredential || !navigator.credentials) throw new Error("当前浏览器不支持 Passkey,请使用更新的 Safari 或 Chrome。");
}

async function refresh() {
  const status = await request("/auth/status");
  $("signed-in").hidden = !status.authenticated;
  $("signed-out").hidden = status.authenticated;
  $("heading").textContent = status.authenticated ? "账户与 Passkey" : "登录你的面板";
  $("intro").textContent = status.authenticated
    ? status.passkeys ? "已登录。你可以添加其他设备,或直接进入面板。" : "添加 Passkey,下次用系统验证直接登录。"
    : "使用已添加的 Passkey,或输入一次性登录码。";
  $("key-count").textContent = `已添加 ${status.passkeys} 个 Passkey`;
  return status;
}

$("code-form").addEventListener("submit", (e) => {
  e.preventDefault();
  action(async () => {
    await request("/auth/code", { code: $("code").value.trim() });
    $("code").value = "";
    const s = await refresh();
    if (s.passkeys) location.assign("/");
    else message("登录成功。请添加 Passkey。");
  });
});
$("passkey-register").addEventListener("click", () => action(async () => {
  requirePasskeySupport();
  const raw = await request("/auth/passkey/register/begin", {});
  message("请在系统提示中验证身份…");
  const c = await navigator.credentials.create(optionsFromJSON(raw, true));
  await request("/auth/passkey/register/finish", credentialJSON(c));
  await refresh(); message("Passkey 已添加。下次可直接用 Passkey 登录。");
}));
$("passkey-login").addEventListener("click", () => action(async () => {
  requirePasskeySupport();
  const raw = await request("/auth/passkey/login/begin", {});
  message("请在系统提示中验证身份…");
  const c = await navigator.credentials.get(optionsFromJSON(raw, false));
  await request("/auth/passkey/login/finish", credentialJSON(c));
  location.assign("/");
}));
$("logout").addEventListener("click", () => action(async () => {
  await request("/auth/logout", {}); await refresh(); message("已退出登录。");
}));
refresh().catch((e) => message(e.message || "无法连接服务,请刷新重试。", true));
