import { render, screen } from "@testing-library/react";
import { vi } from "vitest";
import { ChangesView } from "./diff-views";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

const ctx = { path: "page.md", pageHref: (path: string) => path, imageSrc: (path: string) => `/images/${path}` };

test("shows both changed link destinations even when their labels match", () => {
  const { container } = render(<ChangesView base="[Sign in](https://trusted.example)" content="[Sign in](https://other.example)" ctx={ctx} />);
  expect(container.querySelector(".chg-removed a")).toHaveAttribute("href", "https://trusted.example");
  expect(container.querySelector(".chg-added a")).toHaveAttribute("href", "https://other.example");
  expect(screen.getByText("https://trusted.example")).toBeVisible();
  expect(screen.getByText("https://other.example")).toBeVisible();
});

test("shows old and new images and their descriptions", () => {
  const { container } = render(<ChangesView base="![Original image](old.png)" content="![Replacement image](new.png)" ctx={ctx} />);
  expect(container.querySelector(".chg-removed img")).toHaveAttribute("src", "/images/old.png");
  expect(container.querySelector(".chg-added img")).toHaveAttribute("src", "/images/new.png");
  expect(screen.getByRole("img", { name: "Original image" })).toBeVisible();
  expect(screen.getByRole("img", { name: "Replacement image" })).toBeVisible();
  expect(screen.getByText("Original image")).toBeVisible();
  expect(screen.getByText("Replacement image")).toBeVisible();
});

test("shows mark-only changes while keeping word diffs for plain text", () => {
  const { container, rerender } = render(<ChangesView base="Some text" content="Some **text**" ctx={ctx} />);
  expect(container.querySelector(".chg-added strong")).toHaveTextContent("text");
  expect(container.querySelector(".chg-removed")).toHaveTextContent("Some text");
  rerender(<ChangesView base="Some text" content="Some changed text" ctx={ctx} />);
  expect(container.querySelector("ins")).toHaveTextContent("changed");
});

test("renders changed reference-style images without losing their definitions", () => {
  const { container } = render(<ChangesView base={"![Diagram][img]\n\n[img]: before.png"} content={"![Diagram][img]\n\n[img]: after.png"} ctx={ctx} />);
  expect(container.querySelector(".chg-removed img")).toHaveAttribute("src", "/images/before.png");
  expect(container.querySelector(".chg-added img")).toHaveAttribute("src", "/images/after.png");
});
