/** Resolves a markdown link target relative to the file that contains it. */
export function resolveRelative(fromFile: string, target: string): string {
  if (target.startsWith("/")) return target.replace(/^\/+/, "");
  const base = fromFile.split("/").slice(0, -1);
  for (const part of target.split("/")) {
    if (part === "." || part === "") continue;
    if (part === "..") base.pop();
    else base.push(part);
  }
  return base.join("/");
}

export function isExternal(href: string): boolean {
  return /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("//");
}

/** GitHub-style heading anchor. */
export function slugify(text: string): string {
  return text
    .toLowerCase()
    .trim()
    .replace(/[^\p{L}\p{N}\s_-]/gu, "")
    .replace(/\s/g, "-");
}
