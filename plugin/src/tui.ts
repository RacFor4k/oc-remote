import { spawn } from "node:child_process"
import { Plugin } from "@opencode/plugin/tui"
import { OcRemoteRpc, type OcRemotePair, type OcRemoteStatus } from "./rpc.js"

function copyLink(text: string): Promise<boolean> {
  return new Promise((resolve) => {
    const command =
      process.platform === "win32" ? "clip.exe" : process.platform === "darwin" ? "pbcopy" : "wl-copy"
    let child
    try {
      child = spawn(command, [], { stdio: ["pipe", "ignore", "ignore"] })
    } catch {
      resolve(false)
      return
    }
    child.on("error", () => resolve(false))
    child.on("close", (code) => resolve(code === 0))
    child.stdin?.end(text)
  })
}

export default Plugin.define({
  id: "oc-remote.tui",
  setup(context) {
    const rpc = context.client.rpc(OcRemoteRpc)

    context.keymap.layer(() => ({
      mode: "global",
      commands: [
        {
          id: "oc-remote.show",
          title: "Remote access",
          description: "Show the oc-remote link and tunnel status",
          group: "Remote",
          palette: true,
          slash: { name: "remote", aliases: ["rc"], arguments: true },
          run: async (input) => {
            const arg = (input ?? "").trim().toLowerCase()
            try {
              if (arg === "off") {
                await rpc.off({})
                context.ui.toast.show({ message: "Remote access off", variant: "info" })
                return
              }
              if (arg === "on") {
                await rpc.on({})
                context.ui.toast.show({ message: "Remote access on", variant: "info" })
                return
              }

              const status = (await rpc.status({})) as OcRemoteStatus
              if (!status.configured) {
                await context.ui.dialog.alert({
                  title: "oc-remote",
                  message: "Not configured. Run on this machine:\n\nnpx oc-remote join <url> <key>",
                })
                return
              }
              if (!status.online) {
                await context.ui.dialog.alert({
                  title: "oc-remote",
                  message: `Offline: ${status.error || "tunnel is not connected"}\n\nRun /remote on to reconnect.`,
                })
                return
              }

              const pair = (await rpc.pair({})) as OcRemotePair
              const copied = await copyLink(pair.url)
              await context.ui.dialog.alert({
                title: "oc-remote",
                message: `${status.tenant} / ${status.device}\n\n${
                  copied ? "Link copied to clipboard.\n\n" : ""
                }${pair.url}\n\nSingle use, expires in 5 minutes.`,
              })
            } catch (error) {
              context.ui.toast.show({
                message: error instanceof Error ? error.message : String(error),
                variant: "error",
              })
            }
          },
        },
      ],
    }))
  },
})
