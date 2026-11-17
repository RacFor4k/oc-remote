import assert from "node:assert/strict"
import test from "node:test"
import { parse } from "jsonc-parser"
import { removePlugin, upsertPlugin } from "../bin/config-edit.mjs"

const entry = { package: "oc-remote", options: { url: "https://rc.example", key: "ocr_key" } }

test("creates a config from an empty file", () => {
  const out = upsertPlugin("", "oc-remote", entry)
  const json = parse(out)
  assert.equal(json.$schema, "https://opencode.ai/config.json")
  assert.deepEqual(json.plugins, [entry])
})

test("appends to an existing config and keeps unrelated settings", () => {
  const src = `{\n  "plugins": ["@acme/a", { "package": "@acme/b", "options": { "x": 1 } }],\n  "model": "acme/model"\n}\n`
  const out = upsertPlugin(src, "oc-remote", entry)
  const json = parse(out)
  assert.equal(json.model, "acme/model")
  assert.deepEqual(json.plugins.slice(0, 2), ["@acme/a", { package: "@acme/b", options: { x: 1 } }])
  assert.deepEqual(json.plugins[2], entry)
})

test("replaces an existing entry", () => {
  const src = `{"plugins":[{"package":"oc-remote","options":{"url":"old","key":"old"}}]}`
  const out = upsertPlugin(src, "oc-remote", entry)
  const json = parse(out)
  assert.equal(json.plugins.length, 1)
  assert.deepEqual(json.plugins[0], entry)
})

test("replaces a versioned string entry", () => {
  const src = `{"plugins":["oc-remote@0.1.0"]}`
  const out = upsertPlugin(src, "oc-remote", entry)
  const json = parse(out)
  assert.equal(json.plugins.length, 1)
  assert.deepEqual(json.plugins[0], entry)
})

test("keeps comments and formatting", () => {
  const src = `{\n  // keep this comment\n  "plugins": []\n}\n`
  const out = upsertPlugin(src, "oc-remote", entry)
  assert.ok(out.includes("// keep this comment"))
  assert.deepEqual(parse(out).plugins, [entry])
})

test("replaces a file package with the same relay url", () => {
  const src = `{"plugins":[{"package":"file:///D:/Source/oc-remote/plugin","options":{"url":"https://rc.example","key":"old"}}]}`
  const out = upsertPlugin(src, "oc-remote", { package: "oc-remote", options: { url: "https://rc.example", key: "new" } })
  const json = parse(out)
  assert.equal(json.plugins.length, 1)
  assert.equal(json.plugins[0].package, "oc-remote")
  assert.equal(json.plugins[0].options.key, "new")
})

test("removes the entry and leaves the rest", () => {
  const src = `{"plugins":["@acme/a",{"package":"oc-remote","options":{}}]}`
  const out = removePlugin(src, "oc-remote")
  assert.deepEqual(parse(out).plugins, ["@acme/a"])
})

test("removes nothing when the package is absent", () => {
  const src = `{"plugins":["@acme/a"]}`
  assert.equal(removePlugin(src, "oc-remote"), src)
})
