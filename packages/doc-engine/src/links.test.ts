import { extractLinks, rewriteLinks, slugify } from "./links";

const md = `# Guide

See [the handbook](../index.md#start), ![desk](images/desk.png "Desk") and [ref][r].
Also <https://example.com> and [spaced](<my page.md>) and [paren](a_(b).md).

## Start here
## Start here

[r]: ./other.md "Other"
`;

test("extracts links with exact destination ranges", () => {
  const { links, headings } = extractLinks(md);
  const urls = links.map((l) => [l.kind, l.url, l.start >= 0 ? md.slice(l.start, l.end) : null]);
  expect(urls).toEqual([
    ["link", "../index.md#start", "../index.md#start"],
    ["image", "images/desk.png", "images/desk.png"],
    ["link", "https://example.com", null],
    ["link", "my page.md", "my page.md"],
    ["link", "a_(b).md", "a_(b).md"],
    ["definition", "./other.md", "./other.md"],
  ]);
  expect(headings.map((h) => h.slug)).toEqual(["guide", "start-here", "start-here-1"]);
});

test("rewrites only destinations, keeping every other byte", () => {
  const out = rewriteLinks(md, (u) => (u.startsWith("../index.md") ? u.replace("../index.md", "../start.md") : u === "my page.md" ? "your page.md" : u === "./other.md" ? "./moved/other.md" : null));
  expect(out).toBe(md.replace("../index.md#start", "../start.md#start").replace("<my page.md>", "<your page.md>").replace("./other.md", "./moved/other.md"));
});

test("CRLF files stay CRLF", () => {
  const crlf = "A [x](a.md)\r\nB [y](b.md)\r\n";
  expect(rewriteLinks(crlf, (u) => (u === "b.md" ? "c.md" : null))).toBe("A [x](a.md)\r\nB [y](c.md)\r\n");
});

test("encodes spaces when the destination isn't bracketed", () => {
  expect(rewriteLinks("[a](x.md)\n", () => "my x.md")).toBe("[a](my%20x.md)\n");
});

test("slugify matches GitHub for common headings", () => {
  expect(slugify("Hello, World!")).toBe("hello-world");
  expect(slugify("Écoles & Élèves")).toBe("écoles--élèves");
  expect(slugify("  API v2.0 ")).toBe("api-v20");
  expect(slugify("Qu’est-ce que c’est ? — 概要")).toBe("quest-ce-que-cest---概要");
  expect(slugify("Emoji 🚀 launch")).toBe("emoji--launch");
});
