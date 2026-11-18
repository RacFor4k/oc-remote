import { Plugin } from "@opencode/plugin"
import { OcRemoteRpc } from "./rpc.js"
import { Tunnel } from "./tunnel.js"

type Shared = {
  tunnel: Tunnel
  refs: number
}

type Global = typeof globalThis & {
  __ocRemoteShared?: Shared
  __ocRemoteSeen?: Set<string>
}

const global = globalThis as Global

function acquire(url: string, key: string, name?: string): Shared {
  if (!global.__ocRemoteShared) {
    const tunnel = new Tunnel({ url, key, name })
    tunnel.start()
    global.__ocRemoteShared = { tunnel, refs: 0 }
  }
  const state = global.__ocRemoteShared
  state.refs++
  return state
}

function release(state: Shared): void {
  state.refs--
  if (state.refs > 0) return
  state.tunnel.stop()
  if (global.__ocRemoteShared === state) delete global.__ocRemoteShared
}

function remember(id: string): boolean {
  const seen = (global.__ocRemoteSeen ??= new Set<string>())
  if (seen.has(id)) return false
  seen.add(id)
  if (seen.size > 512) {
    const first = seen.values().next().value
    if (first !== undefined) seen.delete(first)
  }
  return true
}

export default Plugin.define({
  id: "oc-remote",
  async setup(ctx) {
    const url = typeof ctx.options.url === "string" ? ctx.options.url.trim() : ""
    const key = typeof ctx.options.key === "string" ? ctx.options.key.trim() : ""
    const name = typeof ctx.options.name === "string" ? ctx.options.name : undefined
    const state = url && key ? acquire(url, key, name) : null

    const registration = await ctx.rpc.register(OcRemoteRpc, {
      status: async () =>
        state
          ? state.tunnel.status()
          : {
              configured: false,
              online: false,
              url: "",
              device: "",
              tenant: "",
              error: "run npx @racfor/oc-remote join <url> <key>",
            },
      pair: async () => {
        if (!state) throw new Error("oc-remote is not configured")
        return state.tunnel.mintAndRegister()
      },
      off: async () => {
        state?.tunnel.stop()
        return {}
      },
      on: async () => {
        state?.tunnel.start()
        return {}
      },
    })

    const controller = state ? new AbortController() : null
    if (state && controller) {
      void (async () => {
        try {
          for await (const event of ctx.event.subscribe({
            types: ["permission.asked"],
            signal: controller.signal,
          })) {
            if (remember(event.id)) state.tunnel.notifyPermission(event.data)
          }
        } catch {}
      })()
    }

    return async () => {
      controller?.abort()
      await registration.dispose()
      if (state) release(state)
    }
  },
})
