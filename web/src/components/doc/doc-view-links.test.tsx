import { render, screen } from "@testing-library/react";
import { vi } from "vitest";
import { DocView } from "./doc-view";

vi.mock("@tanstack/react-router", () => ({ useRouter: () => ({ history: { push: vi.fn() } }) }));

const ctx = { path: "docs/readme.md", pageHref: (path: string) => `/pages/${encodeURI(path)}`, imageSrc: (path: string) => path };

test.each([
  ["100%.md", "/pages/docs/100%25.md"],
  ["bad%2.md", "/pages/docs/bad%252.md"],
  ["bad%FF.md", "/pages/docs/bad%25FF.md"],
  ["caf%C3%A9.md", "/pages/docs/caf%C3%A9.md"],
])("renders the relative target %s safely", (target, expected) => {
  render(<DocView markdown={`[Read more](${target})`} ctx={ctx} />);
  expect(screen.getByRole("link", { name: "Read more" })).toHaveAttribute("href", expected);
});
