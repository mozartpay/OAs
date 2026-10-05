const token =
  new URLSearchParams(window.location.search).get("token") ||
  new URLSearchParams(window.location.hash.slice(1)).get("token");

const elements = {
  network: document.getElementById("network"),
  walletAddress: document.getElementById("walletAddress"),
  walletSelect: document.getElementById("walletSelect"),
  walletBalance: document.getElementById("walletBalance"),
  activeBots: document.getElementById("activeBots"),
  totalBots: document.getElementById("totalBots"),
  openOffers: document.getElementById("openOffers"),
  connectionState: document.getElementById("connectionState"),
  lastRefresh: document.getElementById("lastRefresh"),
  botGrid: document.getElementById("botGrid"),
  emptyState: document.getElementById("emptyState"),
  locked: document.getElementById("locked"),
  refreshButton: document.getElementById("refreshButton"),
  guideButton: document.getElementById("guideButton"),
  guideDialog: document.getElementById("guideDialog"),
  closeGuide: document.getElementById("closeGuide"),
  createButton: document.getElementById("createButton"),
  emptyCreateButton: document.getElementById("emptyCreateButton"),
  strategyDialog: document.getElementById("strategyDialog"),
  strategyForm: document.getElementById("strategyForm"),
  strategyType: document.querySelector("#strategyForm select[name='type']"),
  strategyLevels: document.querySelector("#strategyForm input[name='levels']"),
  spreadHelp: document.getElementById("spreadHelp"),
  priceFeedHelp: document.getElementById("priceFeedHelp"),
  maxOpenHelp: document.getElementById("maxOpenHelp"),
  closeDialog: document.getElementById("closeDialog"),
  cancelDialog: document.getElementById("cancelDialog"),
  confirmDialog: document.getElementById("confirmDialog"),
  confirmTitle: document.getElementById("confirmTitle"),
  confirmMessage: document.getElementById("confirmMessage"),
  confirmYes: document.getElementById("confirmYes"),
  confirmNo: document.getElementById("confirmNo"),
  toast: document.getElementById("toast"),
};

let latestState = null;
let toastTimer = null;
let confirmAction = null;

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function formatNumber(value, digits = 7) {
  const number = Number(value || 0);
  if (!Number.isFinite(number)) return "0";
  return number.toLocaleString(undefined, {
    minimumFractionDigits: 0,
    maximumFractionDigits: digits,
  });
}

function formatPercent(value) {
  return `${formatNumber(value, 3)}%`;
}

function formatTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString();
}

function shortAddress(address) {
  if (!address) return "Not configured";
  if (address.length <= 18) return address;
  return `${address.slice(0, 10)}…${address.slice(-6)}`;
}

function showToast(message, kind = "success") {
  clearTimeout(toastTimer);
  elements.toast.textContent = message;
  elements.toast.className = `toast show ${kind}`;
  toastTimer = setTimeout(() => {
    elements.toast.className = "toast";
  }, 4200);
}

async function api(path, options = {}) {
  const headers = {
    "Content-Type": "application/json",
    ...(options.headers || {}),
  };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const response = await fetch(path, {
    ...options,
    headers,
    credentials: "same-origin",
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(body.error || `Request failed with HTTP ${response.status}`);
  }
  return body;
}

async function loadState() {
  const state = await api("/api/overview");
  latestState = state;
  render(state);
}

function connectEvents() {
  const eventsURL = token ? `/api/events?token=${encodeURIComponent(token)}` : "/api/events";
  const events = new EventSource(eventsURL);
  events.addEventListener("state", (event) => {
    latestState = JSON.parse(event.data);
    elements.connectionState.textContent = "Live";
    render(latestState);
  });
  events.addEventListener("error", () => {
    elements.connectionState.textContent = "Reconnecting";
  });
}

function render(state) {
  const bots = state.bots || [];
  const active = bots.filter((bot) => bot.strategy?.status === "active").length;
  const openOffers = bots.reduce((total, bot) => total + Number(bot.openOffers || 0), 0);

  const network = state.network || "stellar-testnet";
  if (network && ![...elements.network.options].some((option) => option.value === network)) {
    const option = document.createElement("option");
    option.value = network;
    option.textContent = network;
    elements.network.appendChild(option);
  }
  elements.network.value = network;

  const wallets = state.wallets || [];
  if (wallets.length > 0) {
    elements.walletAddress.classList.add("hidden");
    elements.walletSelect.classList.remove("hidden");
    const signature = wallets.map((w) => `${w.address}:${w.active}`).join("|");
    if (elements.walletSelect.dataset.signature !== signature) {
      elements.walletSelect.dataset.signature = signature;
      elements.walletSelect.innerHTML = wallets
        .map((w) => {
          const label = `${w.name || shortAddress(w.address)} · ${w.network || "unknown"}`;
          return `<option value="${escapeHTML(w.address)}" ${w.active ? "selected" : ""}>${escapeHTML(label)}</option>`;
        })
        .join("");
    }
    const activeWallet = wallets.find((w) => w.active);
    elements.walletSelect.value = activeWallet ? activeWallet.address : wallets[0].address;
  } else {
    elements.walletSelect.classList.add("hidden");
    elements.walletAddress.classList.remove("hidden");
    elements.walletAddress.textContent = shortAddress(state.wallet?.address);
  }
  const walletNetworkMismatch = state.wallet?.address && state.wallet.network && state.wallet.network !== state.network;
  elements.walletBalance.textContent = state.wallet?.address
    ? `${state.wallet.balance || "0"} · ${state.wallet.funded ? "funded" : "not funded"}${walletNetworkMismatch ? ` · on ${state.wallet.network}` : ""}`
    : "Create or select a CLI wallet";
  elements.activeBots.textContent = active;
  elements.totalBots.textContent = `${bots.length} configured`;
  elements.openOffers.textContent = openOffers;
  elements.lastRefresh.textContent = `Updated ${formatTime(state.generatedAt)}`;

  elements.emptyState.classList.toggle("hidden", bots.length !== 0);
  const expandedStrategies = new Set(
    [...elements.botGrid.querySelectorAll("details[data-strategy-id][open]")]
      .map((details) => details.dataset.strategyId),
  );
  const scrollX = window.scrollX;
  const scrollY = window.scrollY;
  elements.botGrid.innerHTML = bots
    .map((bot) => renderBot(bot, expandedStrategies.has(bot.strategy?.id)))
    .join("");
  requestAnimationFrame(() => window.scrollTo(scrollX, scrollY));
}

function renderBot(bot, expanded = false) {
  const strategy = bot.strategy || {};
  const runtime = bot.runtime || {};
  const performance = bot.performance || {};
  const status = strategy.status || "stopped";
  const pnl = Number(strategy.totalProfit || performance.totalReturn || 0);
  const pnlClass = pnl >= 0 ? "positive" : "negative";
  const error = runtime.lastError || "";
  const heartbeat = runtime.lastHeartbeat ? formatTime(runtime.lastHeartbeat) : "not running";
  const heartbeatMs = runtime.lastHeartbeat ? new Date(runtime.lastHeartbeat).getTime() : 0;
  const running = Boolean(runtime.pid && heartbeatMs && Date.now() - heartbeatMs < 120000);
  const runtimeMode = runtime.dryRun ? "dry" : "live";
  const killSwitch = Boolean(runtime.killSwitch);
  const canStart = status !== "active";
  const canPause = status === "active";
  const runDisabled = status !== "active" || running || killSwitch;

  return `
    <article class="bot-card status-${escapeHTML(status)}">
      <div class="bot-head">
        <div>
          <div class="eyebrow">${escapeHTML(strategy.type || "strategy")}</div>
          <h3>${escapeHTML(strategy.name || strategy.id)}</h3>
          <div class="pair">${escapeHTML(strategy.baseAsset || "")}/${escapeHTML(strategy.quoteAsset || "")} · ${escapeHTML(strategy.network || "")}${strategy.wallet ? ` · ${escapeHTML(shortAddress(strategy.wallet))}` : ""}</div>
        </div>
        <span class="status-pill ${escapeHTML(status)}">${escapeHTML(status)}</span>
      </div>

      <div class="stats">
        <div class="stat"><span>Offers</span><strong>${bot.openOffers || 0}</strong></div>
        <div class="stat"><span>Spread</span><strong>${formatPercent(bot.spreadPct)}</strong></div>
        <div class="stat"><span>P&L</span><strong class="${pnlClass}">${formatNumber(pnl, 7)}</strong></div>
        <div class="stat"><span>Cycles</span><strong>${runtime.cycles || 0}</strong></div>
      </div>

      <div class="pair mono">Heartbeat: ${escapeHTML(running ? `${runtimeMode} · ${heartbeat}` : heartbeat)}${killSwitch ? " · kill switch on" : ""}</div>
      ${error ? `<div class="error-box">${escapeHTML(error)}</div>` : ""}

      <div class="actions">
        <button class="button secondary" data-action="${canStart ? "start" : "pause"}" data-id="${escapeHTML(strategy.id)}">${canStart ? "Activate" : "Pause"}</button>
        <button class="button secondary" data-action="dry_run" data-id="${escapeHTML(strategy.id)}" ${status === "stopped" ? "disabled" : ""}>Run once</button>
        <button class="button secondary" data-action="run_dry" data-id="${escapeHTML(strategy.id)}" ${runDisabled ? "disabled" : ""}>Run dry</button>
        <button class="button secondary" data-action="run_live" data-id="${escapeHTML(strategy.id)}" ${runDisabled ? "disabled" : ""}>Run live</button>
        <button class="button secondary" data-action="stop" data-id="${escapeHTML(strategy.id)}" ${status === "stopped" ? "disabled" : ""}>Stop</button>
        <button class="button secondary" data-action="cancel" data-id="${escapeHTML(strategy.id)}" ${bot.openOffers ? "" : "disabled"}>Cancel offers</button>
        <button class="button danger" data-action="${killSwitch ? "clear_kill" : "kill"}" data-id="${escapeHTML(strategy.id)}">${killSwitch ? "Clear kill" : "Kill switch"}</button>
      </div>

      <details class="details" data-strategy-id="${escapeHTML(strategy.id)}" ${expanded ? "open" : ""}>
        <summary>Offers, fills, executions, and parameters</summary>
        ${renderOffers(bot.offers || [])}
        ${renderFills(bot.recentFills || [])}
        ${renderExecutions(bot.recentExecutions || [])}
        ${renderParameters(strategy)}
      </details>
    </article>`;
}

function renderOffers(offers) {
  const rows = offers.map((offer) => `
    <tr>
      <td>${escapeHTML(offer.intentId)}</td>
      <td>${escapeHTML(offer.side)}</td>
      <td>${formatNumber(offer.price)}</td>
      <td>${formatNumber(offer.amount)}</td>
      <td>${escapeHTML(offer.status)}</td>
      <td class="mono">${offer.offerId || "—"}</td>
    </tr>`).join("");
  return `<div class="detail-section"><h4>Managed offers</h4><div class="table-wrap"><table>
    <thead><tr><th>Intent</th><th>Side</th><th>Price</th><th>Amount</th><th>Status</th><th>Offer ID</th></tr></thead>
    <tbody>${rows || `<tr><td colspan="6">No managed offers</td></tr>`}</tbody>
  </table></div></div>`;
}

function renderFills(fills) {
  const rows = fills.map((fill) => `
    <tr>
      <td>${formatTime(fill.executedAt)}</td>
      <td>${formatNumber(fill.price)}</td>
      <td>${formatNumber(fill.amount)}</td>
      <td class="mono">${escapeHTML(fill.tradeId || fill.id || "")}</td>
    </tr>`).join("");
  return `<div class="detail-section"><h4>Recent fills</h4><div class="table-wrap"><table>
    <thead><tr><th>Time</th><th>Price</th><th>Amount</th><th>Trade</th></tr></thead>
    <tbody>${rows || `<tr><td colspan="4">No fills recorded</td></tr>`}</tbody>
  </table></div></div>`;
}

function renderExecutions(executions) {
  const rows = executions.map((execution) => `
    <tr>
      <td>${formatTime(execution.timestamp)}</td>
      <td>${escapeHTML(execution.action)}</td>
      <td>${escapeHTML(execution.status)}</td>
      <td>${formatNumber(execution.price)}</td>
      <td class="mono">${escapeHTML(execution.txHash || execution.error || "—")}</td>
    </tr>`).join("");
  return `<div class="detail-section"><h4>Recent executions</h4><div class="table-wrap"><table>
    <thead><tr><th>Time</th><th>Action</th><th>Status</th><th>Price</th><th>Transaction / error</th></tr></thead>
    <tbody>${rows || `<tr><td colspan="5">No executions recorded</td></tr>`}</tbody>
  </table></div></div>`;
}

function renderParameters(strategy) {
  const parameters = JSON.stringify(strategy.parameters || {}, null, 2);
  const limits = JSON.stringify(strategy.riskLimits || {}, null, 2);
  return `<div class="detail-section"><h4>Configuration</h4>
    <pre class="mono">parameters ${escapeHTML(parameters)}\n\nrisk limits ${escapeHTML(limits)}</pre>
  </div>`;
}

async function runAction(strategyID, action, options = {}) {
  const result = await api(`/api/strategies/${encodeURIComponent(strategyID)}/action`, {
    method: "POST",
    body: JSON.stringify({ action, ...options }),
  });
  if (action === "dry_run" && result.result) {
    const report = result.result;
    showToast(`Dry run: ${report.desired || 0} desired, ${report.created || 0} create, ${report.canceled || 0} cancel`);
  } else {
    showToast(`Action completed: ${action}`);
  }
  await loadState();
}

function askConfirmation(title, message, action) {
  elements.confirmTitle.textContent = title;
  elements.confirmMessage.textContent = message;
  confirmAction = action;
  elements.confirmDialog.showModal();
}

function updateStrategyHints() {
  const isBuySell = elements.strategyType.value === "buysell";
  const levels = Math.max(1, Number(elements.strategyLevels.value || 1));
  const minimumOffers = isBuySell ? levels * 2 : levels;
  elements.maxOpenHelp.textContent = `Suggested minimum: ${minimumOffers} for the current ${isBuySell ? "buysell" : "sell"} configuration.`;
  elements.spreadHelp.textContent = isBuySell
    ? "Total distance between the inner bid and ask around the reference price. A 1% spread places each side approximately 0.5% from midpoint."
    : "Percentage above the reference price for the first sell level. Higher values make fills less likely but improve the requested price.";
  elements.priceFeedHelp.textContent = isBuySell
    ? "Suggested: sdex:XLM/USDC/mid for two-sided market making. Use fixed:<price> for an explicit reference."
    : "Suggested: sdex:XLM/USDC/ask to track the market, or fixed:<price> for fixed-price distribution.";
}

function bindEvents() {
  elements.refreshButton.addEventListener("click", () => loadState().catch((error) => showToast(error.message, "error")));

  elements.network.addEventListener("change", () => {
    const next = elements.network.value;
    const revert = () => {
      elements.network.value = latestState?.network || "stellar-testnet";
    };
    const apply = () =>
      api("/api/network", { method: "POST", body: JSON.stringify({ network: next }) })
        .then(() => loadState())
        .then(() => showToast(`Network switched to ${next}`))
        .catch((error) => {
          revert();
          showToast(error.message, "error");
        });
    if (next === "stellar-mainnet") {
      revert();
      askConfirmation(
        "Switch to mainnet?",
        "Stellar mainnet uses real assets. Running dashboard workers will be stopped.",
        apply,
      );
      return;
    }
    apply();
  });

  elements.walletSelect.addEventListener("change", () => {
    const address = elements.walletSelect.value;
    api("/api/wallets/active", { method: "POST", body: JSON.stringify({ address }) })
      .then(() => loadState())
      .then(() => showToast("Active wallet updated"))
      .catch((error) => {
        showToast(error.message, "error");
        loadState().catch(() => {});
      });
  });

  elements.guideButton.addEventListener("click", () => elements.guideDialog.showModal());
  elements.closeGuide.addEventListener("click", () => elements.guideDialog.close());
  elements.strategyType.addEventListener("change", updateStrategyHints);
  elements.strategyLevels.addEventListener("input", updateStrategyHints);
  elements.createButton.addEventListener("click", () => {
    updateStrategyHints();
    elements.strategyDialog.showModal();
  });
  elements.emptyCreateButton.addEventListener("click", () => {
    updateStrategyHints();
    elements.strategyDialog.showModal();
  });
  elements.closeDialog.addEventListener("click", () => elements.strategyDialog.close());
  elements.cancelDialog.addEventListener("click", () => elements.strategyDialog.close());
  elements.confirmNo.addEventListener("click", () => elements.confirmDialog.close());
  elements.confirmYes.addEventListener("click", async () => {
    elements.confirmDialog.close();
    if (!confirmAction) return;
    try {
      await confirmAction();
    } catch (error) {
      showToast(error.message, "error");
    } finally {
      confirmAction = null;
    }
  });

  elements.botGrid.addEventListener("click", (event) => {
    const button = event.target.closest("button[data-action]");
    if (!button || button.disabled) return;
    const strategyID = button.dataset.id;
    const action = button.dataset.action;
    const bot = (latestState?.bots || []).find((item) => item.strategy?.id === strategyID);
    const name = bot?.strategy?.name || strategyID;

    if (action === "cancel") {
      askConfirmation(
        "Cancel strategy offers?",
        `This submits a testnet/mainnet cancellation transaction for ${name}'s managed offers.`,
        () => runAction(strategyID, "cancel", { allPair: false }),
      );
      return;
    }
    if (action === "kill") {
      askConfirmation(
        "Enable kill switch?",
        `${name} will pause and future reconciliations will be blocked until the switch is cleared.`,
        () => runAction(strategyID, "kill"),
      );
      return;
    }
    if (action === "run_live") {
      askConfirmation(
        "Run live strategy?",
        `${name} will submit real SDEX offer transactions until paused or stopped.`,
        () => runAction(strategyID, "run_live"),
      );
      return;
    }
    runAction(strategyID, action).catch((error) => showToast(error.message, "error"));
  });

  elements.strategyForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = new FormData(elements.strategyForm);
    const payload = {
      name: String(form.get("name") || "").trim(),
      type: String(form.get("type") || ""),
      baseAsset: String(form.get("baseAsset") || "").trim(),
      quoteAsset: String(form.get("quoteAsset") || "").trim(),
      parameters: {
        price_feed: String(form.get("priceFeed") || "").trim(),
        amount_per_level: Number(form.get("amountPerLevel")),
        levels: Number(form.get("levels")),
        spread_pct: Number(form.get("spreadPct")),
        level_spacing_pct: Number(form.get("levelSpacingPct")),
        interval_seconds: Number(form.get("intervalSeconds")),
        maker_only: form.get("makerOnly") === "on",
      },
      riskLimits: {
        maxPositionSize: Number(form.get("maxPositionSize")),
        maxOpenTrades: Number(form.get("maxOpenTrades")),
      },
    };
    try {
      await api("/api/strategies", { method: "POST", body: JSON.stringify(payload) });
      elements.strategyDialog.close();
      elements.strategyForm.reset();
      showToast("Strategy created");
      await loadState();
    } catch (error) {
      showToast(error.message, "error");
    }
  });
}

bindEvents();
updateStrategyHints();
loadState()
  .then(connectEvents)
  .catch((error) => {
    elements.connectionState.textContent = error.message === "unauthorized" ? "Locked" : "Error";
    if (error.message === "unauthorized") {
      elements.locked.classList.remove("hidden");
    }
    showToast(error.message, "error");
  });
