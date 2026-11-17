(() => {
  const $ = (id) => document.getElementById(id)
  const msg = (t) => ($("msg").textContent = t || "")
  const b64 = (s) => {
    const pad = "=".repeat((4 - (s.length % 4)) % 4)
    const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"))
    return Uint8Array.from(raw, (c) => c.charCodeAt(0))
  }

  $("back").onclick = () => history.length > 1 ? history.back() : location.replace("/_ocr/")

  $("enable").onclick = async () => {
    msg("")
    if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
      msg("Push is not supported in this browser.")
      return
    }
    try {
      const reg = await navigator.serviceWorker.register("/_ocr/sw.js", { scope: "/_ocr/" })
      const permission = await Notification.requestPermission()
      if (permission !== "granted") {
        msg("Notification permission was not granted.")
        return
      }
      const key = await fetch("/_ocr/api/push/key", { credentials: "same-origin" }).then((r) => r.json())
      let sub = await reg.pushManager.getSubscription()
      if (!sub) {
        sub = await reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: b64(key.key),
        })
      }
      const res = await fetch("/_ocr/api/push/subscribe", {
        method: "POST",
        credentials: "same-origin",
        headers: { "content-type": "application/json", "X-OCR-CSRF": "1" },
        body: JSON.stringify(sub.toJSON()),
      })
      if (!res.ok) throw new Error(await res.text())
      msg("Notifications enabled.")
    } catch (e) {
      msg(e.message || String(e))
    }
  }
})()
