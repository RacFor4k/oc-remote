import assert from "node:assert/strict"
import { join } from "node:path"
import test from "node:test"
import { basicAuth, normalizeBase, serviceFile } from "../src/local.ts"

test("normalizeBase strips trailing slashes", () => {
  assert.equal(normalizeBase("https://rc.example/"), "https://rc.example")
  assert.equal(normalizeBase("https://rc.example///"), "https://rc.example")
})

test("basicAuth uses the opencode user", () => {
  assert.equal(basicAuth("secret"), "Basic " + Buffer.from("opencode:secret").toString("base64"))
})

test("serviceFile respects XDG_STATE_HOME", () => {
  const previous = process.env.XDG_STATE_HOME
  process.env.XDG_STATE_HOME = join("tmp", "state")
  try {
    assert.equal(serviceFile(), join("tmp", "state", "opencode", "service.json"))
  } finally {
    if (previous === undefined) delete process.env.XDG_STATE_HOME
    else process.env.XDG_STATE_HOME = previous
  }
})
