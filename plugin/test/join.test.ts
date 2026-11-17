import assert from "node:assert/strict"
import { execFile } from "node:child_process"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { createServer, type Server } from "node:http"
import type { AddressInfo } from "node:net"
import { tmpdir } from "node:os"
import { join } from "node:path"
import test from "node:test"
import { promisify } from "node:util"

const run = promisify(execFile)

type Relay = { server: Server; url: string }

function mockRelay(): Promise<Relay> {
  return new Promise<Relay>((resolve) => {
    const server = createServer((req, res) => {
      if (req.url === "/_ocr/healthz") {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ ok: true, version: "test" }))
        return
      }
      if (req.url === "/_ocr/api/verify" && req.method === "POST") {
        if (req.headers.authorization !== "Bearer ocr_test") {
          res.writeHead(403)
          res.end("invalid key")
          return
        }
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ tenant: "alice", device: "alice-laptop" }))
        return
      }
      res.writeHead(404)
      res.end()
    })
    server.listen(0, "127.0.0.1", () => {
      const address = server.address() as AddressInfo | null
      if (!address) throw new Error("mock relay has no address")
      resolve({ server, url: `http://127.0.0.1:${address.port}` })
    })
  })
}

test("join writes the plugin entry, leave removes it", async () => {
  const relay = await mockRelay()
  const home = await mkdtemp(join(tmpdir(), "oc-remote-test-"))
  try {
    const env = { ...process.env, XDG_CONFIG_HOME: home }
    const bin = join(process.cwd(), "bin", "oc-remote.mjs")
    const joined = await run(process.execPath, [bin, "join", relay.url, "ocr_test", "--name", "laptop"], { env })
    assert.match(joined.stdout, /alice\/alice-laptop/)

    const configPath = join(home, "opencode", "opencode.json")
    const config = JSON.parse(await readFile(configPath, "utf8"))
    assert.equal(config.plugins.length, 1)
    assert.deepEqual(config.plugins[0], {
      package: "oc-remote",
      options: { url: relay.url, key: "ocr_test", name: "laptop" },
    })

    await run(process.execPath, [bin, "leave"], { env })
    const after = JSON.parse(await readFile(configPath, "utf8"))
    assert.deepEqual(after.plugins, [])
  } finally {
    relay.server.close()
    await rm(home, { recursive: true, force: true })
  }
})

test("join rejects a bad key and does not write the config", async () => {
  const relay = await mockRelay()
  const home = await mkdtemp(join(tmpdir(), "oc-remote-test-"))
  try {
    const env = { ...process.env, XDG_CONFIG_HOME: home }
    const bin = join(process.cwd(), "bin", "oc-remote.mjs")
    await assert.rejects(
      run(process.execPath, [bin, "join", relay.url, "ocr_bad"], { env }),
      /device key rejected/,
    )
    await assert.rejects(readFile(join(home, "opencode", "opencode.json"), "utf8"), /ENOENT/)
  } finally {
    relay.server.close()
    await rm(home, { recursive: true, force: true })
  }
})
