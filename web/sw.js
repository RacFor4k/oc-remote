self.addEventListener("push", (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {}
  const title = data.title || "oc-remote"
  const body = data.body || "OpenCode needs permission"
  event.waitUntil(
    self.registration.showNotification(title, {
      body,
      tag: data.tag || "oc-remote",
      data: { url: data.url || "/" },
    }),
  )
})

self.addEventListener("notificationclick", (event) => {
  event.notification.close()
  const url = (event.notification.data && event.notification.data.url) || "/"
  event.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
      for (const client of list) {
        if (client.url.startsWith(self.location.origin) && "focus" in client) return client.focus()
      }
      return clients.openWindow(url)
    }),
  )
})
