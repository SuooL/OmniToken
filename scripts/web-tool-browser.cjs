#!/usr/bin/env node
"use strict";
// Real-browser acceptance against an isolated actual Hub. Never uses production
// configuration, credentials or metadata. Build the binary before running:
// go build -o /tmp/omnitoken-web-qa ./cmd/omnitoken
// OMNITOKEN_QA_BIN=/tmp/omnitoken-web-qa node scripts/web-tool-browser.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const net = require("node:net");
const { spawn } = require("node:child_process");
function playwright() {
  return require(process.env.OMNITOKEN_QA_PLAYWRIGHT || "playwright");
}
async function unusedPort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}
(async () => {
  const dir = fs.mkdtempSync(
    path.join(process.env.OMNITOKEN_QA_ARTIFACT_ROOT || os.tmpdir(), "omnitoken-web-tool-browser-"),
  );
  console.log(`Browser acceptance artifacts: ${dir}`);
  const binary =
    process.env.OMNITOKEN_QA_BIN || path.resolve(__dirname, "../omnitoken");
  assert.ok(
    fs.existsSync(binary),
    `Build a Hub binary and set OMNITOKEN_QA_BIN: ${binary}`,
  );
  const port = await unusedPort();
  const base = `http://127.0.0.1:${port}`;
  const configFile = path.join(dir, "config.json");
  fs.writeFileSync(
    configFile,
    JSON.stringify({
      listen: `127.0.0.1:${port}`,
      db: path.join(dir, "usage.db"),
      state: path.join(dir, "state.json"),
      collect: { local: false },
      pricing_auto_update: false,
      timezone: "UTC",
    }),
  );
  const log = fs.openSync(path.join(dir, "server.log"), "w");
  const hub = spawn(binary, ["serve", "-config", configFile], {
    stdio: ["ignore", log, log],
  });
  fs.closeSync(log);
  let browser;
  const errors = [],
    records = [],
    checks = [];
  const check = (name) => {
    checks.push(name);
    console.log(`PASS ${name}`);
  };
  try {
    browser = await playwright().chromium.launch({
      channel: process.env.OMNITOKEN_QA_BROWSER || "chrome",
      headless: true,
    });
    const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
      colorScheme: "dark",
      reducedMotion: "reduce",
      acceptDownloads: true,
    });
    const page = await context.newPage();
    page.on("pageerror", (error) => errors.push(error.message));
    // Bounded readiness polling uses the Hub's health endpoint.
    let ready = false;
    for (let attempt = 0; attempt < 100; attempt++) {
      if (hub.exitCode != null)
        throw Error(`Hub exited ${hub.exitCode}; inspect ${dir}/server.log`);
      try {
        ready = (
          await context.request.get(base + "/api/v1/health", { timeout: 500 })
        ).ok();
      } catch {}
      if (ready) break;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    assert.ok(ready, "isolated Hub became healthy");
    const now = Date.now(),
      events = [];
    for (let day = 0; day < 30; day++)
      for (let i = 0; i < 30; i++)
        for (const [source, model, provider, device] of [
          ["claude-code", "claude-sonnet-4-5", "anthropic-oauth", "Studio"],
          ["codex", "gpt-5", "openai-codex", "Workstation"],
          ["dsh", "deepseek-chat", "deepseek", "Lab"],
        ])
          events.push({
            event_id: `qa-${day}-${i}-${source}`,
            ts: now - day * 86400000 - i * 60000,
            device,
            source,
            model,
            provider,
            input_tokens: 4000 + ((i * 173) % 3000),
            output_tokens: 500 + ((i * 71) % 1500),
            cache_read_tokens: 8000,
            gen_ms: source === "codex" ? 0 : 12000,
            duration_ms: 30000,
            session_id: `qa-${source}-${day}`,
            repo: "example/research",
            cwd: "/synthetic/research",
          });
    const seeded = await context.request.post(base + "/api/v1/ingest", {
      data: {
        events,
        quotas: [
          {
            device: "Studio",
            source: "claude-code",
            limit_id: "qa-five",
            scope: "primary",
            window_minutes: 300,
            used_percent: 42,
            resets_at: now + 5400000,
            observed_at: now,
          },
          {
            device: "Studio",
            source: "claude-code",
            limit_id: "qa-week",
            scope: "secondary",
            window_minutes: 10080,
            used_percent: 91,
            resets_at: now + 250000000,
            observed_at: now,
          },
        ],
      },
    });
    assert.ok(seeded.ok(), await seeded.text());
    const routes = [
      "overview",
      "live",
      "speed",
      "reports",
      "details",
      "devices",
      "models",
      "cache",
      "settings",
    ];
    async function route(name) {
      await page.evaluate((name) => {
        location.hash = name;
      }, name);
      await page.locator("#view-" + name).waitFor({ state: "visible" });
      await page.waitForFunction((name) => {
        const el = document.getElementById("view-" + name);
        if (el.getAttribute("aria-busy") === "true") return false;
        return {
          overview: () => Overview.lastData,
          live: () => Live.data,
          speed: () => SpeedView.lastData,
          reports: () => Reports.lastData,
          details: () => document.querySelector("#d-table tbody tr"),
          devices: () => DevicesView._displayData,
          models: () => ModelsView.lastData,
          cache: () => CacheView.lastData,
          settings: () => SettingsView._loaded,
        }[name]();
      }, name);
      await page.evaluate(
        () =>
          new Promise((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(resolve)),
          ),
      );
    }
    await page.goto(base);
    for (const width of [420, 768, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      for (const view of routes) {
        await route(view);
        const record = await page.evaluate(
          ({ view, width }) => ({
            view,
            width,
            overflow: document.documentElement.scrollWidth > innerWidth + 1,
            heading: document.getElementById("page-title").textContent,
          }),
          { view, width },
        );
        assert.equal(
          record.overflow,
          false,
          `${view} at ${width}px has no page horizontal overflow`,
        );
        assert.ok(record.heading, `${view} exposes a page name`);
        records.push(record);
        await page.screenshot({
          path: path.join(dir, `${view}-${width}.png`),
          fullPage: true,
        });
      }
      check(`nine routes render at ${width}px without horizontal overflow`);
    }
    assert.deepEqual(
      errors,
      [],
      "no browser runtime errors during route matrix",
    );
    await route("overview");
    // Native disclosure must resize a real ECharts instance and survive polling.
    assert.equal(
      await page.locator("#overview-usage-analysis").evaluate((el) => el.open),
      false,
    );
    await page.locator("#overview-usage-analysis > summary").click();
    await page.waitForFunction(() => {
      const el = document.getElementById("overview-model-composition"),
        chart = ChartRegistry._instances.get(el);
      return chart && chart.getWidth() > 0 && chart.getHeight() > 0;
    });
    await page.evaluate(() => {
      globalThis.qaChartRef = ChartRegistry._instances.get(
        document.getElementById("overview-model-composition"),
      );
    });
    await page.evaluate(async () => {
      await Overview.load();
    });
    assert.equal(
      await page.locator("#overview-usage-analysis").evaluate((el) => el.open),
      true,
    );
    assert.equal(
      await page.evaluate(
        () =>
          qaChartRef ===
          ChartRegistry._instances.get(
            document.getElementById("overview-model-composition"),
          ),
      ),
      true,
    );
    assert.equal(
      await page
        .locator("#overview-usage-analysis > summary")
        .evaluate((el) => el === document.activeElement),
      true,
    );
    check(
      "analysis disclosure starts closed, reveals nonzero chart and survives refresh",
    );
    // Real source/event/device drawers: refresh while open, then Escape to the
    // current replacement row. This catches detached trigger focus regressions.
    async function drawer(name, selector, refresh) {
      await route(name);
      const row = page.locator(selector).first();
      const attr = await row.evaluate((el) =>
        ["data-focus-key", "data-event-key", "data-device-key"].find((key) =>
          el.hasAttribute(key),
        ),
      );
      const value = await row.getAttribute(attr);
      await row.focus();
      await row.press("Enter");
      await page.locator("#tool-detail-drawer").waitFor({ state: "visible" });
      assert.ok((await page.locator(".drawer-content").innerText()).length > 0);
      await page.screenshot({
        path: path.join(dir, `${name}-drawer.png`),
        fullPage: true,
      });
      await page.evaluate(refresh);
      assert.equal(
        await page.locator("#tool-detail-drawer").evaluate((el) => el.open),
        true,
      );
      await page.keyboard.press("Escape");
      assert.equal(
        await page.locator("#tool-detail-drawer").evaluate((el) => el.open),
        false,
      );
      assert.equal(
        await page.evaluate(
          ({ attr, value }) =>
            document.activeElement?.getAttribute(attr) === value,
          { attr, value },
        ),
        true,
        `${name} drawer restores row focus after refresh`,
      );
      check(`${name} drawer refresh, Escape and focus restore`);
    }
    await drawer("overview", "button[data-source-detail]", async () => {
      await Overview.load();
    });
    await drawer("details", "[data-event-key]", async () => {
      await Details.load();
    });
    await drawer("devices", "[data-device-key]", async () => {
      await DevicesView.load();
    });
    await route("details");
    await page.selectOption("#d-f-days", "30");
    await page.waitForFunction(
      () => Details.filters.days === "30" && Details.total > 100,
    );
    await page.selectOption("#d-f-source", "codex");
    await page.waitForFunction(
      () =>
        Details.filters.source === "codex" &&
        Details.offset === 0 &&
        Details.total > 100,
    );
    await page.locator("#d-next").click();
    await page.waitForFunction(
      () =>
        location.hash.includes("offset=100") &&
        Details.offset === 100 &&
        !document.getElementById("d-next").disabled,
    );
    const paged = await page.evaluate(() => location.hash);
    await page.selectOption("#d-f-device", "Workstation");
    await page.waitForFunction(
      () => Details.filters.device === "Workstation" && Details.offset === 0,
    );
    await page.goBack();
    await page.waitForFunction(
      (hash) =>
        location.hash === hash &&
        Details.offset === 100 &&
        document.getElementById("d-f-source").value === "codex",
      paged,
    );
    check(
      "detail filters share URL, reset paging and browser Back restores context",
    );
    await route("settings");
    const token = page.locator("#settings-panel-connection input").first();
    await token.fill("synthetic-unsaved-draft");
    await page.locator('[data-settings-nav="pricing"]').click();
    await page.locator('[data-settings-nav="connection"]').click();
    assert.equal(await token.inputValue(), "synthetic-unsaved-draft");
    await page.locator('[data-settings-nav="devices"]').click();
    assert.equal(
      await page.locator("#settings-device-advanced").evaluate((el) => el.open),
      false,
    );
    check(
      "settings groups preserve unsaved drafts; dangerous operations start collapsed",
    );
    await route("reports");
    await page.locator('#reports-range [data-days="7"]').click();
    await page.locator('#reports-gran [data-gran="weekly"]').click();
    await page.waitForFunction(
      () => Reports.days === 7 && Reports.granularity === "weekly",
    );
    const request = page.waitForRequest(
      (req) =>
        req.url().includes("/api/v1/reports?") &&
        req.url().includes("format=csv"),
    );
    const download = page.waitForEvent("download");
    await page.locator('#reports-export [data-format="csv"]').click();
    const exportURL = new URL((await request).url());
    assert.equal(exportURL.searchParams.get("days"), "7");
    assert.equal(exportURL.searchParams.get("granularity"), "weekly");
    await (await download).saveAs(path.join(dir, "selected-weekly-7d.csv"));
    check("report CSV export uses selected range and granularity");
    // Pure quota rendering runs in the actual browser using the shipped helper.
    const quota = await page.evaluate(() => {
      const windows = [
        {
          source: "claude-code",
          kind: "subscription",
          window_minutes: 300,
          authoritative: true,
          used_percent: 0,
          remaining_tokens: 0,
          capacity_tokens: 1000,
        },
      ];
      const rows = QuotaUI.normalize(windows, [
        {
          source: "claude-code",
          window_minutes: 10080,
          scope: "seven_day",
          used_percent: 105,
        },
      ]);
      const host = document.createElement("div");
      host.innerHTML = QuotaUI.render(windows, [
        {
          source: "claude-code",
          window_minutes: 10080,
          scope: "seven_day",
          used_percent: 105,
        },
      ]);
      const unknown = QuotaUI.normalize(
        [
          {
            source: "codex",
            kind: "subscription",
            window_minutes: 300,
            placeholder: true,
          },
        ],
        [],
      );
      const full = QuotaUI.normalize(
        [],
        [{ source: "codex", window_minutes: 300, used_percent: 100 }],
      );
      return {
        zero: rows[0].value,
        remaining: rows[0].remaining,
        full: full[0].value,
        fullRemaining: full[0].remaining,
        over: rows[1].value,
        risk: host.querySelector('[data-risk="critical"]') != null,
        unknown: unknown[0].value,
        metered: QuotaUI.render(
          [{ kind: "relay", tokens: 100, cost_usd: 1 }],
          [],
        ).includes('role="progressbar"'),
      };
    });
    assert.deepEqual(quota, {
      zero: 0,
      remaining: 0,
      full: 100,
      fullRemaining: 0,
      over: 105,
      risk: true,
      unknown: null,
      metered: false,
    });
    check(
      "quota true zero, zero remaining, full, over-limit, missing and metered domains",
    );
    await route("overview");
    await page.route("**/api/v1/live", (route) => route.abort("failed"));
    await page.evaluate(async () => {
      LiveSnapshotCache.receivedAt = 0;
      await Overview.load();
    });
    assert.match(
      await page.locator("#overview-quotas").innerText(),
      /上次成功/,
    );
    assert.match(await page.locator("#refresh-note").innerText(), /更新失败/);
    await page.unroute("**/api/v1/live");
    check("failed live GET preserves explicitly stale last-good quota");
    for (const status of [401, 403]) {
      await page.route("**/api/v1/overview?*", (route) =>
        route.fulfill({
          status,
          contentType: "application/json",
          body: '{"error":"synthetic denied"}',
        }),
      );
      await page.evaluate(async () => {
        await Overview.load();
      });
      const state = await page
        .locator("#view-overview .view-state")
        .innerText();
      assert.match(state, /授权|权限|拒绝|过期|令牌/);
      assert.equal(
        await page.locator("#view-overview .view-state-unauthorized").count(),
        1,
      );
      assert.equal(
        await page.locator("#overview-today-total").count(),
        0,
        "denied reads clear prior private usage",
      );
      await page.unroute("**/api/v1/overview?*");
      await page.evaluate(async () => {
        await Overview.load();
      });
    }
    check(
      "401 and 403 controlled response states render without runtime errors",
    );
    await page.setViewportSize({ width: 420, height: 800 });
    await route("overview");
    const original = await page.evaluate(() => Overview.lastData);
    await page.evaluate(() => {
      const payload = structuredClone(Overview.lastData);
      const row = payload.telemetry.today.sources[0];
      row.models = Array.from({ length: 25 }, (_, i) => ({
        model:
          i === 3
            ? "long-model-name-with-many-characters-to-verify-wrapping-and-token-alignment"
            : `qa-model-${i}`,
        tokens: 1,
      }));
      Overview.render(payload);
    });
    await page.locator("button[data-source-detail]").first().click();
    assert.equal(await page.locator(".drawer-content tbody tr").count(), 25);
    assert.equal(
      await page
        .locator(".drawer-content")
        .evaluate((el) => el.scrollHeight > el.clientHeight),
      true,
    );
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth + 1,
      ),
      false,
    );
    await page.keyboard.press("Escape");
    await page.evaluate((payload) => Overview.render(payload), original);
    check(
      "narrow drawer supports many long model names with contained scrolling",
    );
    assert.deepEqual(
      errors,
      [],
      "all browser interactions complete without pageerror",
    );
    fs.writeFileSync(
      path.join(dir, "acceptance.json"),
      JSON.stringify(
        {
          fixture:
            "2700 synthetic metadata events; actual local isolated Hub; no production data",
          records,
          checks,
          errors,
        },
        null,
        2,
      ),
    );
    console.log(`PASS ${checks.length} acceptance groups; artifacts: ${dir}`);
  } finally {
    if (browser) await browser.close();
    hub.kill("SIGTERM");
    fs.writeFileSync(
      path.join(dir, "run-summary.json"),
      JSON.stringify({ checks, errors, records }, null, 2),
    );
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
