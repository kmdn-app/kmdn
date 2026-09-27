import { ChangeSet, Text } from "@codemirror/state";
import { sourceChanges } from "./source-changes";

const text = (value: string) => Text.of(value.split("\n"));

test.each([
  ["First Middle Last", "First theirs Middle Last theirs", 12, "First theirs Middle ours Last theirs"],
  ["First\n\nMiddle\n\nLast\n", "First theirs\n\nMiddle\n\nLast theirs\n", 13, "First theirs\n\nMiddle ours\n\nLast theirs\n"],
])("preserves the middle edit between separate remote changes", (before, after, position, expected) => {
  const local = ChangeSet.of({ from: position, insert: " ours" }, before.length);
  expect(local.map(sourceChanges(before, after)).apply(text(after)).toString()).toBe(expected);
});

test("reconstructs insertions, deletions and replacements without losing characters", () => {
  const words = ["", "😀\nfirst", "🦊\nlast", "x😀x", "x🦊x"];
  for (let length = 1; length <= 4; length++) {
    for (let value = 0; value < 2 ** length; value++) words.push(value.toString(2).padStart(length, "0"));
  }
  for (const before of words) {
    for (const after of words) expect(sourceChanges(before, after).apply(text(before)).toString()).toBe(after);
  }
});

test("maps separate edits around a large unchanged range", () => {
  const middle = "keep\n".repeat(50_000);
  const before = `First\n${middle}Last\n`;
  const after = `First theirs\n${middle}Last theirs\n`;
  const local = ChangeSet.of({ from: 100, insert: "ours" }, before.length);
  expect(local.map(sourceChanges(before, after)).apply(text(after)).toString()).toBe(after.slice(0, 107) + "ours" + after.slice(107));
});

test("refuses a diff beyond its budget instead of replacing independent edit anchors", () => {
  expect(() => sourceChanges("a".repeat(1000), "b".repeat(1000))).toThrow("could not be combined safely");
});
