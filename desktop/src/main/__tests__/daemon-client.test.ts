// @vitest-environment node
import { describe, expect, it } from "vitest"
import { daemonSocketName } from "../daemon-client.js"

describe("daemonSocketName", () => {
  it("keeps the historical name for the default home", () => {
    expect(daemonSocketName("")).toBe("devsy-local.sock")
  })

  it.skipIf(process.platform === "win32")(
    "matches the CLI's name for a DEVSY_HOME override",
    () => {
      // Keep in sync with pkg/daemon/local/socket_name_test.go.
      expect(daemonSocketName("/home/user/.silicon-harbor")).toBe(
        "devsy-local-c0ea3dbb9f45.sock",
      )
    },
  )

  it("treats equivalent paths as the same home", () => {
    expect(daemonSocketName("/home/user/.silicon-harbor/")).toBe(
      daemonSocketName("/home/user/.silicon-harbor/sub/.."),
    )
  })

  it("gives different homes different daemons", () => {
    expect(daemonSocketName("/home/a/.devsy")).not.toBe(
      daemonSocketName("/home/b/.devsy"),
    )
  })
})
