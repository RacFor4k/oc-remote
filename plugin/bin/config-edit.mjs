import { applyEdits, findNodeAtLocation, format, getNodeValue, modify, parseTree } from "jsonc-parser"

export const SCHEMA = "https://opencode.ai/config.json"

function options(text) {
  return {
    formattingOptions: {
      insertSpaces: true,
      tabSize: 2,
      eol: text.includes("\r\n") ? "\r\n" : "\n",
    },
  }
}

function clean(text) {
  return text.replace(/^\uFEFF/, "")
}

function matches(entry, pkg, url) {
  if (typeof entry === "string") return entry === pkg || entry.startsWith(pkg + "@")
  if (entry && typeof entry === "object") {
    if (typeof entry.package === "string" && entry.package === pkg) return true
    if (url && entry.options && entry.options.url === url) return true
  }
  return false
}

export function upsertPlugin(text, pkg, entry) {
  const source = clean(text)
  if (!source.trim()) {
    return JSON.stringify({ $schema: SCHEMA, plugins: [entry] }, null, 2) + "\n"
  }
  const tree = parseTree(source, [], { allowTrailingComma: true })
  if (!tree) throw new Error("opencode config is not valid JSON")

  const node = findNodeAtLocation(tree, ["plugins"])
  const plugins = node ? getNodeValue(node) : undefined
  const opts = options(source)
  const url = entry && typeof entry === "object" && entry.options ? entry.options.url : undefined
  let edits

  if (Array.isArray(plugins)) {
    const index = plugins.findIndex((item) => matches(item, pkg, url))
    const target = index >= 0 ? index : plugins.length
    edits = modify(source, ["plugins", target], entry, opts)
  } else {
    edits = modify(source, ["plugins"], [entry], opts)
  }
  const edited = applyEdits(source, edits)
  return applyEdits(edited, format(edited, undefined, opts.formattingOptions))
}

export function removePlugin(text, pkg) {
  const source = clean(text)
  const tree = parseTree(source, [], { allowTrailingComma: true })
  if (!tree) throw new Error("opencode config is not valid JSON")

  const node = findNodeAtLocation(tree, ["plugins"])
  const plugins = node ? getNodeValue(node) : undefined
  if (!Array.isArray(plugins)) return source

  const index = plugins.findIndex((item) => matches(item, pkg))
  if (index < 0) return source
  const edited = applyEdits(source, modify(source, ["plugins", index], undefined, options(source)))
  return applyEdits(edited, format(edited, undefined, options(source).formattingOptions))
}
