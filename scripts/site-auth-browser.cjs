// Browser acceptance with an isolated Chromium profile and virtual platform
// authenticator. No production credentials or user browser profiles are used.
// NODE_PATH=/path/to/playwright/node_modules node scripts/site-auth-browser.cjs
const { chromium } = require("playwright");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const https = require("node:https");
const { spawn, execFileSync } = require("node:child_process");
const assert = require("node:assert/strict");

async function main() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "omnitoken-auth-qa-"));
  fs.chmodSync(dir, 0o700);
  const binary = path.resolve(process.env.OMNITOKEN_QA_BIN || "./omnitoken");
  const origin = "https://localhost:18443";
  const config = path.join(dir, "config.json");
  fs.writeFileSync(config, JSON.stringify({
    listen: "127.0.0.1:18787", db: path.join(dir, "usage.db"),
    state: path.join(dir, "state.json"), collect: { local: false },
    site_auth_origin: origin, read_token: "qa-read", admin_token: "qa-admin", token: "qa-ingest",
  }), { mode: 0o600 });
  execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", path.join(dir,"key.pem"), "-out", path.join(dir,"cert.pem"), "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost"], { stdio: "ignore" });
  const log = fs.openSync(path.join(dir,"hub.log"), "w", 0o600);
  const hub = spawn(binary, ["serve", "-config", config], { stdio: ["ignore",log,log] });
  const proxy = https.createServer({ key: fs.readFileSync(path.join(dir,"key.pem")), cert: fs.readFileSync(path.join(dir,"cert.pem")) }, (req,res) => {
    const upstream = http.request({ hostname:"127.0.0.1", port:18787, path:req.url, method:req.method, headers:req.headers }, (r) => {res.writeHead(r.statusCode,r.headers);r.pipe(res);});
    upstream.on("error", () => {res.writeHead(502);res.end();});
    req.pipe(upstream);res.on("close", () => upstream.destroy());
  });
  let browser;
  try {
    await new Promise((resolve,reject) => {proxy.once("error",reject);proxy.listen(18443,"127.0.0.1",resolve);});
    const deadline = Date.now()+15000;
    while (true) {
      const ready = await new Promise(resolve => {
        const req=http.get("http://127.0.0.1:18787/api/v1/health",r=>{r.resume();resolve(r.statusCode===200);});
        req.on("error",()=>resolve(false));req.setTimeout(1000,()=>{req.destroy();resolve(false);});
      });
      if (ready) break;
      if (hub.exitCode!==null || Date.now()>deadline) throw new Error("QA Hub did not become healthy: "+fs.readFileSync(path.join(dir,"hub.log"),"utf8"));
      await new Promise(resolve=>setTimeout(resolve,100));
    }
    browser = await chromium.launch({ channel:"chrome", headless:true });
    const context = await browser.newContext({ ignoreHTTPSErrors:true, viewport:{width:1280,height:900} });
    const page = await context.newPage();
    const cdp = await context.newCDPSession(page);
    await cdp.send("WebAuthn.enable");
    await cdp.send("WebAuthn.addVirtualAuthenticator", {options:{protocol:"ctap2",transport:"internal",hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.goto(origin+"/login");
    await page.locator("#signed-out").waitFor({state:"visible"});
    await page.screenshot({path:path.join(dir,"login-desktop.png")});
    assert.equal((await context.request.get(origin+"/api/v1/live")).status(),401);
    await page.locator("#code").fill("INVALID");await page.locator("#code-login").click();
    await page.locator("#message.error").waitFor();
    const code=execFileSync(binary,["auth-code","-config",config],{encoding:"utf8"}).trim();
    await page.locator("#code").fill(code);await page.locator("#code-login").click();
    await page.locator("#signed-in").waitFor({state:"visible"});
    assert.equal((await context.request.post(origin+"/auth/code",{headers:{Origin:origin},data:{code}})).status(),401);
    await page.locator("#passkey-register").click();
    await page.waitForFunction(() => document.getElementById("key-count").textContent.includes("1 个"));
    await page.screenshot({path:path.join(dir,"passkey-registered.png")});
    await page.locator("a.panel-link").click();
    await page.locator("#site-account").waitFor();
    assert.equal((await context.request.get(origin+"/api/v1/live")).status(),200);
    // Browser sessions cannot write pricing or enroll/revoke devices.
    assert.equal((await context.request.put(origin+"/api/v1/settings",{data:{}})).status(),401);
    await page.screenshot({path:path.join(dir,"dashboard.png")});
    await page.locator("#site-account").click();await page.locator("#logout").click();
    await page.locator("#signed-out").waitFor({state:"visible"});
    assert.equal((await context.request.get(origin+"/api/v1/live")).status(),401);
    // A real assertion with a forged signature must fail before issuing a session.
    await page.route("**/auth/passkey/login/finish", async route => {
      const body = route.request().postDataJSON();
      body.response.signature = "AAAA";
      await route.continue({postData:JSON.stringify(body)});
    }, {times:1});
    await page.locator("#passkey-login").click();
    await page.locator("#message.error").waitFor();
    assert.equal((await context.request.get(origin+"/api/v1/live")).status(),401);
    await page.locator("#passkey-login").click();
    await page.waitForURL(origin+"/");await page.locator("#site-account").waitFor();
    assert.equal((await context.request.get(origin+"/api/v1/live")).status(),200);
    await page.goto(origin+"/login");await page.locator("#logout").click();
    await page.locator("#signed-out").waitFor({state:"visible"});
    await page.setViewportSize({width:390,height:844});
    await page.screenshot({path:path.join(dir,"login-mobile.png")});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth>innerWidth),false);
    assert.deepEqual(errors,[]);
    console.log("PASS: code login/replay, platform Passkey registration, logout, discoverable Passkey login, session API scopes, desktop/mobile UI");
    console.log("QA artifacts:",dir);
  } finally {
    if (browser) await browser.close();
    proxy.closeAllConnections();await new Promise(resolve=>proxy.close(resolve));
    hub.kill("SIGTERM");fs.closeSync(log);
  }
}
main().catch(e=>{console.error(e);process.exitCode=1;});
