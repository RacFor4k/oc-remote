(() => {
  if (window.__ocrInjected) return
  window.__ocrInjected = true

  const sessionID = () => {
    const m = location.pathname.match(/\/session\/([^/?#]+)/)
    if (m) return decodeURIComponent(m[1])
    return new URLSearchParams(location.search).get("session") || ""
  }

  const button = document.createElement("button")
  button.textContent = "Remote"
  button.id = "ocr-fab"
  button.setAttribute("aria-label", "oc-remote")
  button.style.cssText = [
    "position:fixed", "right:12px", "bottom:88px", "z-index:2147483647",
    "padding:8px 12px", "border-radius:999px", "border:1px solid rgba(255,255,255,.18)",
    "background:rgba(17,19,24,.92)", "color:#e6e8ee", "font:600 12px/1 ui-sans-serif,system-ui,sans-serif",
    "letter-spacing:.02em", "cursor:pointer", "backdrop-filter:blur(8px)",
    "box-shadow:0 6px 24px rgba(0,0,0,.35)",
  ].join(";")

  const overlay = document.createElement("div")
  overlay.id = "ocr-overlay"
  overlay.style.cssText = [
    "position:fixed", "inset:0", "z-index:2147483646", "display:none",
    "background:rgba(0,0,0,.45)", "backdrop-filter:blur(2px)",
    "align-items:center", "justify-content:center", "padding:16px",
  ].join(";")

  const frame = document.createElement("iframe")
  frame.title = "oc-remote"
  frame.style.cssText = [
    "width:min(420px,100%)", "height:min(640px,100%)", "border:0",
    "border-radius:16px", "background:#0f1115", "box-shadow:0 24px 80px rgba(0,0,0,.5)",
  ].join(";")

  overlay.appendChild(frame)
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) overlay.style.display = "none"
  })
  button.addEventListener("click", () => {
    frame.src = "/_ocr/?embed=1&session=" + encodeURIComponent(sessionID())
    overlay.style.display = "flex"
  })
  window.addEventListener("message", (e) => {
    if (e.origin === location.origin && e.data === "ocr-close") overlay.style.display = "none"
  })

  document.body.appendChild(overlay)
  document.body.appendChild(button)
})()
