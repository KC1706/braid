import type { ConflictHunk } from "./conflict.js";

export interface ResolveInput {
  path: string;
  hunk: ConflictHunk;
  /** Intent registered by the agent whose branch is "ours" in this merge. */
  oursIntent: string;
  /** Intent registered by the agent whose branch is "theirs". */
  theirsIntent: string;
  /** Retry counter; >0 means a previous resolution broke the tests. */
  attempt: number;
  /** Test failure text from the previous attempt, fed back for a better retry. */
  lastFailure?: string;
}

export interface ResolveOutput {
  /** The text that replaces the entire conflict region. */
  content: string;
  /** Human-readable "why" for the provenance trail. */
  rationale: string;
  /** Which path produced this: "union" (deterministic) or "llm". */
  strategy: "union" | "llm";
}

export interface Resolver {
  readonly name: string;
  resolve(input: ResolveInput): Promise<ResolveOutput>;
}

/** Lines one side added relative to the common ancestor, order preserved. */
function added(side: string[], base: string[]): string[] {
  const baseCount = new Map<string, number>();
  for (const l of base) baseCount.set(l, (baseCount.get(l) ?? 0) + 1);
  const out: string[] = [];
  for (const l of side) {
    const n = baseCount.get(l) ?? 0;
    if (n > 0) baseCount.set(l, n - 1);
    else out.push(l);
  }
  return out;
}

/**
 * Deterministic 3-way union. For a hunk where each agent inserted distinct
 * lines around a shared base region, keep BOTH agents' additions (in agent
 * order) followed by the preserved base lines. This resolves the canonical
 * "two agents added a guard/call before the same statement" collision with no
 * LLM, no network, and no nondeterminism — the Spork/Mergiraf philosophy.
 */
export class UnionResolver implements Resolver {
  readonly name = "union";

  async resolve(input: ResolveInput): Promise<ResolveOutput> {
    const { hunk, oursIntent, theirsIntent } = input;
    const oursAdded = added(hunk.ours, hunk.base);
    const theirsAdded = added(hunk.theirs, hunk.base);
    const content = [...oursAdded, ...theirsAdded, ...hunk.base].join("\n");
    return {
      content,
      strategy: "union",
      rationale:
        `kept both; applied "${oursIntent}" then "${theirsIntent}" around the shared line(s). ` +
        `independent concerns, so both additions survive.`,
    };
  }
}

/**
 * Resolves a true conflict hunk with Claude (via Cloudflare AI Gateway when
 * CF_AI_GATEWAY is set, else the Anthropic API directly). Returns only the
 * replacement text; correctness is still decided downstream by re-running the
 * real test suite — the LLM never gets the final word.
 */
export class ClaudeResolver implements Resolver {
  readonly name = "claude";
  private apiKey: string;
  private model: string;
  private endpoint: string;

  constructor(opts?: { apiKey?: string; model?: string; gatewayBaseUrl?: string }) {
    this.apiKey = opts?.apiKey ?? process.env.ANTHROPIC_API_KEY ?? "";
    this.model = opts?.model ?? process.env.BRAID_MODEL ?? "claude-opus-4-8";
    const gw = opts?.gatewayBaseUrl ?? process.env.CF_AI_GATEWAY ?? "";
    this.endpoint = gw
      ? `${gw.replace(/\/$/, "")}/v1/messages`
      : "https://api.anthropic.com/v1/messages";
  }

  get available(): boolean {
    return this.apiKey.length > 0;
  }

  async resolve(input: ResolveInput): Promise<ResolveOutput> {
    if (!this.available) {
      throw new Error("ClaudeResolver: no ANTHROPIC_API_KEY configured");
    }
    const { hunk, path, oursIntent, theirsIntent, attempt, lastFailure } = input;
    const prompt = [
      `You are resolving a git merge conflict in \`${path}\`.`,
      `Two agents edited the same region of one codebase:`,
      `- "ours" (${hunk.oursLabel}) implements: ${oursIntent}`,
      `- "theirs" (${hunk.theirsLabel}) implements: ${theirsIntent}`,
      ``,
      `Common ancestor (base):`,
      "```\n" + hunk.base.join("\n") + "\n```",
      `Ours:`,
      "```\n" + hunk.ours.join("\n") + "\n```",
      `Theirs:`,
      "```\n" + hunk.theirs.join("\n") + "\n```",
      `Context immediately before:`,
      "```\n" + hunk.contextBefore.join("\n") + "\n```",
      `Context immediately after:`,
      "```\n" + hunk.contextAfter.join("\n") + "\n```",
      attempt > 0 && lastFailure
        ? `\nYour previous resolution FAILED the test suite:\n${lastFailure}\nFix it.`
        : "",
      ``,
      `Compose BOTH agents' intent into one correct version. Preserve indentation.`,
      `Return ONLY the replacement code for the conflicted region — no markers, no fences, no commentary.`,
    ].join("\n");

    const res = await fetch(this.endpoint, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-api-key": this.apiKey,
        "anthropic-version": "2023-06-01",
      },
      body: JSON.stringify({
        model: this.model,
        max_tokens: 1024,
        messages: [{ role: "user", content: prompt }],
      }),
    });
    if (!res.ok) {
      throw new Error(`Claude API ${res.status}: ${await res.text()}`);
    }
    const data = (await res.json()) as { content: { type: string; text?: string }[] };
    const text = data.content.map((b) => b.text ?? "").join("").trim();
    const content = text.replace(/^```[a-z]*\n?/i, "").replace(/\n?```$/i, "");
    return {
      content,
      strategy: "llm",
      rationale: `Claude composed "${oursIntent}" with "${theirsIntent}" (re-verified by tests).`,
    };
  }
}

/**
 * Tries the deterministic union first; escalates to Claude only when a retry is
 * requested (union broke the tests) and Claude is configured. This is the
 * hybrid policy from the plan: free + deterministic where possible, LLM only at
 * a true overlap that the cheap path couldn't satisfy.
 */
export class HybridResolver implements Resolver {
  readonly name = "hybrid";
  constructor(private union: UnionResolver, private claude: ClaudeResolver) {}

  async resolve(input: ResolveInput): Promise<ResolveOutput> {
    if (input.attempt === 0 || !this.claude.available) {
      return this.union.resolve(input);
    }
    return this.claude.resolve(input);
  }
}
