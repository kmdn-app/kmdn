import { canonical, fnv1a64, nodeHash } from "./hash";

test("canonical sorts keys and drops undefined", () => {
  expect(canonical({ b: 1, a: [true, null, { d: "x", c: undefined }] })).toBe('{"a":[true,null,{"d":"x"}],"b":1}');
});

test("fnv1a64 matches the reference vectors", () => {
  // Reference values from the FNV spec test suite.
  expect(fnv1a64("")).toBe("cbf29ce484222325");
  expect(fnv1a64("a")).toBe("af63dc4c8601ec8c");
  expect(fnv1a64("foobar")).toBe("85944171f73967e8");
});

test("nodeHash ignores sid", () => {
  const a = { type: "paragraph", attrs: { sid: 1 }, content: [{ type: "text", text: "hi" }] };
  const b = { type: "paragraph", attrs: { sid: 9 }, content: [{ type: "text", text: "hi" }] };
  expect(nodeHash(a)).toBe(nodeHash(b));
  expect(nodeHash(a)).not.toBe(nodeHash({ ...a, content: [{ type: "text", text: "ho" }] }));
});
