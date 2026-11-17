import { readFile } from "node:fs/promises"
import { homedir } from "node:os"
import { join } from "node:path"

export type ServiceInfo = {
  url: string
  password: string
}

export function serviceFile(): string {
  const state = process.env.XDG_STATE_HOME || join(homedir(), ".local", "state")
  return join(state, "opencode", "service.json")
}

export function normalizeBase(url: string): string {
  return url.replace(/\/+$/, "")
}

export function basicAuth(password: string): string {
  return "Basic " + Buffer.from("opencode:" + password).toString("base64")
}

export async function readService(): Promise<ServiceInfo> {
  const raw = await readFile(serviceFile(), "utf8")
  const value = JSON.parse(raw) as { url?: unknown; password?: unknown }
  if (typeof value.url !== "string" || typeof value.password !== "string") {
    throw new Error("service.json: url or password missing")
  }
  return { url: normalizeBase(value.url), password: value.password }
}

export async function mintPair(base: string, password: string): Promise<{ code: string; expiresIn: number }> {
  const res = await fetch(normalizeBase(base) + "/api/pair", {
    method: "POST",
    headers: { authorization: basicAuth(password) },
  })
  if (!res.ok) {
    throw new Error(`pair mint failed: ${res.status} ${res.statusText}`)
  }
  const value = (await res.json()) as { code?: unknown; expires_in?: unknown }
  if (typeof value.code !== "string" || typeof value.expires_in !== "number") {
    throw new Error("pair mint failed: unexpected response")
  }
  return { code: value.code, expiresIn: value.expires_in }
}
