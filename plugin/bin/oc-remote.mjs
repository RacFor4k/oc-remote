#!/usr/bin/env node
import { constants } from "node:fs"
import { access, copyFile, mkdir, readFile, writeFile } from "node:fs/promises"
import { homedir } from "node:os"
import { dirname, join } from "node:path"
import { removePlugin, upsertPlugin } from "./config-edit.mjs"

const DEFAULT_PACKAGE = "oc-remote"

async function main() {
  const args = process.argv.slice(2)
  const command = args.shift()
  if (!command || command === "help" || command === "--help" || command === "-h") {
    help()
    return
  }
  if (command === "join") {
    await joinCommand(args)
    return
  }
  if (command === "leave") {
    await leaveCommand(args)
    return
  }
  throw new Error(`unknown command: ${command}`)
}

function parseFlags(args) {
  const out = { positional: [], name: "", pkg: DEFAULT_PACKAGE, help: false }
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]
    if (arg === "--name" || arg === "-n") {
      out.name = args[++i] ?? ""
      continue
    }
    if (arg.startsWith("--name=")) {
      out.name = arg.slice("--name=".length)
      continue
    }
    if (arg === "--package" || arg === "-p") {
      out.pkg = args[++i] ?? ""
      continue
    }
    if (arg.startsWith("--package=")) {
      out.pkg = arg.slice("--package=".length)
      continue
    }
    if (arg === "--help" || arg === "-h") {
      out.help = true
      continue
    }
    if (arg.startsWith("-")) throw new Error(`unknown flag: ${arg}`)
    out.positional.push(arg)
  }
  return out
}

function normalizeUrl(raw) {
  const url = new URL(raw)
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new Error("url must start with https:// or http://")
  }
  url.pathname = "/"
  url.search = ""
  url.hash = ""
  return url.origin
}

async function joinCommand(args) {
  const flags = parseFlags(args)
  if (flags.help) {
    help()
    return
  }
  if (!flags.pkg) throw new Error("--package needs a value")
  const [rawUrl, key] = flags.positional
  if (!rawUrl || !key) {
    throw new Error("usage: oc-remote join <url> <key> [--name <device>] [--package <pkg>]")
  }
  const url = normalizeUrl(rawUrl)
  await health(url)
  const verified = await verify(url, key)
  const config = await configPath()
  const text = await readText(config)
  const entry = {
    package: flags.pkg,
    options: { url, key, ...(flags.name ? { name: flags.name } : {}) },
  }
  await backup(config)
  await mkdir(dirname(config), { recursive: true })
  await writeFile(config, upsertPlugin(text, flags.pkg, entry), "utf8")
  console.log(`Configured ${verified.tenant}/${verified.device} at ${url}`)
  console.log(`Plugin entry: ${config}`)
  console.log("Run /remote in the OpenCode TUI.")
}

async function leaveCommand(args) {
  const flags = parseFlags(args)
  if (flags.help) {
    help()
    return
  }
  const config = await configPath()
  const text = await readText(config)
  if (!text) {
    console.log("No OpenCode config found.")
    return
  }
  await backup(config)
  await mkdir(dirname(config), { recursive: true })
  await writeFile(config, removePlugin(text, flags.pkg), "utf8")
  console.log(`Removed ${flags.pkg} from ${config}`)
}

async function health(url) {
  const res = await fetch(url + "/_ocr/healthz", { signal: AbortSignal.timeout(10000) })
  if (!res.ok) throw new Error(`relay health failed: ${res.status} ${res.statusText}`)
  const body = await res.json().catch(() => ({}))
  if (!body || body.ok !== true) throw new Error("relay health returned an unexpected response")
}

async function verify(url, key) {
  const res = await fetch(url + "/_ocr/api/verify", {
    method: "POST",
    headers: { authorization: "Bearer " + key },
    signal: AbortSignal.timeout(10000),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => "")
    throw new Error(`device key rejected: ${res.status} ${text}`.trim())
  }
  const body = await res.json()
  if (typeof body?.tenant !== "string" || typeof body?.device !== "string") {
    throw new Error("relay verify returned an unexpected response")
  }
  return { tenant: body.tenant, device: body.device }
}

async function configPath() {
  const base = process.env.XDG_CONFIG_HOME || join(homedir(), ".config")
  const dir = join(base, "opencode")
  const json = join(dir, "opencode.json")
  const jsonc = join(dir, "opencode.jsonc")
  if (await exists(json)) return json
  if (await exists(jsonc)) return jsonc
  return json
}

async function readText(path) {
  try {
    return await readFile(path, "utf8")
  } catch (error) {
    if (error && error.code === "ENOENT") return ""
    throw error
  }
}

async function backup(path) {
  if (!(await exists(path))) return
  const backupPath = path + ".bak"
  if (await exists(backupPath)) return
  await copyFile(path, backupPath)
}

async function exists(path) {
  try {
    await access(path, constants.F_OK)
    return true
  } catch {
    return false
  }
}

function help() {
  console.log(`oc-remote — remote access for a local OpenCode v2 server

Usage:
  oc-remote join <url> <key> [--name <device>] [--package <pkg>]
  oc-remote leave [--package <pkg>]

Examples:
  oc-remote join https://rc.addies.ai ocr_...
  oc-remote join http://127.0.0.1:4097 ocr_... --package file:///D:/Source/oc-remote/plugin

The join command writes the plugin and its options into the global opencode.json
(comments and unrelated settings are preserved) and keeps a .bak copy.`)
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error))
  process.exit(1)
})
