// A scripted stand-in for Anthropic's Messages API (streaming) and an
// OpenAI-style /v1/embeddings endpoint, so the documentation screenshots
// show the assistant and the consistency check without a real provider.
// Answers follow the sample handbook in content.ts.
import { createServer, type Server } from "node:http";
import { crc32 } from "node:zlib";

type Block = {
  type: string;
  text?: string;
  name?: string;
  input?: unknown;
  content?: unknown;
};
type Body = {
  system?: unknown;
  messages?: { role: string; content: Block[] | string }[];
  tool_choice?: { name?: string };
  tools?: { name: string }[];
};

function textEvents(text: string) {
  const out: object[] = [
    {
      type: "content_block_start",
      index: 0,
      content_block: { type: "text", text: "" },
    },
  ];
  for (const w of text.split(" "))
    out.push({
      type: "content_block_delta",
      index: 0,
      delta: { type: "text_delta", text: w + " " },
    });
  out.push({ type: "content_block_stop", index: 0 });
  return out;
}

function toolEvents(id: string, name: string, input: unknown) {
  return [
    {
      type: "content_block_start",
      index: 0,
      content_block: { type: "tool_use", id, name, input: {} },
    },
    {
      type: "content_block_delta",
      index: 0,
      delta: { type: "input_json_delta", partial_json: JSON.stringify(input) },
    },
    { type: "content_block_stop", index: 0 },
  ];
}

function lastBlock(body: Body): Block {
  const m = body.messages?.at(-1);
  if (!m) return { type: "text", text: "" };
  return typeof m.content === "string" ? { type: "text", text: m.content } : (m.content.at(-1) ?? { type: "text", text: "" });
}

function reply(body: Body): { events: object[]; stop: string } {
  const forced = body.tool_choice?.name;
  const all = JSON.stringify(body.messages ?? []);
  if (forced === "judge_pair") {
    const days = [...all.matchAll(/up to (\d+) working days/g)].map((m) => m[1]);
    const years = [...all.matchAll(/refreshed every (\w+) years/g)].map((m) => m[1]);
    let v: object = {
      verdict: "related",
      explanation: "Same topic, compatible statements.",
    };
    if (days.length >= 2 && days[0] !== days[1])
      v = {
        verdict: "contradiction",
        claim_a: `up to ${days[0]} working days`,
        claim_b: `up to ${days[1]} working days`,
        explanation: `One page allows ${days[0]} working days abroad a year, the other ${days[1]}.`,
      };
    else if (years.length >= 2 && years[0] !== years[1])
      v = {
        verdict: "contradiction",
        claim_a: `refreshed every ${years[0]} years`,
        claim_b: `refreshed every ${years[1]} years`,
        explanation: `One page says laptops are refreshed every ${years[0]} years, the other every ${years[1]}.`,
      };
    return {
      events: toolEvents("toolu_judge", "judge_pair", v),
      stop: "tool_use",
    };
  }
  if (forced === "report_review") {
    return {
      events: toolEvents("toolu_review", "report_review", {
        summary: "Aligns the IT setup page with the laptop policy (laptops are refreshed every three years) and adds a first-day checklist for new hires.",
        commit_title: "IT setup: three-year laptop refresh, first-day checklist",
        commit_body: "Match the laptop policy and give new hires a short checklist for their first day.",
        style_issues: [],
      }),
      stop: "tool_use",
    };
  }
  if (forced)
    return {
      events: toolEvents("toolu_check", forced, { ok: true }),
      stop: "tool_use",
    };
  if (!body.tools?.length) {
    // Short texts: change summaries for readers, titles.
    return {
      events: textEvents("Laptops are now refreshed every three years, and new hires get a first-day checklist."),
      stop: "end_turn",
    };
  }
  const inRevision = JSON.stringify(body.system ?? "").includes("revision #");
  const last = lastBlock(body);
  if (inRevision) {
    if (last.type !== "tool_result")
      return {
        events: toolEvents("toolu_edit", "edit_file", {
          path: "docs/onboarding/it-setup.md",
          edits: [
            {
              find: "Laptops are refreshed every four years.",
              replace: "Laptops are refreshed every three years, as the [laptop policy](../policies/laptop-policy.md) says.",
            },
          ],
        }),
        stop: "tool_use",
      };
    return {
      events: textEvents(
        "I suggested one change: the refresh cycle now matches the laptop policy (three years) and links to it [[docs/policies/laptop-policy.md#laptop-policy]].",
      ),
      stop: "end_turn",
    };
  }
  if (last.type !== "tool_result")
    return {
      events: toolEvents("toolu_search", "search", {
        query: "working abroad days",
      }),
      stop: "tool_use",
    };
  return {
    events: textEvents(
      "The remote work policy allows up to 30 working days abroad a year, with two weeks' notice to your manager [[docs/policies/remote-work.md#working-abroad]]. The travel page says 20 days [[docs/policies/travel.md#working-abroad]], so the two pages disagree: ask the people team which one applies.",
    ),
    stop: "end_turn",
  };
}

// Embeddings: a few topic dimensions plus a hashed one, so passages on the
// same topic land close together and the rest stay apart.
const TOPICS = ["abroad", "laptop", "refresh", "deploy", "incident"];
function embed(text: string): number[] {
  const body = text.toLowerCase(); // page title, heading trail and text
  const v = [...TOPICS.map((w) => (body.includes(w) ? 1 : 0)), ...new Array(64).fill(0)];
  v[TOPICS.length + (crc32(body) % 64)] = 0.42;
  return v;
}

export function startFakeAI(port: number): Promise<Server> {
  const server = createServer((req, res) => {
    let raw = "";
    req.on("data", (c) => (raw += c));
    req.on("end", () => {
      const body = raw ? JSON.parse(raw) : {};
      if (req.url?.endsWith("/embeddings")) {
        const input: string[] = Array.isArray(body.input) ? body.input : [body.input];
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            data: input.map((t, index) => ({ index, embedding: embed(t) })),
            usage: { prompt_tokens: 10, total_tokens: 10 },
          }),
        );
        return;
      }
      if (req.url?.endsWith("/count_tokens")) {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ input_tokens: 1200 }));
        return;
      }
      const { events, stop } = reply(body as Body);
      const all = [
        {
          type: "message_start",
          message: { usage: { input_tokens: 1200, output_tokens: 1 } },
        },
        ...events,
        {
          type: "message_delta",
          delta: { stop_reason: stop },
          usage: { output_tokens: 60 },
        },
        { type: "message_stop" },
      ];
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      for (const e of all) res.write(`event: ${(e as { type: string }).type}\ndata: ${JSON.stringify(e)}\n\n`);
      res.end();
    });
  });
  return new Promise((resolve) => server.listen(port, "127.0.0.1", () => resolve(server)));
}
