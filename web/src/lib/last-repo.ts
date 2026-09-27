const KEY = "kmdn-last-repo";

export function rememberRepo(id: string) {
  try {
    localStorage.setItem(KEY, id);
  } catch {
    /* storage unavailable */
  }
}

export function lastRepo(): string | null {
  try {
    return localStorage.getItem(KEY);
  } catch {
    return null;
  }
}
