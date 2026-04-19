(() => {
  const API = window.API_BASE || "";

  const $ = (id) => document.getElementById(id);
  const show = (id, v = true) => { const el = $(id); if (el) el.hidden = !v; };

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

  // ---- settings ----

  async function loadSettings() {
    const s = await api("/api/settings");
    $("emails").value = (s.emails || []).join("\n");
    $("cadence").value = s.cadence || "off";
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
      await api("/api/settings", {
        method: "PUT",
        body: JSON.stringify({ emails, cadence }),
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

  async function loadKeys() {
    const r = await api("/api/keys");
    const body = $("keys-body");
    body.innerHTML = "";
    const keys = r.keys || [];
    if (keys.length === 0) {
      const tr = document.createElement("tr");
      tr.innerHTML = `<td colspan="5" class="paragraph" style="color:#888;">No keys yet.</td>`;
      body.appendChild(tr);
      return;
    }
    for (const k of keys) {
      const tr = document.createElement("tr");
      const created = k.created_at ? new Date(k.created_at).toISOString().slice(0, 10) : "—";
      const short = (k.key_hash || "").slice(0, 12);
      const stateCell = k.enabled
        ? `<span class="status-ok">enabled</span>`
        : `<span style="color:#888;">disabled</span>`;
      const action = k.enabled
        ? `<button class="danger-link" data-disable="${k.key_hash}">disable</button>`
        : "";
      tr.innerHTML = `
        <td>${escapeHTML(k.label || "")}</td>
        <td>${created}</td>
        <td><code class="code">${short}…</code></td>
        <td>${stateCell}</td>
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

  function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    })[c]);
  }

  // ---- boot ----

  (async () => {
    try {
      const me = await api("/api/me");
      if (me.unauthorized) {
        show("signed-out", true);
        return;
      }
      $("whoami-email").textContent = me.email || me.user_id;
      $("whoami-provider").textContent = me.provider;
      show("signed-in", true);
      await Promise.all([loadSettings(), loadKeys()]);
    } catch (err) {
      show("signed-out", true);
      console.error(err);
    }
  })();
})();
