import { ENGINE_VERSION } from "./index";

test("engine version is stamped", () => {
  expect(ENGINE_VERSION).toBeGreaterThan(0);
});
