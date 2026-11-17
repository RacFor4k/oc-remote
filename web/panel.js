(() => {
  const $ = (id) => document.getElementById(id)
  const q = new URLSearchParams(location.search)
  const SESSION = q.get("session") || ""
  const EMBED = q.get("embed") === "1"
  let timer

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c])

  async function api(path, opts = {}) {
    const headers = Object.assign({}, opts.headers || {})
    if ((opts.method || "GET") !== "GET") headers["X-OCR-CSRF"] = "1"
    const res = await fetch(path, { ...opts, headers, credentials: "same-origin" })
    if (res.status === 204) return null
    if (!res.ok) {
      let text = ""
      try { text = await res.text() } catch {}
      throw new Error(text || res.statusText)
    }
    const type = res.headers.get("content-type") || ""
    return type.includes("application/json") ? res.json() : res.text()
  }

  function msg(text) { $("msg").textContent = text || "" }

  function ago(ts) {
    if (!ts) return "never"
    const s = Math.max(0, Math.round(Date.now() / 1000 - ts))
    if (s < 60) return s + "s ago"
    if (s < 3600) return Math.round(s / 60) + "m ago"
    if (s < 86400) return Math.round(s / 3600) + "h ago"
    return Math.round(s / 86400) + "d ago"
  }

  async function refresh() {
    try {
      const s = await api("/_ocr/api/status")
      $("status").innerHTML = `
        <div class="kv">
          <div>Tenant</div><div>${esc(s.tenant)}</div>
          <div>Device</div><div>${esc(s.device)}</div>
          <div>Status</div><div><span class="dot ${s.online ? "on" : ""}"></span>${s.online ? "online" : "offline"}</div>
          <div>Sessions</div><div>${s.sessions}</div>
          <div>Push</div><div>${s.push ? "enabled" : "off"}</div>
          <div>Last seen</div><div>${esc(ago(s.lastSeen))}</div>
        </div>`
      $("push").textContent = s.push ? "Notifications enabled" : "Enable notifications"
    } catch (e) {
      $("status").innerHTML = `<p class="muted">${esc(e.message)}</p>`
    }
  }

  async function pair() {
    msg("")
    $("pair").disabled = true
    try {
      const r = await api("/_ocr/api/pair", { method: "POST" })
      $("link").hidden = false
      $("url").value = r.url
      const until = Number(r.expiresAt) * 1000
      clearInterval(timer)
      const tick = () => {
        const left = Math.max(0, Math.round((until - Date.now()) / 1000))
        $("expiry").textContent = left ? `(expires in ${left}s)` : "(expired)"
        if (!left) clearInterval(timer)
      }
      tick()
      timer = setInterval(tick, 1000)
    } catch (e) {
      msg(e.message)
    } finally {
      $("pair").disabled = false
    }
  }

  async function logout() {
    if (!confirm("Sign out every browser session of this tenant?")) return
    try {
      await api("/_ocr/api/logout-all", { method: "POST" })
      location.replace("/_ocr/")
    } catch (e) {
      msg(e.message)
    }
  }

  async function loadAgents() {
    if (!SESSION) return
    try {
      const list = await api("/_ocr/api/agents")
      const info = await fetch("/api/session/" + encodeURIComponent(SESSION), { credentials: "same-origin" }).then((r) => r.json())
      const sel = $("agentSelect")
      sel.innerHTML = ""
      for (const a of list.agents || []) {
        const o = document.createElement("option")
        o.value = a.id
        o.textContent = a.name || a.id
        if (a.id === info.agent) o.selected = true
        sel.appendChild(o)
      }
      $("agent").hidden = !(list.agents || []).length
    } catch {
      $("agent").hidden = true
    }
  }

  $("pair").onclick = pair
  $("logout").onclick = logout
  $("push").onclick = () => window.open("/_ocr/notify", "_blank", "noopener")
  $("copy").onclick = async () => {
    try {
      await navigator.clipboard.writeText($("url").value)
      $("copy").textContent = "Copied"
      setTimeout(() => ($("copy").textContent = "Copy"), 1500)
    } catch {
      $("url").select()
    }
  }
  $("agentSelect").onchange = async (e) => {
    msg("")
    try {
      await api("/_ocr/api/agent", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ sessionID: SESSION, agent: e.target.value }),
      })
      msg("Agent switched")
    } catch (err) {
      msg(err.message)
    }
  }
  $("close").hidden = !EMBED
  $("close").onclick = () => window.parent.postMessage("ocr-close", "*")

  refresh()
  loadAgents()
  setInterval(refresh, 30000)
})()
