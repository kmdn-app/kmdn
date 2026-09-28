# 08 · Assistant

The assistant is a server-side agent loop in Go that uses kmdn's own services as tools. It never writes directly to published content and never edits a revision silently: all edits are **suggestions**.

## Surfaces

| Where | Thread type | Visibility | Can edit |
|-------|-------------|------------|----------|
| Repo home composer, Assistant tab with Published context | **Repo Q&A thread** | Private to the user | No. Offers "Start a revision" |
| Assistant tab inside a revision | **Revision thread** (one per revision) | Shared with everyone who can see the revision | Yes, as suggestions in the revision |
| Review screen | **Review summary** (see [07](07-review.md#review-assistant)) | Revision viewers | Via "Fix" on a finding |
| ⌘K "Ask assistant" | Routes to the current context's thread | — | — |

### Repo Q&A → revision handoff

When a Q&A turn asks for a change, the model calls the `propose_revision` tool instead of editing. The UI renders a card "Start a revision: <title>" with the files it plans to touch. On confirm, kmdn creates the revision, sets it as the active context, and **moves the conversation**: the private Q&A turns that led to the request are copied into the new revision's shared thread (the user sees a notice "This conversation will be visible to revision collaborators"), then the agent continues there.

### Shared revision thread

- Every message shows the human who wrote it. Anyone who can edit the revision can prompt the assistant; Viewers of the revision can read the thread but not prompt.
- One run at a time per revision thread. A second prompt while a run is in progress is queued (shown as "Queued").
- Assistant replies stream to all subscribers as events on the thread's `assistant:<thread>` scope over the multiplexed WebSocket (text deltas, tool activity, run state).
- Suggestions made in a run are grouped: "Added 4 suggestions in 2 files" with file chips; each suggestion links back to the message.

## Agent loop

- Provider-agnostic loop in `internal/assistant`: messages → model → tool calls → results → … until the model ends the turn or limits hit.
- Max 25 tool calls per run, max 10 minutes wall clock, configurable.
- Streaming text and tool-call progress ("Reading docs/onboarding/it-setup.md…") are sent to the UI.
- System prompt includes: product role, the repo's content root, its guidance (`AGENTS.md`, style guide, skill index: see [Repository guidance](#repository-guidance)), the user's role, the current revision manifest, the active file and selection (if any).
- Prompt caching used where the provider supports it (Anthropic cache breakpoints on system prompt + repo context).

## Tools

Read tools (all contexts):

| Tool | Description |
|------|-------------|
| `search(query, path_prefix?, limit?)` | Full-text search over published content (and the revision's materialized files in revision context). Returns path, heading, snippet |
| `list_tree(path?)` | Files and folders under the content root (revision-aware) |
| `read_file(path, from_heading?, max_chars?)` | Markdown of a file (checkpoint if in revision context) with heading outline |
| `get_history(path, limit?)` | Recent published versions of a file with authors and messages |
| `get_links(path)` | Inbound/outbound links |
| `find_related(path, heading?)` | Similar passages elsewhere in the repo (passage index), with similarity scores. Arrives with the passage index (#60) |
| `read_comments(path?)` | Comment threads in the revision or discussions on a published doc |

Only when the repository has skills: `load_skill(name, file?)` returns a skill's `SKILL.md` without its frontmatter and lists the other files in its folder; `file` reads one of those. Only listed files can be read, so a path can't leave the skill's folder.

Q&A-only tool: `propose_revision(title, description, files[])`.

Revision-context write tools (all produce suggestions attributed to the assistant on behalf of the requesting user):

| Tool | Description |
|------|-------------|
| `edit_file(path, edits[{find, replace}] \| new_markdown)` | Engine computes a block diff (then a word diff inside changed blocks) and applies it as insertion/deletion suggestions marked `assistant`, on behalf of the person who asked. Preferred mode: targeted find/replace on markdown; each `find` must match exactly once, or the tool reports why |
| `create_file(path, markdown)` | Adds a new file to the manifest, content wrapped as one insertion suggestion |
| `rename_file(from, to, rewrite_links: bool)` | Proposes a rename; file ops are proposals anyone who can edit the revision confirms in the thread |
| `delete_file(path)` | Proposal, confirmed in the thread |
| `reply_to_thread(thread_id, body)` | Reply in a comment thread when asked (posted as the person, marked "via the assistant") |

In a revision thread the read tools see the revision: `read_file` returns the revision's version of its pages (pending suggestions excluded), `list_tree` includes its added, renamed and deleted pages, and `read_comments` lists its comment threads. Suggestion cards show "Assistant for <person>".

The assistant never approves, publishes, resolves threads, or changes settings.

### Repository guidance

A repository tells agents how to work on it with the files coding agents already use. At the start of every run, in Q&A and revision threads alike, the assistant reads these from the published head. They're read wherever they are, not just in the content scope (`repos.ReadConfigFile`):

- **`AGENTS.md`** at the repo root, then at the content root if that's a subfolder. Both go in, the content root's last, and the prompt says the later one takes precedence. Capped at 16,000 characters each.
- **Style guide**: the first of `.kmdn/style.md`, `STYLE.md`, `<content root>/STYLE.md` (the same file the review summary checks against). Capped at 8,000 characters.
- **Skills** in `.skills/` at the repo root and the content root, in the Agent Skills layout: `.skills/<name>/SKILL.md` with `name` and `description` in YAML frontmatter, plus any other files in the folder, or a flat `.skills/<name>.md`. A folder without `SKILL.md` and `.skills/README.md` aren't skills. On a name clash the content root's skill wins. Only the index (name and description, at most 50 skills, 300 characters per description) goes in the prompt. The model calls `load_skill` when a task matches (progressive disclosure keeps the prompt small).

All of it goes in the cached system prompt block after kmdn's rules. The prompt says kmdn's rules win on a conflict: repository text can't make the assistant publish, approve, write outside suggestions or change settings. Guidance is cached in memory per repo and head SHA, so a sync that moves the head brings in the new files on the next run. A read error isn't cached, so the next run tries again. Without any of these files, the prompt and the tools are unchanged.

### Citations

Answers cite sources with `[[path#heading-slug]]` markers the UI turns into chips linking to the doc and heading. The model is instructed to cite every factual claim drawn from the repo; the UI shows "No sources" when a Q&A answer has none.

## Providers

```go
type Provider interface {
    Name() string
    Stream(ctx, req ChatRequest) (<-chan ChatEvent, error) // text deltas, tool calls, usage, stop
    CountTokens(ctx, req ChatRequest) (int, error)
}
```

- **Anthropic** (default, first-class): Messages API with tool use, streaming, prompt caching. Default model `claude-sonnet-5`; admin can pick others (e.g. `claude-opus-5-5` for review summaries, `claude-haiku-4-5-20251001` for cheap tasks like commit titles).
- **OpenAI-compatible**: base URL + key + model name. Covers OpenAI, OpenRouter, Ollama, vLLM, LM Studio. On `api.openai.com` kmdn uses the Responses API (reasoning models only take function tools there) and `max_output_tokens` with room for reasoning; other servers get Chat Completions. Tool-calling support is required; the admin console runs a capability check on save.
- Per task model routing: `chat`, `review_summary`, `short_text` (titles, commit messages, revision names).

## Limits, cost and privacy

- Instance admin sets provider keys (stored encrypted, see [09](09-auth-permissions.md#secrets)).
- Per-user daily token budget (default 500k tokens), a monthly budget per organization (org settings, `monthly_tokens`; a deployment can fix it per plan) and an optional per-instance monthly cap; usage recorded per run in `assistant_runs` (with its org) and shown in the org console and the instance console. An org can also turn AI features off ([16](16-organizations.md#ai)).
- Repo-level switch "Allow assistant in this repo" (default on) for repos whose content must not leave the instance; with a local OpenAI-compatible provider this can stay on.
- Content sent to the provider: only what the tools return and the current context. No background indexing sent out.
- Every run is audited (who, repo, revision, tools called, tokens).

## Consistency check (duplicates and contradictions)

Finds places where the repo says the same thing twice or says contradicting things ("30 working days abroad" on one page, "20 days" on another). Needs an AI provider; without one the feature is hidden.

### Passage index

- Published content (and each open revision's changed files, in a separate scope) is chunked into **passages**: one per heading section, split further at ~300 tokens, with path, heading slug and content hash.
- Each passage gets an **embedding** from the configured provider (a separate `embeddings` model setting: an OpenAI-compatible embeddings endpoint, e.g. a local model via Ollama, or a hosted one; Anthropic has no embeddings API, so the admin picks one). Stored as unit-length float32 blobs in `passage_embeddings`, keyed by model and content hash (page title + heading trail + text), so only changed passages are re-embedded and a revision's unchanged passages reuse the published vectors. Only published passages are stored (`passages`); a revision's are chunked from its materialized files on each check.
- Nearest-neighbour search is brute-force cosine similarity in Go over the repo's vectors (thousands of passages fit comfortably; no vector extension, keeps SQLite pure-Go). Behind an interface so pgvector can be used on Postgres later.

### Per revision

When a revision's changed passages are materialized (debounced 30 s) and at submit:

1. For each changed passage, take the top-k (k=8) similar passages elsewhere in the repo above a similarity threshold (0.78).
2. Pairs above 0.92 whose text is identical (case and spacing aside) are **duplicates** directly; near-identical pairs still go to the model, since a changed number or date there is a contradiction (spike S8).
3. The LLM (`short_text` task model) judges each remaining pair: `contradiction` (with the conflicting claims quoted), `duplicate`, `related`, or `none`.
4. Findings appear in the revision overview (a Consistency section under the review card and checks), in the Publish dialog, and as an underline on the passage in the editor (wavy for contradictions, dotted for duplicates) with a hover card. Actions: **Fix** (briefs the revision's assistant thread to align the text, as suggestions), **Link instead** (duplicates: replace the repeated passage with a link to the other page, as a suggestion), **Ignore** (with a reason; ignored pairs are remembered by passage hashes across revisions and scans until either passage changes).

Verdicts are cached per pair of passage hashes (`consistency_judgments`), so re-checks and scans only pay for new pairs. A check makes at most 40 model calls.

Advisory only: never blocks submit, approval or publish. Findings are listed in the Publish dialog.

### Repo scan

- Weekly (configurable, or "Run now" by maintainers): all-pairs over Published passages via the same neighbour search, then LLM judgment on candidates, capped per run (default 500 LLM calls) and resumable.
- Results form the repo's **Consistency report**: Contradictions and Duplicates, each with both excerpts side by side, pages involved, first seen, status (Open / Ignored / Fixing in revision X). **Start a revision to fix** creates a revision touching both pages with the assistant briefed on the finding.
- Findings auto-close when a later scan no longer reproduces them (e.g. after a fix is published). Pairs a capped scan didn't get to keep their state; the scan shows as "capped" and the next one continues (cached verdicts are free).
- Scan frequency and the call cap are instance settings (Admin → AI provider); an hourly job queues scans that are due.
- Cost shown in admin usage; the scan respects the instance token budget.

### Link graph integration

Duplicate pairs can be drawn as dotted edges in the link graph ("Show duplicates"), see [04](04-doc-engine.md#link-index-and-graph).

## Search index (assistant + UI)

- Published content indexed on each mirror update (changed files only); revision files indexed on materialization in a separate per-revision scope.
- Fields: path, title (frontmatter `title` or first H1), headings, body text, updated_at.
- SQLite FTS5 with `unicode61 remove_diacritics 2` tokenizer; Postgres `tsvector` with `simple` config + trigram for paths.
- The passage embeddings built for the consistency check can later power semantic search; v1 search stays full-text.
