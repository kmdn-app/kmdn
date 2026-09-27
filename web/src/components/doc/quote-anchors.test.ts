import { findQuote } from "./quote-anchors";

describe("findQuote", () => {
  const text = "Ask your lead. Then ask your lead again, politely.";
  it("picks the occurrence whose context matches", () => {
    expect(findQuote(text, { quote: "ask your lead", prefix: "Then " })).toEqual([20, 33]);
    expect(findQuote(text, { quote: "your lead", suffix: " again" })).toEqual([24, 33]);
    expect(findQuote(text, { quote: "your lead", prefix: "Ask " })).toEqual([4, 13]);
  });
  it("gives up when the quote is gone", () => {
    expect(findQuote(text, { quote: "your manager" })).toBeNull();
    expect(findQuote(text, { quote: "" })).toBeNull();
  });
});
