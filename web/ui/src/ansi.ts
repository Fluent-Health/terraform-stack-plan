// ANSI SGR → HTML and a carriage-return-aware line buffer — a TypeScript port
// of the tier viewer's term.js (internal/server/assets/term.js), kept
// behaviorally identical: all log bytes are HTML-escaped before insertion
// (logs are attacker-influenceable), \n flushes a completed line, \r restarts
// the current one (progress spinners), and only the simple color/bold SGR
// codes render — everything else is stripped.

const MAP: Record<number, string> = {
  31: "a-red", 32: "a-green", 33: "a-yellow", 34: "a-blue", 35: "a-magenta", 36: "a-cyan", 90: "a-grey",
  91: "a-red", 92: "a-green", 93: "a-yellow", 94: "a-blue", 95: "a-magenta", 96: "a-cyan",
};

function esc(s: string): string {
  return s.replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[c]!);
}

function cls(codes: string): string {
  const out: string[] = [];
  for (const part of codes.split(";")) {
    const n = +part;
    if (n === 1) out.push("a-bold");
    else if (MAP[n]) out.push(MAP[n]);
  }
  return out.join(" ");
}

/** ansi renders one line of terminal output as escaped HTML with color spans. */
export function ansi(line: string): string {
  let out = "";
  let open = false;
  const re = /\x1b\[([0-9;]*)m/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(line))) {
    out += esc(line.slice(last, m.index));
    last = re.lastIndex;
    if (open) {
      out += "</span>";
      open = false;
    }
    const codes = m[1] || "0";
    if (codes !== "0" && codes !== "") {
      const c = cls(codes);
      if (c) {
        out += `<span class="${c}">`;
        open = true;
      }
    }
  }
  out += esc(line.slice(last));
  if (open) out += "</span>";
  return out;
}

/**
 * LineBuffer interprets \n (flush completed line) and \r (overwrite current
 * line) across streamed chunks. push() returns newly completed lines and the
 * current pending line, both ANSI-rendered to HTML.
 */
export class LineBuffer {
  private pendingRaw = "";

  push(chunk: string): { completed: string[]; pending: string } {
    const completed: string[] = [];
    for (const ch of chunk) {
      if (ch === "\n") {
        completed.push(ansi(this.pendingRaw));
        this.pendingRaw = "";
      } else if (ch === "\r") {
        this.pendingRaw = "";
      } else {
        this.pendingRaw += ch;
      }
    }
    return { completed, pending: ansi(this.pendingRaw) };
  }
}

/** renderStatic collapses \r-runs per line (final frame wins) for a finished log. */
export function renderStatic(text: string): string[] {
  return text.split("\n").map((line) => {
    const frames = line.split("\r");
    return ansi(frames[frames.length - 1]);
  });
}
