import { createHash } from "node:crypto"
import http from "node:http"
import { resolve } from "node:path"

const SOCKET_SUFFIX = "devsy-local.sock"
const SOCKET_HASH_LEN = 12
const REQUEST_TIMEOUT = 3000

/**
 * Mirrors socketName() in pkg/daemon/local/socket_name.go: the default home
 * keeps the historical name, a DEVSY_HOME override gets its own daemon keyed
 * by sha256 over the absolute home path.
 */
export function daemonSocketName(home = process.env.DEVSY_HOME): string {
  if (!home) return SOCKET_SUFFIX
  const hash = createHash("sha256")
    .update(resolve(home))
    .digest("hex")
    .slice(0, SOCKET_HASH_LEN)
  return `devsy-local-${hash}.sock`
}

function getDefaultSocketPath(): string {
  if (process.platform === "win32") {
    return `\\\\.\\pipe\\${daemonSocketName()}`
  }
  return `/tmp/${daemonSocketName()}`
}

export class DaemonClient {
  private socketPath: string

  constructor(socketPath = getDefaultSocketPath()) {
    this.socketPath = socketPath
  }

  async health(): Promise<boolean> {
    try {
      await this.get("/health")
      return true
    } catch {
      return false
    }
  }

  async listWorkspaces<T>(options: { fresh?: boolean } = {}): Promise<T> {
    return this.get<T>(options.fresh ? "/list?fresh=true" : "/list")
  }

  async listProviders<T>(): Promise<T> {
    return this.get<T>("/provider/list")
  }

  async listMachines<T>(): Promise<T> {
    return this.get<T>("/machine/list")
  }

  async listContexts<T>(): Promise<T> {
    return this.get<T>("/context/list")
  }

  private get<T>(path: string): Promise<T> {
    return new Promise((resolve, reject) => {
      const req = http.get(
        { socketPath: this.socketPath, path, timeout: REQUEST_TIMEOUT },
        (res) => {
          const chunks: Buffer[] = []
          res.on("data", (chunk: Buffer) => chunks.push(chunk))
          res.on("end", () => {
            const body = Buffer.concat(chunks).toString()
            if (res.statusCode !== 200) {
              reject(new Error(`daemon ${path}: ${res.statusCode} ${body}`))
              return
            }
            try {
              resolve(JSON.parse(body) as T)
            } catch (e) {
              reject(e)
            }
          })
        },
      )
      req.on("error", reject)
      req.on("timeout", () => {
        req.destroy()
        reject(new Error(`daemon ${path}: timeout`))
      })
    })
  }
}
