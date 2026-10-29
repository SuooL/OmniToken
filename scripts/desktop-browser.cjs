// Production desktop markup and behavior, with deterministic Tauri IPC only.
// NODE_PATH=/path/to/playwright/node_modules node scripts/desktop-browser.cjs
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const os = require("node:os");
const root = path.resolve(__dirname, "../desktop/ui");
const now = Date.now();
const telemetry = {
  timezone: "UTC",
  generated_at_ms: now,
  today: {
    total_tokens: 24800000,
    sources: [
      {
        source: "claude-code",
        tokens: 12600000,
        previous_tokens: 10000000,
        change_percent: 26,
        models: [
          { model: "Claude Opus", tokens: 8200000 },
          { model: "Claude Sonnet", tokens: 4400000 },
        ],
      },
      {
        source: "codex",
        tokens: 9800000,
        previous_tokens: 0,
        models: [{ model: "GPT-5", tokens: 9800000 }],
      },
      {
        source: "dsh",
        tokens: 2400000,
        models: [{ model: "DeepSeek V3", tokens: 2400000 }],
      },
      { source: "api", tokens: 3, models: [{ model: "<unsafe>", tokens: 3 }] },
    ],
  },
  speed: {
    peak_tps: 183,
    active_ratio: 0.64,
    measured_sources: ["claude-code", "dsh"],
    unmeasured_sources: ["codex"],
    coverage: [
      { source: "claude-code", total_events: 100, measured_events: 100 },
    ],
    series: Array.from({ length: 60 }, (_, i) => ({
      start_ms: now - (60 - i) * 60000,
      aggregate_tps: (i * 23) % 183,
      sources: [
        { source: "claude-code", contribution_tps: (i * 17) % 150 },
        { source: "dsh", contribution_tps: i % 30 },
      ],
    })),
  },
};
const live = {
  connection: { kind: "live", generated_at_ms: now },
  activity: {
    kind: "active",
    rate: 128.4,
    session_count: 3,
    contributing_devices: 2,
  },
  quotas: [
    {
      source: "claude-code",
      label: "Claude",
      five_hour_percent: 42,
      weekly_percent: 81,
      five_hour_remaining_tokens: 5200000,
      weekly_remaining_tokens: 12400000,
      five_hour_resets_at_ms: now + 5460000,
      weekly_resets_at_ms: now + 259500000,
    },
    {
      source: "codex",
      label: "Codex",
      five_hour_percent: 123,
      weekly_percent: null,
      five_hour_remaining_tokens: 999,
      weekly_remaining_tokens: null,
    },
  ],
  sessions: [
    {
      tool: "Claude",
      repository: "OmniToken",
      model: "Opus",
      device: "Mac Studio",
      contribution_rate: 128.4,
      native_rate: 128.4,
    },
  ],
  devices: [
    { name: "Mac Studio", state: "active", has_procs: true, running: 3 },
    { name: "Research Node", state: "stale", has_procs: false, running: 0 },
  ],
  device_online: 1,
  device_total: 2,
  device_summary: "1/2 在线",
};
(async () => {
  const server = http.createServer((req, res) => {
    const file = path.join(root, new URL(req.url, "http://local").pathname);
    if (!file.startsWith(root + path.sep)) {
      res.writeHead(403).end();
      return;
    }
    try {
      const type =
        {
          ".js": "application/javascript",
          ".css": "text/css",
          ".svg": "image/svg+xml",
          ".html": "text/html",
        }[path.extname(file)] || "text/plain";
      res.setHeader("Content-Type", type);
      res.end(fs.readFileSync(file));
    } catch {
      res.writeHead(404).end();
    }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const artifact = fs.mkdtempSync(
    path.join(os.tmpdir(), "omnitoken-desktop-browser-"),
  );
  try {
    const context = await browser.newContext({
      viewport: { width: 420, height: 760 },
    });
    const errors = [];
    context.on("page", (p) => p.on("pageerror", (e) => errors.push(e.message)));
    let detail;
    await context.exposeBinding("__sendDetail", async (_, payload) => {
      if (detail)
        await detail.evaluate(
          (p) => window.fixture.emit("detail-view", p),
          payload,
        );
    });
    await context.addInitScript(
      ({ telemetry }) => {
        const listeners = {};
        window.fixture = {
          listeners,
          telemetry,
          calls: [],
          native: true,
          emit: (event, payload) => listeners[event]?.({ payload }),
        };
        window.__TAURI__ = {
          core: {
            invoke: async (command, args) => {
              window.fixture.calls.push({ command, args });
              if (command === "settings_get")
                return {
                  server: "http://127.0.0.1:8787",
                  has_token: false,
                  panel_url: "",
                  autostart: false,
                };
              if (command === "telemetry_get") return window.fixture.telemetry;
              if (command === "details_set") {
                if (window.fixture.native)
                  await window.__sendDetail(args.payload);
                return window.fixture.native;
              }
              if (command === "details_get") return null;
              if (command === "details_close") return;
              if (command === "settings_set")
                return {
                  server: args.server,
                  has_token: !!args.token,
                  panel_url: args.panelUrl,
                  autostart: args.autostart,
                };
              if (command === "check_update")
                return {
                  version: "0.3.0",
                  message: "已是最新版本",
                  status: "current",
                };
            },
          },
          event: {
            listen: async (name, fn) => {
              listeners[name] = fn;
            },
          },
          window: {
            currentMonitor: async () => ({
              workArea: { size: { width: 1440, height: 900 } },
              scaleFactor: 1,
            }),
            getCurrentWindow: () => ({ setSize: async () => {} }),
          },
          dpi: {
            LogicalSize: class {
              constructor(width, height) {
                this.width = width;
                this.height = height;
              }
            },
          },
        };
      },
      { telemetry },
    );
    const main = await context.newPage();
    detail = await context.newPage();
    await detail.setViewportSize({ width: 320, height: 760 });
    await detail.goto(base + "/index.html?details=1");
    await main.goto(base + "/index.html");
    await main.waitForFunction(() => window.fixture.listeners.live);
    await main.evaluate(
      (live) => window.fixture.emit("live", { view: live }),
      live,
    );
    await main.locator('[data-detail="source:claude-code"]').click();
    await detail.locator("#source-detail").getByText("Claude Opus").waitFor();
    assert.equal(await main.locator("#detail-panel").isVisible(), false);
    assert.equal(
      await main
        .locator('[data-detail="source:claude-code"]')
        .getAttribute("aria-expanded"),
      "true",
    );
    const mainBox = await main.locator(".panel").boundingBox();
    const normalHeight = await detail.evaluate(
      () =>
        window.fixture.calls
          .filter((c) => c.command === "details_resize")
          .at(-1).args.height,
    );
    await main.locator('[data-detail="source:dsh"]').click();
    await detail.locator("#source-detail").getByText("DeepSeek V3").waitFor();
    const smallHeight = await detail.evaluate(
      () =>
        window.fixture.calls
          .filter((c) => c.command === "details_resize")
          .at(-1).args.height,
    );
    assert.ok(
      smallHeight < normalHeight,
      "One model must shrink the detail window",
    );
    // Trigger the real telemetry handler using its regular bridge pull.
    await main.evaluate((t) => onTelemetry(t), {
      ...telemetry,
      today: {
        ...telemetry.today,
        sources: telemetry.today.sources.map((s) =>
          s.source === "dsh"
            ? {
                ...s,
                models: Array.from({ length: 25 }, (_, i) => ({
                  model: `long-model-${i}`,
                  tokens: 96000,
                })),
              }
            : s,
        ),
      },
    });
    await detail.locator(".model-row").nth(24).waitFor();
    const longHeight = await detail.evaluate(
      () =>
        window.fixture.calls
          .filter((c) => c.command === "details_resize")
          .at(-1).args.height,
    );
    assert.ok(
      longHeight > smallHeight + 400,
      "Many models must request natural height",
    );
    assert.deepEqual(await main.locator(".panel").boundingBox(), mainBox);
    assert.equal(await main.locator('[role="progressbar"]').count(), 3);
    assert.equal(
      await main
        .locator('[aria-valuenow="100"]')
        .getAttribute("aria-valuetext"),
      "123% 已满",
    );
    assert.equal(await main.locator('[data-detail^="quota"]').count(), 0);
    await main.locator('[data-detail="speed"]').click();
    await detail.locator("#speed-detail").waitFor({ state: "visible" });
    await main.locator('[data-detail="devices"]').click();
    await detail.locator("#devices-detail").waitFor({ state: "visible" });
    assert.equal(await detail.locator(".device-row").count(), 2);
    await main.locator('[data-detail="source:api"]').click();
    await detail.getByText("<unsafe>", { exact: true }).waitFor();
    assert.equal(await detail.locator("unsafe").count(), 0);
    await detail.screenshot({ path: path.join(artifact, "detail.png") });
    await main.screenshot({ path: path.join(artifact, "main.png") });
    await main.evaluate(() => window.fixture.emit("detail-closed", null));
    await main.locator("#open-settings").click();
    await main.locator("#check-update").click();
    await main.getByText("已是最新版本").waitFor();
    await main.locator("#settings-cancel").click();
    await main.evaluate(() => {
      window.fixture.native = false;
    });
    await main.locator('[data-detail="source:codex"]').click();
    await main.locator("#source-detail").getByText("GPT-5").waitFor();
    assert.equal(await main.locator("#main").isVisible(), false);
    await main.keyboard.press("Escape");
    assert.equal(await main.locator("#main").isVisible(), true);
    for (const width of [320, 420]) {
      await main.setViewportSize({ width, height: 680 });
      assert.equal(
        await main
          .locator("#main")
          .evaluate((el) => el.scrollWidth > el.clientWidth + 1),
        false,
        `Main overflows at ${width}`,
      );
      await main.locator('[data-detail="source:dsh"]').click();
      assert.equal(
        await main
          .locator("#detail-scroll")
          .evaluate((el) => el.scrollWidth > el.clientWidth + 1),
        false,
      );
      await main.locator("#detail-close").click();
    }
    assert.deepEqual(errors, []);
    console.log(
      `PASS: production UI disclosure, IPC, natural height, independent quota meters, XSS escaping, native/inline modes, settings, keyboard, 320/420 layout. Screenshots: ${artifact}`,
    );
  } finally {
    await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
