/* OpenRouter Cost Monitor — vanilla JS, EventSource */
"use strict";

const $ = (id) => document.getElementById(id);

const fmtMoney = (v, digits = 4) =>
  v == null ? "—" : "$" + Number(v).toFixed(digits);

const fmtInt = (v) => (v == null ? "—" : Number(v).toLocaleString("en-US"));

const fmtTime = (iso) => {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d)) return String(iso);
  return d.toISOString().replace("T", " ").slice(0, 19) + " UTC";
};

const fmtLatency = (s) => {
  if (s == null || isNaN(s)) return "—";
  return Number(s).toFixed(2);
};

// Prices come from the API per token; we display them per million tokens (the
// conventional unit in model pricing), so multiply by 1e6 for display only.
const MTOK = 1e6;

const fmtPerMTok = (v) => {
  if (v == null || isNaN(v)) return "—";
  const perM = Number(v) * MTOK;
  return "$" + perM.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
};

function renderBalance(b) {
  $("remaining").textContent = b && b.bought >= 0 ? fmtMoney(b.remaining, 2) : "aguardando…";
  $("used").textContent = b && b.bought >= 0 ? fmtMoney(b.used, 2) : "—";
  $("bought").textContent = b && b.bought >= 0 ? fmtMoney(b.bought, 2) : "—";
  $("balanceHover").classList.toggle("has-data", !!(b && b.bought >= 0));
}

function renderPrices(prices) {
  const tb = $("priceRows");
  tb.innerHTML = "";
  const sorted = [...(prices || [])].sort((a, b) =>
    (a.model || "").localeCompare(b.model || "") || (a.provider || "").localeCompare(b.provider || ""));
  $("priceEmpty").classList.toggle("hidden", sorted.length > 0);
  for (const p of sorted) {
    const tr = document.createElement("tr");
    if (!p.found) {
      // Model is in use but its price could not be resolved per provider.
      tr.className = "price-miss";
      tr.innerHTML =
        `<td class="model">${esc(p.model)}</td>` +
        `<td class="provider">${esc(p.provider || "—")}</td>` +
        `<td class="num" colspan="3"><span class="sub">preço não encontrado para o provedor</span></td>`;
      tb.appendChild(tr);
      continue;
    }
    tr.innerHTML =
      `<td class="model">${esc(p.model)}</td>` +
      `<td class="provider">${esc(p.provider || "—")}</td>` +
      `<td class="num">${fmtPerMTok(p.cached)}</td>` +
      `<td class="num">${fmtPerMTok(p.prompt)}</td>` +
      `<td class="num">${fmtPerMTok(p.completion)}</td>`;
    tb.appendChild(tr);
  }
}

const RECENT_VISIBLE = 3;
let recentExpanded = false;
let recentCache = [];

function renderRecent(rows) {
  recentCache = rows;
  const tb = $("recentRows");
  tb.innerHTML = "";
  $("recentEmpty").classList.toggle("hidden", rows.length > 0);

  const sorted = [...rows].sort((a, b) => new Date(b.minute) - new Date(a.minute));
  const limit = recentExpanded ? sorted.length : Math.min(RECENT_VISIBLE, sorted.length);
  for (let i = 0; i < limit; i++) {
    const r = sorted[i];
    const tr = document.createElement("tr");
    tr.innerHTML =
      `<td>${esc(r.minute)}</td>` +
      `<td class="model">${esc(r.model)}</td>` +
      `<td>${esc(r.provider || "")}</td>` +
      `<td class="num">${fmtInt(r.requests)}</td>` +
      `<td class="num">${fmtMoney(r.cost, 8)}</td>` +
      `<td class="num">${fmtInt(r.tokens)}</td>`;
    tb.appendChild(tr);
  }

  const btn = $("recentToggle");
  const hasHidden = sorted.length > RECENT_VISIBLE;
  btn.classList.toggle("hidden", !hasHidden);
  if (hasHidden) {
    const hidden = sorted.length - (recentExpanded ? 0 : RECENT_VISIBLE);
    btn.textContent = recentExpanded
      ? "Ocultar linhas restantes"
      : `Mostrar ${fmtInt(hidden)} ${hidden === 1 ? "linha" : "linhas"} restantes (${fmtInt(sorted.length)} no total)`;
  }
}

$("recentToggle").addEventListener("click", () => {
  recentExpanded = !recentExpanded;
  renderRecent(recentCache);
});

function renderGenerations(rows) {
  const tb = $("genRows");
  tb.innerHTML = "";
  $("genEmpty").classList.toggle("hidden", rows.length > 0);
  const sorted = [...rows].sort((a, b) => new Date(b.created_at) - new Date(a.created_at));

  // Column cost annotation: token count + estimated cost, when pricing known.
  const tk = (tokens, cost, hasPricing) =>
    hasPricing
      ? `${fmtInt(tokens)} <span class="sub">${fmtMoney(cost, 8)}</span>`
      : fmtInt(tokens);

  for (const g of sorted) {
    const tr = document.createElement("tr");
    if (g.is_new) tr.className = "isNew";
    // "In tks" excludes the cached tokens (cached are reported separately).
    const inTks = (g.tokens_prompt || 0) - (g.tokens_cached || 0);
    const costCol = g.has_pricing
      ? `${fmtMoney(g.cost, 8)} <span class="sub">(calc: ${fmtMoney(g.cost_calc, 8)})</span>`
      : fmtMoney(g.cost, 8);
    tr.innerHTML =
      `<td>${fmtTime(g.created_at)}</td>` +
      `<td class="model">${esc(g.model)}</td>` +
      `<td>${esc(g.provider)}</td>` +
      `<td class="num">${tk(g.tokens_cached, g.cost_in_cached, g.has_pricing)}</td>` +
      `<td class="num">${tk(inTks, g.cost_in, g.has_pricing)}</td>` +
      `<td class="num">${tk(g.tokens_reasoning, g.cost_reasoning, g.has_pricing)}</td>` +
      `<td class="num">${tk(g.tokens_completion, g.cost_out, g.has_pricing)}</td>` +
      `<td class="num">${costCol}</td>`;
    tb.appendChild(tr);
  }
}

function renderBanners(s) {
  $("errorBanner").classList.toggle("hidden", !s.last_error);
  if (s.last_error) {
    $("errorBanner").textContent = "Falha na API: " + s.last_error;
    $("errorBanner").className = "banner err";
  }
  $("warnBanner").classList.toggle("hidden", !s.warn_truncated);
  if (s.warn_truncated) {
    $("warnBanner").textContent = "Atenção: resposta analytics truncada (partial) — os totais podem não refletir o uso real.";
    $("warnBanner").className = "banner warn";
  }
}

// Reload indicator: a thin bar that fills toward the next tick and flashes
// each time a poll completes.
let loaderTimer = null;
function pulseLoader(intervalMs) {
  const bar = $("reloadBar");
  if (!bar) return;
  if (loaderTimer) clearInterval(loaderTimer);
  const start = Date.now();
  bar.style.width = "0%";
  loaderTimer = setInterval(() => {
    const elapsed = Date.now() - start;
    const pct = Math.min(100, intervalMs > 0 ? (elapsed / intervalMs) * 100 : 0);
    bar.style.width = pct.toFixed(1) + "%";
    if (pct >= 100) { clearInterval(loaderTimer); loaderTimer = null; }
  }, 50);
  // flash to highlight that a reload just happened
  bar.classList.remove("flash");
  void bar.offsetWidth;
  bar.classList.add("flash");
}

function renderMeta(s) {
  $("metaLine").textContent = s.meta_ready
    ? "conectado à API OpenRouter"
    : "Meta não disponível: " + (s.last_error || "verificando…");

  if (s.updated_at) {
    $("lastUpdate").textContent = "atualizado " + fmtTime(s.updated_at);
  }
  pulseLoader(s.interval_ms || 0);
}

function render(s) {
  renderBalance(s.balance);
  renderPrices(s.prices);
  renderRecent(s.hits || []);
  renderGenerations(s.generations || []);
  renderBanners(s);
  renderMeta(s);
}

function esc(s) {
  if (s == null) return "";
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

// Initial load + SSE updates
async function boot() {
  try {
    const res = await fetch("/api/state");
    render(await res.json());
  } catch (_) { /* first SSE event will cover it */ }

  const es = new EventSource("/api/stream");
  es.addEventListener("tick", (ev) => {
    try {
      render(JSON.parse(ev.data));
    } catch (e) {
      console.error("bad tick payload", e);
    }
  });
  es.onerror = () => {
    $("errorBanner").className = "banner err";
    $("errorBanner").textContent = "Conexão SSE interrompida; reconectando…";
    $("errorBanner").classList.remove("hidden");
  };
}

boot();
