import { createApi, readCookie, unwrap, ApiError } from "./index";

test("readCookie", () => {
  expect(readCookie("a=1; kmdn_csrf=abc%3D; b=2", "kmdn_csrf")).toBe("abc=");
  expect(readCookie("", "x")).toBeUndefined();
});

test("adds CSRF header to unsafe methods only and unwraps problems", async () => {
  const seen: Request[] = [];
  const fake = async (input: Request | string | URL) => {
    const req = input as Request;
    seen.push(req);
    if (req.method === "PATCH") {
      return new Response(JSON.stringify({ type: "about:blank", title: "Forbidden", status: 403, code: "csrf" }), {
        status: 403,
        headers: { "Content-Type": "application/problem+json" },
      });
    }
    return new Response(JSON.stringify({ needed: true }), { status: 200, headers: { "Content-Type": "application/json" } });
  };
  const api = createApi({ baseUrl: "http://x/api/v1", fetch: fake as typeof fetch, cookies: () => "kmdn_csrf=tok" });
  const st = await unwrap(api.GET("/setup/status"));
  expect(st.needed).toBe(true);
  expect(seen[0]!.headers.get("X-Kmdn-CSRF")).toBeNull();
  await expect(unwrap(api.PATCH("/me", { body: { name: "x" } }))).rejects.toBeInstanceOf(ApiError);
  expect(seen[1]!.headers.get("X-Kmdn-CSRF")).toBe("tok");
});
