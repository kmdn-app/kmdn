import { withCitations } from "./assistant-panel";

describe("withCitations", () => {
  it("turns [[path#slug]] into links from the repository root", () => {
    expect(withCitations("Every three years [[docs/policy.md#laptops]].")).toBe("Every three years [policy › laptops](docs/policy.md#laptops).");
    expect(withCitations("See [[docs/guides/setup.md]] and [[a.md#x-y]]")).toBe("See [setup](docs/guides/setup.md) and [a › x y](a.md#x-y)");
    expect(withCitations("No [[ spaces ]] or [single] brackets")).toBe("No [[ spaces ]] or [single] brackets");
  });
});
