import { Plugin } from "@opencode/plugin"
import { OcRemoteRpc } from "./rpc.js"
import { Tunnel } from "./tunnel.js"

export default Plugin.define({
  id: "oc-remote",
  async setup(ctx) {
    const url = typeof ctx.options.url === "string" ? ctx.options.url.trim() : ""
    const key = typeof ctx.options.key === "string" ? ctx.options.key.trim() : ""
    const name = typeof ctx.options.name === "string" ? ctx.options.name : undefined
    const configured = Boolean(url && key)

    const tunnel = configured ? new Tunnel({ url, key, name }) : null
    tunnel?.start()

    const registration = await ctx.rpc.register(OcRemoteRpc, {
      status: async () =>
        tunnel
          ? tunnel.status()
          : {
              configured: false,
              online: false,
              url: "",
              device: "",
              tenant: "",
              error: "run npx oc-remote join <url> <key>",
            },
      pair: async () => {
        if (!tunnel) throw new Error("oc-remote is not configured")
        return tunnel.mintAndRegister()
      },
      off: async () => {
        tunnel?.stop()
        return {}
      },
      on: async () => {
        tunnel?.start()
        return {}
      },
    })

    let controller: AbortController | null = null
    if (tunnel) {
      controller = new AbortController()
      void (async () => {
        try {
          for await (const event of ctx.event.subscribe({
            types: ["permission.asked"],
            signal: controller.signal,
          })) {
            tunnel.notifyPermission(event.data)
          }
        } catch {}
      })()
    }

    return async () => {
      controller?.abort()
      await registration.dispose()
      tunnel?.stop()
    }
  },
})
