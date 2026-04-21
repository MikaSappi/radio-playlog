(() => {
  const API = window.API_BASE || "";

  const $ = (id) => document.getElementById(id);
  const show = (id, v = true) => { const el = $(id); if (el) el.hidden = !v; };

  // api() is the one entry point for authenticated JSON calls. It handles
  // 401 uniformly so callers can early-return on sign-out, and it throws
  // on any non-2xx so success paths stay flat.
  async function api(path, opts = {}) {
    const res = await fetch(API + path, {
      credentials: "include",
      headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
      ...opts,
    });
    if (res.status === 401) return { unauthorized: true };
    const text = await res.text();
    let body = null;
    try { body = text ? JSON.parse(text) : null; } catch { /* noop */ }
    if (!res.ok) throw new Error((body && body.error) || `HTTP ${res.status}`);
    return body;
  }

  // ---- login wiring ----

  $("btn-google").href = API + "/auth/google/start";
  $("btn-microsoft").href = API + "/auth/microsoft/start";

  $("btn-logout").addEventListener("click", async (e) => {
    e.preventDefault();
    try { await api("/api/logout", { method: "POST" }); } catch {}
    location.reload();
  });

  // ---- tabs ----

  const panels = ["keys", "logs", "export", "settings"];
  for (const btn of document.querySelectorAll(".tab")) {
    btn.addEventListener("click", () => {
      const tab = btn.getAttribute("data-tab");
      for (const t of document.querySelectorAll(".tab")) t.classList.toggle("active", t === btn);
      for (const p of panels) show("panel-" + p, p === tab);
      // Refresh data for the tab being entered so the user sees current
      // state rather than whatever was loaded at sign-in.
      if (tab === "logs") loadLogs();
      if (tab === "keys") loadKeys();
    });
  }

  // ---- settings ----

  const DEFAULT_SETTINGS = { emails: [], cadence: "off", timezone: "" };

  async function loadSettings() {
    // A brand-new user has no settings row yet, and a fresh deployment may
    // not have the user_settings table populated. Fall back to defaults so
    // the rest of the app (keys tab, logs, etc.) still boots.
    let s;
    try {
      s = await api("/api/settings");
    } catch (err) {
      console.warn("settings load failed, using defaults:", err);
      s = DEFAULT_SETTINGS;
    }
    $("emails").value = (s.emails || []).join("\n");
    $("cadence").value = s.cadence || "off";
    $("timezone").value = s.timezone || "";
  }

  $("form-settings").addEventListener("submit", async () => {
    const btn = $("btn-save-settings");
    const status = $("settings-status");
    btn.disabled = true;
    status.textContent = "Saving…";
    status.className = "paragraph";
    try {
      const emails = $("emails").value
        .split(/\r?\n/)
        .map((x) => x.trim())
        .filter(Boolean);
      const cadence = $("cadence").value;
      const timezone = $("timezone").value.trim();
      await api("/api/settings", {
        method: "PUT",
        body: JSON.stringify({ emails, cadence, timezone }),
      });
      status.textContent = "Saved.";
      status.className = "paragraph status-ok";
    } catch (err) {
      status.textContent = err.message;
      status.className = "paragraph status-err";
    } finally {
      btn.disabled = false;
    }
  });

  // ---- keys ----

  // Cached keys list — the logs and export tabs share it for their
  // filter dropdowns so we don't re-query.
  let cachedKeys = [];

  async function loadKeys() {
    const r = await api("/api/keys");
    const body = $("keys-body");
    body.innerHTML = "";
    const keys = r.keys || [];
    cachedKeys = keys;
    populateKeyDropdowns(keys);
    if (keys.length === 0) {
      const tr = document.createElement("tr");
      tr.innerHTML = `<td colspan="6" class="paragraph" style="color:#888;">No keys yet.</td>`;
      body.appendChild(tr);
      return;
    }
    for (const k of keys) {
      const tr = document.createElement("tr");
      const created = k.created_at ? new Date(k.created_at).toISOString().slice(0, 10) : "—";
      const lastSeen = k.last_seen_at ? relativeAge(k.last_seen_at) : "never";
      const short = (k.key_hash || "").slice(0, 12);
      const led = `<span class="led led-${k.status || "idle"}" title="${k.status || "idle"}"></span>`;
      const labelCell = k.enabled
        ? `<span class="renameable" data-rename="${k.key_hash}">${escapeHTML(k.label || "(unnamed)")}</span>`
        : `<span class="subtle">${escapeHTML(k.label || "(unnamed)")}</span>`;
      const action = k.enabled
        ? `<button class="danger-link" data-disable="${k.key_hash}">disable</button>`
        : "";
      tr.innerHTML = `
        <td>${led}<span class="subtle">${k.status || ""}</span></td>
        <td>${labelCell}</td>
        <td>${created}</td>
        <td title="${k.last_seen_at || ""}">${lastSeen}</td>
        <td><code class="code">${short}…</code></td>
        <td>${action}</td>`;
      body.appendChild(tr);
    }
    body.querySelectorAll("[data-disable]").forEach((el) => {
      el.addEventListener("click", async () => {
        if (!confirm("Disable this key? The logger will reject it on next reload.")) return;
        const hash = el.getAttribute("data-disable");
        try {
          await api("/api/keys/" + encodeURIComponent(hash), { method: "DELETE" });
          await loadKeys();
        } catch (err) { alert(err.message); }
      });
    });
    body.querySelectorAll("[data-rename]").forEach((el) => {
      el.addEventListener("click", async () => {
        const hash = el.getAttribute("data-rename");
        const current = el.textContent;
        const next = prompt("New label for this key:\n(Applies only to future plays — existing history keeps the old label.)", current);
        if (next == null) return;
        const trimmed = next.trim();
        if (!trimmed || trimmed === current) return;
        try {
          await api("/api/keys/" + encodeURIComponent(hash), {
            method: "PATCH",
            body: JSON.stringify({ label: trimmed }),
          });
          await loadKeys();
        } catch (err) { alert(err.message); }
      });
    });
  }

  $("form-new-key").addEventListener("submit", async () => {
    const btn = $("btn-new-key");
    btn.disabled = true;
    try {
      const label = $("new-key-label").value.trim();
      const r = await api("/api/keys", {
        method: "POST",
        body: JSON.stringify({ label }),
      });
      $("new-key-label").value = "";
      $("new-key-raw").textContent = r.key;
      show("new-key-reveal", true);
      await loadKeys();
    } catch (err) {
      alert(err.message);
    } finally {
      btn.disabled = false;
    }
  });

  function populateKeyDropdowns(keys) {
    for (const id of ["logs-key", "export-key"]) {
      const sel = $(id);
      if (!sel) continue;
      const current = sel.value;
      sel.innerHTML = `<option value="">All keys</option>`;
      for (const k of keys) {
        const opt = document.createElement("option");
        opt.value = k.key_hash;
        opt.textContent = (k.label || k.key_hash.slice(0, 12) + "…") + (k.enabled ? "" : " (disabled)");
        sel.appendChild(opt);
      }
      sel.value = current || "";
    }
  }

  // ---- logs explorer ----

  async function loadLogs() {
    const status = $("logs-status");
    status.textContent = "Loading…";
    try {
      const params = new URLSearchParams();
      const key = $("logs-key").value;
      const from = $("logs-from").value;
      const to = $("logs-to").value;
      const limit = $("logs-limit").value;
      if (key) params.set("key_hash", key);
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      if (limit) params.set("limit", limit);
      const q = params.toString();
      const r = await api("/api/logs" + (q ? "?" + q : ""));
      const body = $("logs-body");
      body.innerHTML = "";
      const plays = r.plays || [];
      if (plays.length === 0) {
        body.innerHTML = `<tr><td colspan="4" class="paragraph subtle">No plays in this window.</td></tr>`;
        status.textContent = `0 plays · tz ${r.timezone || "UTC"}`;
        return;
      }
      for (const p of plays) {
        const tr = document.createElement("tr");
        const ts = (p.timestamp_local || p.timestamp || "").replace("T", " ").slice(0, 19);
        tr.innerHTML = `
          <td><code class="code">${escapeHTML(ts)}</code></td>
          <td>${escapeHTML(p.label || "")}</td>
          <td>${escapeHTML(p.artist || "")}</td>
          <td>${escapeHTML(p.title || "")}</td>`;
        body.appendChild(tr);
      }
      status.textContent = `${plays.length} plays · tz ${r.timezone || "UTC"}`;
    } catch (err) {
      status.textContent = err.message;
      status.className = "paragraph status-err";
    }
  }
  $("btn-logs-reload").addEventListener("click", loadLogs);
  for (const id of ["logs-key", "logs-from", "logs-to", "logs-limit"]) {
    $(id).addEventListener("change", loadLogs);
  }

  // ---- export builder ----

  // Swap between preset-driven and custom-range mode. The date inputs
  // are only meaningful when "custom" is selected.
  function updateExportMode() {
    const preset = $("export-preset").value;
    const custom = preset === "custom";
    $("export-from").disabled = !custom;
    $("export-to").disabled = !custom;
  }
  $("export-preset").addEventListener("change", updateExportMode);
  updateExportMode();

  $("btn-export").addEventListener("click", async () => {
    const btn = $("btn-export");
    const status = $("export-status");
    btn.disabled = true;
    status.textContent = "Building…";
    status.className = "paragraph subtle";
    try {
      const cols = Array.from($("export-cols").querySelectorAll("input:checked")).map((el) => el.value);
      if (cols.length === 0) {
        status.textContent = "Pick at least one column.";
        status.className = "paragraph status-err";
        return;
      }
      const payload = {
        format: $("export-format").value,
        columns: cols,
        preset: $("export-preset").value,
        key_hash: $("export-key").value,
      };
      if (payload.preset === "custom") {
        payload.from = $("export-from").value;
        payload.to = $("export-to").value;
        if (!payload.from || !payload.to) {
          status.textContent = "Pick From and To dates.";
          status.className = "paragraph status-err";
          return;
        }
      }
      const res = await fetch(API + "/api/export", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!res.ok) {
        const text = await res.text();
        let err = text;
        try { err = JSON.parse(text).error || err; } catch {}
        throw new Error(err || ("HTTP " + res.status));
      }
      const rowCount = res.headers.get("X-Row-Count") || "?";
      const disp = res.headers.get("Content-Disposition") || "";
      let filename = "playlog-export." + payload.format;
      const m = /filename="([^"]+)"/.exec(disp);
      if (m) filename = m[1];
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      status.textContent = `Downloaded ${filename} (${rowCount} rows).`;
      status.className = "paragraph status-ok";
    } catch (err) {
      status.textContent = err.message;
      status.className = "paragraph status-err";
    } finally {
      btn.disabled = false;
    }
  });

  // ---- utilities ----

  function escapeHTML(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    })[c]);
  }

  // relativeAge returns a compact human label for a past timestamp. Used
  // under each key's "last seen" column so the table reads at a glance.
  function relativeAge(iso) {
    const then = Date.parse(iso);
    if (!Number.isFinite(then)) return iso;
    const sec = Math.max(0, Math.floor((Date.now() - then) / 1000));
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 48) return `${hr}h ago`;
    const d = Math.floor(hr / 24);
    return `${d}d ago`;
  }

  function showSignedIn() {
    show("signed-in", true);
    show("signed-out", false);
  }
  function showSignedOut() {
    show("signed-out", true);
    show("signed-in", false);
  }

  // ---- live LED refresh ----
  // Poll /api/keys every few seconds so LEDs feel live — but only while
  // the keys tab is actually visible and the page is foregrounded, so we
  // aren't hammering BigQuery for a hidden tab. CSS transitions on .led
  // make the color change smooth.
  const LED_REFRESH_MS = 180000;
  let ledTimer = null;
  function keysTabActive() {
    const el = $("panel-keys");
    return !!el && !el.hidden;
  }
  function startLedRefresh() {
    if (ledTimer) return;
    ledTimer = setInterval(() => {
      if (document.hidden || !keysTabActive()) return;
      loadKeys().catch(() => {});
    }, LED_REFRESH_MS);
  }

  // ---- boot ----

  (async () => {
    try {
      const me = await api("/api/me");
      if (me.unauthorized) {
        showSignedOut();
        return;
      }
      $("whoami-email").textContent = me.email || me.user_id;
      $("whoami-provider").textContent = me.provider;
      showSignedIn();
      await Promise.all([loadSettings(), loadKeys()]);
      startLedRefresh();
    } catch (err) {
      showSignedOut();
      console.error(err);
    }
  })();
})();
