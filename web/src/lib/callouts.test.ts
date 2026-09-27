import { describe, expect, it } from "vitest";
import { parseAlert, parseContainer } from "./callouts";

describe("parseAlert", () => {
  it("does not take the next line as the title", () => {
    expect(parseAlert("> [!NOTE]\n> Everything here is published.")).toEqual({ type: "note", title: undefined, body: "Everything here is published." });
  });
  it("keeps a same-line title and lowercases the type", () => {
    expect(parseAlert("> [!WARNING] Heads up\n> Body\n>\n> More")).toEqual({ type: "warning", title: "Heads up", body: "Body\n\nMore" });
  });
});

describe("parseContainer", () => {
  it("splits type, title and body", () => {
    expect(parseContainer("::: tip Read this\nBody\n:::")).toEqual({ type: "tip", title: "Read this", body: "Body" });
  });
  it("defaults to note", () => {
    expect(parseContainer(":::\nBody\n:::")).toEqual({ type: "note", title: undefined, body: "Body" });
  });
});
