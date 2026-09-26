import { initials, userColor } from "./avatar";

test("initials", () => {
  expect(initials("Maya Chen")).toBe("MC");
  expect(initials("  priya  ")).toBe("PR");
  expect(initials("Tom van der Okafor")).toBe("TO");
  expect(initials("")).toBe("?");
});

test("userColor is stable", () => {
  expect(userColor("usr_1")).toBe(userColor("usr_1"));
});
