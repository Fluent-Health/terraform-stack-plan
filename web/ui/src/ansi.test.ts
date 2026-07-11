import { describe, expect, it } from "vitest";
import { LineBuffer, ansi, renderStatic } from "./ansi";

describe("ansi", () => {
  it("escapes HTML in log bytes", () => {
    expect(ansi("<script>&")).toBe("&lt;script&gt;&amp;");
  });
  it("renders color spans and closes them", () => {
    expect(ansi("\x1b[32mok\x1b[0m done")).toBe('<span class="a-green">ok</span> done');
  });
  it("combines bold with color", () => {
    expect(ansi("\x1b[1;31mfail\x1b[0m")).toBe('<span class="a-bold a-red">fail</span>');
  });
  it("strips unknown SGR codes", () => {
    expect(ansi("\x1b[7mreverse\x1b[0m")).toBe("reverse");
  });
});

describe("LineBuffer", () => {
  it("flushes on newline and keeps the pending tail", () => {
    const b = new LineBuffer();
    const r1 = b.push("hello\nwor");
    expect(r1.completed).toEqual(["hello"]);
    expect(r1.pending).toBe("wor");
    const r2 = b.push("ld\n");
    expect(r2.completed).toEqual(["world"]);
    expect(r2.pending).toBe("");
  });
  it("carriage return restarts the pending line (spinner frames)", () => {
    const b = new LineBuffer();
    const r = b.push("frame1\rframe2\rfinal\n");
    expect(r.completed).toEqual(["final"]);
  });
});

describe("renderStatic", () => {
  it("keeps only the last \\r frame per line", () => {
    expect(renderStatic("a\rb\nplain")).toEqual(["b", "plain"]);
  });
});
