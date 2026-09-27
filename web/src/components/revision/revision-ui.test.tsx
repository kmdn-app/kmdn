import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import "@/i18n";
import type { RevisionEvent } from "@/lib/revisions";
import { ActivityItem } from "./revision-ui";

function line(e: Partial<RevisionEvent>) {
  const event: RevisionEvent = { id: "rve_1", actor_type: "user", kind: "created", data: {}, created_at: new Date().toISOString(), ...e };
  const { container } = render(
    <ol>
      <ActivityItem event={event} />
    </ol>,
  );
  return container.querySelector("li")!.textContent!.split(" · ")[0]!.trim();
}

describe("ActivityItem", () => {
  it("names the branch kmdn started, with no actor", () => {
    expect(line({ actor_type: "system", kind: "branch_created", data: { branch: "kmdn/r12-fix-typos" } })).toBe("Started the branch kmdn/r12-fix-typos");
  });

  it("shows kmdn's own events without an actor", () => {
    for (const [kind, text] of [
      ["change_request_opened", "Opened its pull request on the forge"],
      ["update_available", "Published changed pages in this revision"],
      ["approved", "Every reviewer approved"],
      ["publish_blocked", "Publishing stopped"],
      ["conflicts_resolved", "All conflicts are resolved"],
    ]) {
      expect(line({ actor_type: "system", kind, data: {} })).toBe(text);
    }
  });

  it("says the forge merged a revision published without an actor", () => {
    expect(line({ actor_type: "system", kind: "published", data: { sha: "abc" } })).toBe("Published: the pull request was merged");
  });

  it("names the person, or Someone when their name is unknown", () => {
    expect(line({ actor_name: "Maya Chen", kind: "file_renamed", data: { path: "b.md", from_path: "a.md" } })).toBe("Maya Chen moved a.md to b.md");
    expect(line({ kind: "published", data: {} })).toBe("Someone published it");
  });
});
