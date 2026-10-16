// Real Usage/Sessions renderers, isolated API fixtures, both engines and all
// five languages. No native window, live gateway, account or user config.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");
const assets = process.env.MAGPIE_TIER_ASSETS || path.resolve(__dirname, "../assets");
const now = Date.now();
const rows = [
  { service_tier: "priority", tier_asked: "priority", tier_sent: "priority", tier_served: "default", cost: .004, priced: true },
  { service_tier: "default", tier_asked: "default", tier_sent: "default", tier_served: "default", cost: .002, priced: true },
  { service_tier: "", cost: .002, priced: true, source: "log" },
  { service_tier: "ultrafast", tier_asked: "ultrafast", tier_sent: "ultrafast", cost: .012, priced: true },
  { service_tier: "turbo", tier_asked: "turbo", cost: 0, priced: false },
].map((r, i) => ({ t: new Date(now - i * 60000).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "codex", providerName: "Codex", model: "gpt-6.1-sol", req: "gpt-6.1-sol", in: 1000, out: 0, ms: 1000, status: 200, rid: "tier-" + i, ...r }));

const words = {
  en: ["Fast", "Default", "Unknown tier", "Tier requested", "Tier sent", "Tier returned by upstream", "Cost basis"],
  zh: ["快速", "默认", "档位未知", "请求档位", "发出档位", "上游回显档位", "费用依据"],
  "zh-TW": ["快速", "預設", "檔位未知", "請求檔位", "送出檔位", "上游回傳檔位", "費用依據"],
  ja: ["高速", "デフォルト", "階層不明", "要求した階層", "送信した階層", "上流が返した階層", "費用の根拠"],
  de: ["Schnell", "Standard", "Unbekannte Stufe", "Angeforderte Stufe", "Gesendete Stufe", "Vom Anbieter gemeldete Stufe", "Kostengrundlage"],
};

function serve(lang, requests) {
  return async (route) => {
    const u = new URL(route.request().url()), json = (v) => route.fulfill({ json: v });
    if (u.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (u.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (u.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (u.pathname === "/api/usage/requests") {
      requests.push(u.searchParams.get("tier") || "");
      const tier = u.searchParams.get("tier");
      const selected = !tier ? rows : rows.filter((r) => (r.service_tier || "unknown") === tier);
      return json({ period: "today", rows: selected, offset: 0, total: selected.length, calls: selected.length, errors: 0,
        input: selected.length * 1000, output: 0, cache_read: 0, cache_write: 0, reasoning: 0,
        cost: selected.reduce((s, r) => s + r.cost, 0), unpriced: selected.filter((r) => !r.priced).length,
        agents: [{ id: "codex", name: "Codex", icon: "codex-color" }] });
    }
    if (u.pathname === "/api/usage/quotas") return json([]);
    if (u.pathname === "/api/usage/requests/content") return json({ found: false, why: "session" });
    if (u.pathname === "/api/usage") return json({ calls: 5, errors: 0, input: 5000, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: .02, unpriced: 1, series: [], agents: [], models: [] });
    if (u.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (u.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (u.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (u.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, u.pathname === "/" ? "index.html" : u.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: ({ ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" })[path.extname(file)] });
  };
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  test(engine + ": service tiers, cost evidence and filters", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      await t.test(lang, async () => {
        const context = await browser.newContext({ viewport: { width: 1180, height: 780 }, reducedMotion: "reduce" });
        t.after(() => context.close());
        const p = await context.newPage(), errors = [], requests = [];
        p.setDefaultTimeout(7000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", serve(lang, requests));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator(".led-row").first().waitFor();
        const w = words[lang];
        assert.deepEqual((await p.locator("[data-service-tier]").allTextContents()).slice(0, 3), w.slice(0, 3));
        const first = p.locator(".led-row").first();
        await reader.click(p, first.locator(".model").first());
        const detail = p.locator(".led-detail").first();
        const values = await detail.locator("dt").evaluateAll((nodes) => Object.fromEntries(nodes.map((n) => [n.textContent, n.nextElementSibling.textContent])));
        assert.equal(values[w[3]], "priority");
        assert.equal(values[w[4]], "priority");
        assert.equal(values[w[5]], "default");
        assert(values[w[6]].includes(w[0]), "cost basis names Fast without rewriting the default reply");

        // The shared Sessions renderer separates the same model/effort by
        // tier, instead of drawing the old one-price model aggregate.
        const sessionText = await p.evaluate(() => {
          const d = document.createElement("div");
          sessUsageDetail(d, { agent: "codex", models: [{ model: "old aggregate", input: 2000, output: 0, cost: .004, priced: true }], model_effort: [
            { model: "gpt-6.1-sol", effort: "high", service_tier: "default", input: 1000, output: 0, cost: .002, priced: true },
            { model: "gpt-6.1-sol", effort: "high", service_tier: "priority", input: 1000, output: 0, cost: .004, priced: true },
          ] }, () => {});
          return d.textContent;
        });
        assert(sessionText.includes(w[0]) && sessionText.includes(w[1]) && !sessionText.includes("old aggregate"));

        for (const width of [1180, 420]) {
          await p.setViewportSize({ width, height: 780 });
          for (const tier of ["priority", "default", "unknown"]) {
            await reader.click(p, p.locator("#ledTier"));
            await reader.click(p, p.locator(`.sess-menu .pm-item[title="${tier}"]`));
            await p.waitForFunction((wanted) => document.querySelectorAll(".led-row").length === 1 && document.querySelector("[data-service-tier]")?.dataset.serviceTier === wanted, tier);
            assert.equal(requests.at(-1), tier);
          }
          assert.equal(await p.locator("select").count(), 0, "uses Magpie's menu");
          assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "no page overflow at " + width);
          if (process.env.ARTIFACT_DIR && lang === "zh") {
            await reader.click(p, p.locator("#ledTier"));
            await reader.click(p, p.locator('.sess-menu .pm-item[title="priority"]'));
            await p.waitForFunction(() => document.querySelectorAll(".led-row").length === 1 && document.querySelector('[data-service-tier="priority"]'));
            if (await p.locator(".led-row").getAttribute("aria-expanded") !== "true") await reader.click(p, p.locator(".led-row"));
            await reader.inView(p, p.locator(".led-detail"));
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `service-tier-${engine}-${width}.png`), fullPage: true });
            if (width === 1180) await p.locator("#ledWrap").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `service-tier-${engine}-table.png`) });
          }
        }
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
