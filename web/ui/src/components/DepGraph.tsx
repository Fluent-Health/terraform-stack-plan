import { For, Show, createEffect, createSignal } from "solid-js";
import ELK from "elkjs/lib/elk.bundled.js";
import type { ExecutionDetail } from "../api/client";
import { SEM_DOT, statusSem } from "../status";
import { changeReasonFor, normalizedEdges } from "../dag";

interface PNode {
  id: string;
  x: number;
  y: number;
  width: number;
  height: number;
}
interface PEdge {
  id: string;
  points: { x: number; y: number }[];
}

/**
 * DepGraph: an inert, layered elkjs render of one tier's changed subgraph.
 * Nodes are coloured by status; clicking one surfaces its change reason. Reuses
 * the Catalog renderer approach; no zoom/pan (kept simple inside the panel).
 */
export function DepGraph(props: { detail: ExecutionDetail }) {
  const [nodes, setNodes] = createSignal<PNode[]>([]);
  const [edges, setEdges] = createSignal<PEdge[]>([]);
  const [canvas, setCanvas] = createSignal({ width: 400, height: 200 });
  const [selected, setSelected] = createSignal<string | null>(null);
  const [layoutError, setLayoutError] = createSignal(false);
  const elk = new ELK();

  createEffect(() => {
    const g = props.detail.graph;
    const stacks = g?.stacks ?? [];
    const es = normalizedEdges(g);
    setLayoutError(false);
    if (stacks.length === 0) {
      setNodes([]);
      setEdges([]);
      return;
    }
    elk
      .layout({
        id: "root",
        layoutOptions: {
          "elk.algorithm": "layered",
          "elk.direction": "RIGHT",
          "elk.spacing.nodeNode": "24",
          "elk.spacing.nodeNodeBetweenLayers": "60",
        },
        children: stacks.map((s) => ({ id: s.path, width: 150, height: 34 })),
        edges: es.map((e, i) => ({ id: `e-${i}`, sources: [e.from], targets: [e.to] })),
      })
      .then((res) => {
        const pn: PNode[] = (res.children ?? []).map((n) => ({
          id: n.id,
          x: n.x ?? 0,
          y: n.y ?? 0,
          width: n.width ?? 150,
          height: n.height ?? 34,
        }));
        const pe: PEdge[] = ((res.edges as any[]) ?? []).map((e: any) => {
          const pts: { x: number; y: number }[] = [];
          const sec = e.sections?.[0];
          if (sec) {
            pts.push(sec.startPoint);
            for (const bp of sec.bendPoints ?? []) pts.push(bp);
            pts.push(sec.endPoint);
          }
          return { id: e.id, points: pts };
        });
        setNodes(pn);
        setEdges(pe);
        let maxX = 400,
          maxY = 120;
        for (const n of pn) {
          maxX = Math.max(maxX, n.x + n.width);
          maxY = Math.max(maxY, n.y + n.height);
        }
        setCanvas({ width: maxX + 40, height: maxY + 40 });
      })
      .catch(() => {
        /* layout failure → say so, never crash the panel */
        setNodes([]);
        setEdges([]);
        setLayoutError(true);
      });
  });

  const statusOf = (path: string) => props.detail.graph?.stacks?.find((s) => s.path === path)?.status ?? "";
  const path = (pts: { x: number; y: number }[]) =>
    pts.length < 2 ? "" : `M ${pts[0].x} ${pts[0].y} ` + pts.slice(1).map((p) => `L ${p.x} ${p.y}`).join(" ");

  return (
    <div class="border border-base-300 rounded-field bg-base-100 overflow-auto">
      <Show when={layoutError()}>
        <p class="p-3 text-xs opacity-60">Could not lay out the dependency graph for this run.</p>
      </Show>
      <svg width={canvas().width} height={canvas().height} class="min-h-[120px]">
        <g class="edges">
          <For each={edges()}>
            {(e) => (
              <path
                d={path(e.points)}
                fill="none"
                stroke="currentColor"
                class="text-base-content/20"
                stroke-width="1.5"
              />
            )}
          </For>
        </g>
        <g class="nodes">
          <For each={nodes()}>
            {(n) => (
              <g
                class="cursor-pointer"
                transform={`translate(${n.x}, ${n.y})`}
                onClick={() => setSelected((p) => (p === n.id ? null : n.id))}
              >
                <rect
                  width={n.width}
                  height={n.height}
                  rx="6"
                  class="fill-base-200 stroke-base-300"
                  classList={{ "stroke-primary": selected() === n.id }}
                  stroke-width="1.5"
                />
                <circle cx="12" cy={n.height / 2} r="4" fill={SEM_DOT[statusSem(statusOf(n.id))]} />
                <text x="24" y={n.height / 2 + 4} class="text-[10px] fill-base-content font-mono">
                  {n.id.length > 18 ? "…" + n.id.slice(-17) : n.id}
                </text>
              </g>
            )}
          </For>
        </g>
      </svg>
      <Show when={selected()}>
        {(sel) => (
          <div class="border-t border-base-300 px-3 py-2 text-xs">
            <span class="font-mono">{sel()}</span>
            <Show
              when={changeReasonFor(props.detail.change_reasons, sel())}
              fallback={<span class="opacity-50"> · no change reason recorded</span>}
            >
              {(r) => <span class="opacity-70"> · {r()}</span>}
            </Show>
          </div>
        )}
      </Show>
    </div>
  );
}
