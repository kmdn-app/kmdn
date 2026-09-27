import { describe, expect, it } from "vitest";
import { getSchema } from "@tiptap/core";
import { Node as PMNode } from "@tiptap/pm/model";
import { parse } from "@kmdn/doc-engine";
import { schemaExtensions } from "./schema";
import { findQuote } from "./consistency-marks";

const schema = getSchema(schemaExtensions());
const docOf = (md: string) => PMNode.fromJSON(schema, parse(md).doc);

describe("findQuote", () => {
  it("finds a claim across marks, ignoring case", () => {
    const doc = docOf("# Travel\n\nEmployees can work abroad for up to **30 working days** per year.\n");
    const r = findQuote(doc, "Up to 30 working days");
    expect(r).not.toBeNull();
    expect(doc.textBetween(r!.from, r!.to)).toBe("up to 30 working days");
  });
  it("returns null when the text isn't there", () => {
    expect(findQuote(docOf("# Travel\n\nNothing here.\n"), "30 working days")).toBeNull();
    expect(findQuote(docOf("# Travel\n"), "  ")).toBeNull();
  });
});
