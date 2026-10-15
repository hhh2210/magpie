// #840: reset deadlines are text; a discoverable, keyboard-reachable action
// refreshes only its account and distinguishes a new read, cache and failure.
// Real page, isolated responses: no vendor, account or installed app is used.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.MAGPIE_QUOTA_ASSETS || path.resolve(__dirname, "../assets");
const iso = n => new Date(n).toISOString();
const words = {
  en: { reset: "Resets", busy: "Reading…", cached: "Using cached reading; no newer data", failed: "Refresh failed; keeping the previous reading", deferred: "Not refreshed this time" },
  zh: { reset: "重置于", busy: "读取中…", cached: "沿用缓存，尚无更新数据", failed: "刷新失败，保留上次读数", deferred: "本次未主动刷新" },
  "zh-TW": { reset: "重置於", busy: "讀取中…", cached: "沿用快取，尚無更新資料", failed: "重新整理失敗，保留上次讀數", deferred: "本次未主動重新整理" },
  ja: { reset: "にリセット", busy: "読み込み中…", cached: "新しいデータがないため、キャッシュを表示", failed: "更新に失敗しました。前回のデータを保持しています", deferred: "今回は更新を要求していません" },
  de: { reset: "Zurückgesetzt", busy: "Wird gelesen …", cached: "Keine neueren Daten; gespeicherten Stand anzeigen", failed: "Aktualisierung fehlgeschlagen; letzter Stand bleibt erhalten", deferred: "Diesmal nicht neu abgefragt" },
};
function fixtures() {
  const now = Date.now(), readAt = iso(now - 40 * 60000), resetsAt = iso(now + 2 * 3600000);
  return [
    { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@example.com", plan: "Pro", readAt, asOf: readAt, readDeferred: true, windows: [{ name: "5 hours", used: 28, resetsAt }] },
    { provider: "codex", name: "Codex", icon: "openai", user: "b@example.com", plan: "Pro", readAt, windows: [{ name: "5 hours", used: 50, resetsAt }] },
    { provider: "kimi", name: "Kimi", error: "HTTP 503", windows: [] },
    { provider: "relay", name: "Relay", balance: "$2.00", readAt, windows: [] },
    { provider: "unlimited", name: "Unlimited plan", readAt, windows: [{ name: "Requests", unlimited: true }] },
  ];
}
function serve(lang, data) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async route => {
    const url = new URL(route.request().url()), json = body => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") { data.gets++; return json(data.quotas); }
    if (url.pathname === "/api/usage/quotas/refresh") {
      assert.equal(route.request().method(), "POST");
      data.posts.push(url.search);
      await data.gate;
      if (data.behavior === "fail") return route.fulfill({ status: 503, body: "fixture offline" });
      const q = data.quotas.find(q => q.provider === url.searchParams.get("provider") && (q.user || "") === (url.searchParams.get("user") || ""));
      assert.ok(q, "refresh addressed a different account");
      if (data.behavior !== "cached") {
        delete q.error; delete q.asOf; delete q.readDeferred;
        q.readAt = iso(Date.now());
        if (q.balance) q.balance = "$3.00";
        else if (!q.windows.length) q.windows = [{ name: "Week", used: 10 }];
        else if (!q.windows[0].unlimited) q.windows[0].used = 61;
      }
      return json(data.quotas);
    }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, cost: 0, days: [], agents: [], models: [], keys: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  };
}
async function open(t, engine, lang) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 440, height: 640 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  const errors = [], data = { quotas: fixtures(), gets: 0, posts: [], behavior: "fresh" };
  page.on("pageerror", e => errors.push(e.message));
  await page.route("**/*", serve(lang, data));
  await page.goto("http://magpie.test/?mode=panel");
  await page.locator('#ptabs [data-ptab="usage"]').click();
  await page.locator('.pq-card[data-card="claude|a@example.com"]').waitFor();
  await page.evaluate(() => quotasLoading);
  return { page, data, errors };
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    test(`${engine} ${lang}: panel reset deadlines are named, not refresh glyphs`, async t => {
      const { page, errors } = await open(t, engine, lang);
      const deadline = page.locator('.pq-card[data-card="claude|a@example.com"] > .pq-sub').first();
      const text = await deadline.textContent();
      assert.ok(text.includes(w.reset), `deadline must say ${w.reset}, got ${text}`);
      assert.ok(!text.includes("↻"), "a noninteractive deadline looks like refresh");
      assert.equal(await deadline.locator("button").count(), 0);
      assert.deepEqual(errors, []);
    });
    test(`${engine} ${lang}: panel refresh is scoped, reachable, and honest about cached and failed reads`, async t => {
      const { page, data, errors } = await open(t, engine, lang);
      const card = page.locator('.pq-card[data-card="claude|a@example.com"]');
      const button = card.locator(".quota-refresh"), feedback = card.locator(".pq-feedback");
      assert.equal(await button.count(), 1, "the tray account has no real refresh action");
      assert.equal(await button.evaluate(b => getComputedStyle(b).opacity), "1", "refresh is discoverable before hovering");
      assert.match(await button.getAttribute("title"), /30/, "Claude's minimum reading interval is explained");
      assert.ok((await card.locator(".pq-read").textContent()).includes(w.deferred));
      for (const id of ["kimi", "relay", "unlimited"]) assert.equal(await page.locator(`.pq-card[data-card="${id}"] .quota-refresh`).count(), 1, `${id} needs the same action`);
      const other = page.locator('.pq-card[data-card="codex|b@example.com"]');
      const before = await other.locator(".quota-ago").getAttribute("data-ago");

      // macOS WebKit defaults to skipping buttons with Tab; Option-Tab
      // includes them (WKPreferences.tabFocusesLinks). No OS setting changes.
      const tab = engine === "webkit" && process.platform === "darwin" ? "Alt+Tab" : "Tab";
      const reach = async target => {
        for (let i = 0; i < 50 && !(await target.evaluate(b => b === document.activeElement)); i++) await page.keyboard.press(tab);
        assert.ok(await target.evaluate(b => b === document.activeElement), "refresh is not keyboard reachable");
      };
      await reach(button);
      const scroll = () => page.evaluate(() => [scrollY, document.querySelector("#panelQuota").closest(".view").scrollTop]);
      const y = await scroll(), gets = data.gets;
      let release; data.gate = new Promise(r => { release = r; }); t.after(() => release());
      await page.keyboard.press("Enter");
      await feedback.getByText(w.busy, { exact: true }).waitFor();
      assert.equal(await button.getAttribute("aria-busy"), "true");
      assert.equal(await button.getAttribute("aria-disabled"), "true");
      await page.keyboard.press("Enter");
      release(); data.gate = null;
      await card.locator(".pq-dial b", { hasText: "61%" }).waitFor();
      assert.deepEqual(data.posts, ["?provider=claude&user=a%40example.com"]);
      assert.equal(data.gets, gets, "single-card refresh asked for every account");
      assert.equal(await other.locator(".quota-ago").getAttribute("data-ago"), before, "another account's reading time changed");
      assert.equal(await other.locator(".pq-dial b").textContent(), "50%");
      assert.ok(await button.evaluate(b => b === document.activeElement), "redraw dropped keyboard focus");
      assert.deepEqual(await scroll(), y, "refresh scrolled the panel");
      assert.equal(await button.getAttribute("aria-busy"), "false");

      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const width of [440, 360]) {
          await page.setViewportSize({ width, height: 640 });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `panel-refresh-success-${engine}-${lang}-${width}.png`) });
        }
        await page.setViewportSize({ width: 440, height: 640 });
      }

      const reading = await card.locator(".quota-ago").getAttribute("data-ago");
      data.behavior = "cached";
      await page.keyboard.press("Enter");
      await feedback.getByText(w.cached, { exact: true }).waitFor();
      assert.equal(await card.locator(".quota-ago").getAttribute("data-ago"), reading, "cached data was given a new reading time");
      data.behavior = "fail";
      await page.keyboard.press("Space");
      await feedback.getByText(w.failed, { exact: true }).waitFor();
      assert.equal(await card.locator(".pq-dial b").textContent(), "61%", "failure discarded the previous allowance");
      assert.equal(await card.locator(".quota-ago").getAttribute("data-ago"), reading);

      data.behavior = "fresh";
      await reach(page.locator('.pq-card[data-card="kimi"] .quota-refresh'));
      await page.keyboard.press("Enter");
      await page.locator('.pq-card[data-card="kimi"] .pq-dial b', { hasText: "10%" }).waitFor();
      await reach(page.locator('.pq-card[data-card="relay"] .quota-refresh'));
      await page.keyboard.press("Enter");
      await page.locator('.pq-card[data-card="relay"] .pq-amt', { hasText: "$3.00" }).waitFor();
      // A later normal reading supersedes a transient request failure.
      data.quotas[0].readAt = iso(Date.parse(reading) + 60000);
      await page.evaluate(() => loadQuotas());
      assert.equal(await feedback.textContent(), "");
      for (const width of [440, 360]) {
        await page.setViewportSize({ width, height: 640 });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "panel overflows horizontally");
        assert.ok(await button.evaluate(b => { const r = b.getBoundingClientRect(); return r.width >= 24 && r.left >= 0 && r.right <= innerWidth; }), "refresh is clipped or too small");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `panel-refresh-${engine}-${lang}-${width}.png`), fullPage: true });
        }
      }
      assert.deepEqual(errors, []);
    });
  }
}
