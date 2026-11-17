import { Rpc } from "@opencode/plugin/rpc"

const object = (properties: Record<string, unknown>, required: string[] = []) =>
  ({ type: "object", properties, required, additionalProperties: false }) as const

const noInput = object({})

export const OcRemoteRpc = Rpc.define({
  id: "oc-remote",
  methods: {
    status: {
      input: noInput,
      output: object(
        {
          configured: { type: "boolean" },
          online: { type: "boolean" },
          url: { type: "string" },
          device: { type: "string" },
          tenant: { type: "string" },
          error: { type: "string" },
        },
        ["configured", "online", "url", "device", "tenant"],
      ),
    },
    pair: {
      input: noInput,
      output: object({ url: { type: "string" }, expiresAt: { type: "number" } }, ["url", "expiresAt"]),
    },
    off: {
      input: noInput,
      output: object({}),
    },
    on: {
      input: noInput,
      output: object({}),
    },
  },
  events: {},
})

export type OcRemoteStatus = {
  configured: boolean
  online: boolean
  url: string
  device: string
  tenant: string
  error?: string
}

export type OcRemotePair = {
  url: string
  expiresAt: number
}
