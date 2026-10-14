// Drive the shipped callbacks with held API replies. A background read and
// two different card refreshes may finish in any order (#1630).
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

const source = fs.readFileSync(process.env.MAGPIE_APP_JS || path.resolve(__dirname, "../assets/app.js"), "utf8").replace(/\r\n/g, "\n");
function section(start, end) {
  const a = source.indexOf(start), b = source.indexOf(end, a);
  assert(a >= 0 && b > a, `missing ${start}`);
  return source.slice(a, b);
}
const plain = (x) => JSON.parse(JSON.stringify(x));
const card = (user, used) => ({ provider: "claude", user, windows: [{ name: "7 days", used }] });

function page() {
  const context = vm.createContext({ Date, performance, setTimeout });
  vm.runInContext(`
    let quotas = [], quotasAt = 0, quotaHist = [], quotasAskedAt = 0, view = "usage";
    const requests = [], errors = [];
    const usageAcctKey = q => q.provider + "\\n" + (q.user || "").toLowerCase();
    const t = x => x;
    const el = () => ({ classList: { add() {} }, setAttribute() {} });
    function renderQuotas() {}
    function loadUpstream() {}
    function status(x) { errors.push(x); }
    function api(path) {
      if (path.includes("history")) return Promise.resolve([]);
      return new Promise((resolve, reject) => requests.push({ path, resolve, reject }));
    }
    ${section("let quotasLoading =", "// the period picker, in the page's head")}
    ${section("const quotaRefreshing =", "// familyQuota:")}
    globalThis.p = {
      requests, errors,
      seed: x => { quotas = x; },
      cards: () => quotas,
      load: () => loadQuotas(),
      refresh: q => quotaRefresh(q).onclick({ stopPropagation() {} }),
      answer: (i, x) => requests[i].resolve(x),
      fail: i => requests[i].reject(new Error("offline")),
    };
  `, context);
  return context.p;
}
const tick = () => new Promise(setImmediate);

for (const when of ["before", "during"]) {
  test(`a background read started ${when} a card refresh cannot undo its result`, async () => {
    const p = page(), old = card("a@x.com", 0), fresh = card("a@x.com", 32);
    p.seed([old]);
    let loading, refreshing;
    if (when === "before") {
      loading = p.load(); await tick(); refreshing = p.refresh(old);
    } else {
      refreshing = p.refresh(old); loading = p.load(); await tick();
    }
    const post = p.requests.findIndex(r => r.path.includes("/refresh?"));
    const get = p.requests.findIndex(r => r.path === "usage/quotas");
    assert(post >= 0 && get >= 0);
    p.answer(post, [fresh]); await refreshing;
    assert.equal(p.cards()[0].windows[0].used, 32);
    p.answer(get, [old]); await loading;
    assert.equal(p.cards()[0].windows[0].used, 32, "an older whole-page reply overwrote the refreshed card");
    // A later whole-page reading remains authoritative, including removals.
    const later = p.load(); await tick();
    p.answer(p.requests.length - 1, []); await later;
    assert.deepEqual(plain(p.cards()), []);
  });
}

test("refreshing two cards keeps both results when their full-list replies cross", async () => {
  const p = page(), a = card("a@x.com", 0), b = card("b@x.com", 0);
  p.seed([a, b]);
  const first = p.refresh(a), second = p.refresh(b);
  p.answer(1, [a, card("b@x.com", 61)]); await second;
  p.answer(0, [card("a@x.com", 32), b]); await first;
  assert.deepEqual(plain(p.cards()).map(q => q.windows[0].used), [32, 61], "one card's reply undid another card's completed refresh");
});

test("a failed card refresh leaves background readings usable", async () => {
  const p = page(), old = card("a@x.com", 0);
  p.seed([old]);
  const refresh = p.refresh(old), loading = p.load(); await tick();
  p.fail(0); await refresh;
  p.answer(1, [card("a@x.com", 32)]); await loading;
  assert.equal(p.cards()[0].windows[0].used, 32);
  assert.deepEqual(plain(p.errors), ["offline"]);
});
