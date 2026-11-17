import http from "node:http"
import { mintPair, normalizeBase, readService } from "./local.js"

export type TunnelOptions = {
  url: string
  key: string
  name?: string
}

export type TunnelStatus = {
  configured: boolean
  online: boolean
  url: string
  device: string
  tenant: string
  error?: string
}

type Frame = {
  t: string
  id?: number
  device?: string
  tenant?: string
  version?: string
  method?: string
  path?: string
  headers?: [string, string][]
  auth?: string
  body?: string
  status?: number
  code?: string
  url?: string
  expires_in?: number
  kind?: string
  sessionID?: string
  requestID?: string
  action?: string
  error?: string
}

export type PermissionEvent = {
  id: string
  sessionID: string
  action: string
}

const VERSION = "0.1.0"

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

export class Tunnel {
  private ws: WebSocket | null = null
  private stopped = false
  private timer: ReturnType<typeof setTimeout> | null = null
  private retry = 1000
  private device = ""
  private tenant = ""
  private lastError = ""
  private base: string | null = null
  private readonly inflight = new Map<number, http.ClientRequest>()
  private readonly cancelled = new Set<number>()

  constructor(private readonly opts: TunnelOptions) {}

  start(): void {
    this.stopped = false
    if (!this.ws && !this.timer) this.connect()
  }

  stop(): void {
    this.stopped = true
    if (this.timer) {
      clearTimeout(this.timer)
      this.timer = null
    }
    this.close()
  }

  status(): TunnelStatus {
    return {
      configured: true,
      online: this.online,
      url: normalizeBase(this.opts.url),
      device: this.device,
      tenant: this.tenant,
      error: this.lastError || undefined,
    }
  }

  async mintAndRegister(): Promise<{ url: string; expiresAt: number }> {
    if (!this.online) throw new Error("tunnel is offline")
    const service = await readService()
    const { code, expiresIn } = await mintPair(service.url, service.password)
    const url = this.pairURL(code)
    this.send({ t: "pair.register", code, expires_in: expiresIn, url })
    return { url, expiresAt: Math.floor(Date.now() / 1000) + expiresIn }
  }

  notifyPermission(event: PermissionEvent): void {
    if (!this.online) return
    this.safeSend({
      t: "notify",
      kind: "permission",
      sessionID: event.sessionID,
      requestID: event.id,
      action: event.action,
    })
  }

  private get online(): boolean {
    return this.ws?.readyState === WebSocket.OPEN
  }

  private connect(): void {
    if (this.stopped) return
    const u = new URL(this.opts.url)
    u.protocol = u.protocol === "https:" ? "wss:" : "ws:"
    u.pathname = "/_ocr/tunnel"
    u.search = "?key=" + encodeURIComponent(this.opts.key)

    let ws: WebSocket
    try {
      ws = new WebSocket(u.toString())
    } catch (error) {
      this.lastError = message(error)
      this.schedule()
      return
    }
    this.ws = ws
    ws.onopen = () => {
      this.retry = 1000
      this.lastError = ""
    }
    ws.onmessage = (event) => {
      if (typeof event.data === "string") this.receive(event.data)
    }
    ws.onerror = () => {
      this.lastError = "websocket error"
    }
    ws.onclose = () => {
      if (this.ws === ws) this.ws = null
      this.schedule()
    }
  }

  private schedule(): void {
    if (this.stopped || this.timer) return
    const delay = Math.min(30000, this.retry) + Math.floor(Math.random() * 250)
    this.retry = Math.min(30000, this.retry * 2)
    this.timer = setTimeout(() => {
      this.timer = null
      this.connect()
    }, delay)
  }

  private close(): void {
    const ws = this.ws
    this.ws = null
    if (ws) {
      try {
        ws.close()
      } catch {}
    }
    for (const request of this.inflight.values()) request.destroy()
    this.inflight.clear()
    this.cancelled.clear()
  }

  private send(frame: Frame): void {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) throw new Error("tunnel is offline")
    ws.send(JSON.stringify(frame))
  }

  private safeSend(frame: Frame): void {
    try {
      this.send(frame)
    } catch {}
  }

  private receive(raw: string): void {
    let frame: Frame
    try {
      frame = JSON.parse(raw) as Frame
    } catch {
      return
    }
    switch (frame.t) {
      case "hello":
        this.device = frame.device ?? ""
        this.tenant = frame.tenant ?? ""
        this.safeSend({ t: "hello.ack", version: VERSION })
        break
      case "req":
        void this.request(frame)
        break
      case "pair.mint":
        void this.mint(frame)
        break
      case "abort":
        if (frame.id !== undefined) {
          this.cancelled.add(frame.id)
          this.inflight.get(frame.id)?.destroy()
          this.inflight.delete(frame.id)
        }
        break
    }
  }

  private async mint(frame: Frame): Promise<void> {
    if (frame.id === undefined) return
    try {
      const service = await readService()
      const { code, expiresIn } = await mintPair(service.url, service.password)
      this.safeSend({
        t: "pair.minted",
        id: frame.id,
        code,
        expires_in: expiresIn,
        url: this.pairURL(code),
      })
    } catch (error) {
      this.safeSend({ t: "res.error", id: frame.id, error: message(error) })
    }
  }

  private pairURL(code: string): string {
    return normalizeBase(this.opts.url) + "/_ocr/pair/" + encodeURIComponent(code)
  }

  private async baseURL(): Promise<string> {
    if (this.base) return this.base
    const service = await readService()
    this.base = service.url
    return this.base
  }

  private async request(frame: Frame): Promise<void> {
    const id = frame.id
    if (id === undefined) return

    let base: string
    try {
      base = await this.baseURL()
    } catch (error) {
      this.fail(id, error)
      return
    }

    const target = new URL(base)
    const headers: Record<string, string | string[]> = {}
    for (const [name, value] of frame.headers ?? []) {
      const lower = name.toLowerCase()
      if (lower === "host" || lower === "authorization" || lower === "content-length" || lower === "connection") continue
      const current = headers[name]
      if (current === undefined) headers[name] = value
      else if (Array.isArray(current)) current.push(value)
      else headers[name] = [current, value]
    }
    if (frame.auth) headers["authorization"] = frame.auth
    const body = frame.body ? Buffer.from(frame.body, "base64") : undefined
    headers["content-length"] = String(body?.length ?? 0)

    const request = http.request(
      {
        hostname: target.hostname,
        port: target.port ? Number(target.port) : 80,
        path: frame.path ?? "/",
        method: frame.method ?? "GET",
        headers,
      },
      (response) => {
        const out: [string, string][] = []
        for (const [name, value] of Object.entries(response.headers)) {
          if (value === undefined) continue
          if (Array.isArray(value)) for (const item of value) out.push([name, item])
          else out.push([name, value])
        }
        this.safeSend({ t: "res.start", id, status: response.statusCode ?? 502, headers: out })
        response.on("data", (chunk: Buffer) => {
          this.safeSend({ t: "res.chunk", id, body: chunk.toString("base64") })
        })
        response.on("end", () => {
          this.safeSend({ t: "res.end", id })
        })
        response.on("error", (error) => {
          if (!this.cancelled.has(id)) this.fail(id, error)
        })
      },
    )

    this.inflight.set(id, request)
    request.on("error", (error) => {
      this.base = null
      if (!this.cancelled.has(id)) this.fail(id, error)
    })
    request.on("close", () => {
      this.inflight.delete(id)
      this.cancelled.delete(id)
    })
    request.end(body)
  }

  private fail(id: number, error: unknown): void {
    this.safeSend({ t: "res.error", id, error: message(error) })
  }
}
