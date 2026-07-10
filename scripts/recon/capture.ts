/**
 * Standalone capture harness — spawn the real Claude Code CLI through a local
 * proxy and dump the exact POST /v1/messages request it emits (all headers,
 * betas, body) to a JSON file. Unlike intercept-claude.ts this has NO build
 * artifact imports (no ../dist/*), so it runs directly against a downloaded CLI
 * binary for reverse-engineering a fresh version.
 *
 * Run (node >= 22, --experimental-strip-types):
 *   CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-... \
 *   CLAUDE_BIN=/tmp/cc-cli-2.1.206/package/claude \
 *   node --experimental-strip-types scripts/recon/capture.ts \
 *     --model claude-fable-5 --output testdata/captures/v2.1.206-fable5.json
 *
 * Security: forwards a real OAuth token to api.anthropic.com over HTTPS; the
 * localhost leg is plaintext. Dev-only. The written JSON is UNSANITIZED — strip
 * secrets before committing.
 */

import { createServer, type IncomingMessage, type ServerResponse } from "node:http"
import { request as httpsRequest } from "node:https"
import { spawn } from "node:child_process"
import { writeFileSync, mkdirSync } from "node:fs"
import { dirname } from "node:path"

const PORT = Number(process.env.CAPTURE_PORT ?? 18899)
const TIMEOUT_MS = Number(process.env.CAPTURE_TIMEOUT_MS ?? 45_000)
const CLAUDE_BIN = process.env.CLAUDE_BIN ?? "claude"

function arg(name: string, fallback: string): string {
  const i = process.argv.indexOf(name)
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback
}

const model = arg("--model", "claude-sonnet-4-6")
const output = arg("--output", `testdata/captures/capture-${model}.json`)
const prompt = arg("--prompt", "say hi in one word")

interface Captured {
  capturedAt: string
  cliBin: string
  model: string
  method: string
  path: string
  headers: Record<string, string>
  betas: string[]
  userAgent: string
  billingHeader: string
  body: unknown
  upstreamStatus: number | null
}

function collectHeaders(req: IncomingMessage): Record<string, string> {
  const h: Record<string, string> = {}
  for (const [k, v] of Object.entries(req.headers)) {
    if (typeof v === "string") h[k] = v
    else if (Array.isArray(v)) h[k] = v.join(", ")
  }
  return h
}

// The billing header historically rode inside system[] as a text block starting
// "x-anthropic-billing-header", not as a real HTTP header — check both places.
function findBillingHeader(headers: Record<string, string>, body: unknown): string {
  const sys = (body as { system?: unknown })?.system
  if (Array.isArray(sys)) {
    for (const entry of sys) {
      const text =
        typeof entry === "string"
          ? entry
          : entry && typeof entry === "object" && "text" in entry && typeof (entry as { text: unknown }).text === "string"
            ? (entry as { text: string }).text
            : ""
      if (text.startsWith("x-anthropic-billing-header")) return text
    }
  }
  return headers["x-anthropic-billing-header"] ?? ""
}

function capture(): Promise<Captured | null> {
  return new Promise((resolve) => {
    let done = false
    const finish = (r: Captured | null) => {
      if (done) return
      done = true
      server.close()
      resolve(r)
    }
    const timer = setTimeout(() => {
      console.error(`timeout after ${TIMEOUT_MS / 1000}s`)
      finish(null)
    }, TIMEOUT_MS)

    const server = createServer((req: IncomingMessage, res: ServerResponse) => {
      const chunks: Buffer[] = []
      req.on("data", (c: Buffer) => chunks.push(c))
      req.on("end", () => {
        const raw = Buffer.concat(chunks).toString()
        const headers = collectHeaders(req)
        let body: unknown = raw
        try {
          body = JSON.parse(raw)
        } catch {
          /* non-JSON (e.g. count_tokens ping) — keep raw */
        }
        const isMessages = req.method === "POST" && (req.url ?? "").startsWith("/v1/messages")

        // When OAUTH_TOKEN is set, swap the CLI's dummy x-api-key for a real
        // Bearer OAuth token so the upstream returns 200 (captures a full turn).
        const upstreamHeaders: Record<string, string | string[] | undefined> = {
          ...req.headers,
          host: "api.anthropic.com",
        }
        const oauthToken = process.env.OAUTH_TOKEN
        if (oauthToken) {
          delete upstreamHeaders["x-api-key"]
          upstreamHeaders["authorization"] = `Bearer ${oauthToken}`
          const existing = String(upstreamHeaders["anthropic-beta"] ?? "")
          if (!existing.includes("oauth-2025-04-20")) {
            upstreamHeaders["anthropic-beta"] = existing ? `oauth-2025-04-20,${existing}` : "oauth-2025-04-20"
          }
        }
        // The CLI fires a title-generation sidecar (entrypoint sdk-cli) before
        // the real turn; forward it but do not treat it as the capture target.
        const isSidecar = raw.includes("cc_entrypoint=sdk-cli") || raw.includes("Generate a concise")
        const isCaptureTarget = isMessages && !isSidecar

        const proxy = httpsRequest(
          {
            hostname: "api.anthropic.com",
            path: req.url,
            method: req.method,
            headers: upstreamHeaders,
          },
          (pr) => {
            res.writeHead(pr.statusCode ?? 502, pr.headers)
            pr.pipe(res)
            pr.on("end", () => {
              if (isCaptureTarget) {
                const betaHeader = headers["anthropic-beta"] ?? ""
                finish({
                  capturedAt: new Date().toISOString(),
                  cliBin: CLAUDE_BIN,
                  model,
                  method: req.method ?? "",
                  path: req.url ?? "",
                  headers,
                  betas: betaHeader.split(",").map((s) => s.trim()).filter(Boolean),
                  userAgent: headers["user-agent"] ?? "",
                  billingHeader: findBillingHeader(headers, body),
                  body,
                  upstreamStatus: pr.statusCode ?? null,
                })
              }
            })
          },
        )
        proxy.on("error", (e) => {
          console.error(`proxy error: ${e.message}`)
          res.writeHead(502)
          res.end("proxy error")
          finish(null)
        })
        proxy.write(raw)
        proxy.end()
      })
    })

    server.on("error", (e) => {
      console.error(`server error: ${e.message}`)
      finish(null)
    })

    server.listen(PORT, () => {
      console.error(`proxy listening :${PORT} → spawning ${CLAUDE_BIN} --model ${model}`)
      const child = spawn(CLAUDE_BIN, ["-p", prompt, "--model", model], {
        env: { ...process.env, ANTHROPIC_BASE_URL: `http://localhost:${PORT}`, TERM: "dumb" },
        stdio: "ignore",
      })
      child.on("error", (e) => {
        console.error(`claude spawn error: ${e.message}`)
        finish(null)
      })
      child.on("close", () => setTimeout(() => finish(null), 4000))
    })
  })
}

const result = await capture()
if (!result) {
  console.error("capture FAILED (no /v1/messages seen)")
  process.exit(1)
}
mkdirSync(dirname(output), { recursive: true })
writeFileSync(output, JSON.stringify(result, null, 2))
console.error(`captured → ${output}`)
console.error(`  status=${result.upstreamStatus} betas=${result.betas.length} ua=${result.userAgent.slice(0, 40)} billing=${result.billingHeader ? "PRESENT" : "ABSENT"}`)
