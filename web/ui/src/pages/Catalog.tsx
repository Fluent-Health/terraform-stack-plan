import { For, Show, Suspense, createEffect, createResource, createSignal } from "solid-js";
import { api, type Catalog } from "../api/client";
import ELK from "elkjs/lib/elk.bundled.js";

interface PositionedNode {
	id: string;
	x: number;
	y: number;
	width: number;
	height: number;
}

interface PositionedEdge {
	id: string;
	points: { x: number; y: number }[];
	from: string;
	to: string;
	kind: string;
}

export function Catalog() {
	createEffect(() => {
		document.title = "Component Catalog · tfstackplan";
	});
	const [catalog] = createResource(api.catalog);
	const [search, setSearch] = createSignal("");
	const [selected, setSelected] = createSignal<string | null>(null);

	// Zoom and Pan States
	const [zoom, setZoom] = createSignal(1);
	const [pan, setPan] = createSignal({ x: 50, y: 50 });
	const [dragging, setDragging] = createSignal(false);
	const [dragStart, setDragStart] = createSignal({ x: 0, y: 0 });

	// Positioned Graph Elements
	const [nodes, setNodes] = createSignal<PositionedNode[]>([]);
	const [edges, setEdges] = createSignal<PositionedEdge[]>([]);
	const [canvasSize, setCanvasSize] = createSignal({ width: 800, height: 600 });

	const elk = new ELK();

	// Calculate Graph Layout dynamically whenever catalog loads
	createEffect(() => {
		const cat = catalog();
		if (!cat) return;

		const elkGraph = {
			id: "root",
			layoutOptions: {
				"elk.algorithm": "layered",
				"elk.direction": "RIGHT",
				"elk.spacing.nodeNode": "60",
				"elk.spacing.nodeNodeBetweenLayers": "100",
			},
			children: cat.components.map((c) => ({
				id: c.id,
				width: 180,
				height: 70,
			})),
			edges: cat.edges.map((e, index) => ({
				id: `e-${index}`,
				sources: [e.from],
				targets: [e.to],
			})),
		};

		elk.layout(elkGraph)
			.then((result) => {
				const posNodes: PositionedNode[] = (result.children ?? []).map((n) => ({
					id: n.id,
					x: n.x ?? 0,
					y: n.y ?? 0,
					width: n.width ?? 180,
					height: n.height ?? 70,
				}));

				const posEdges: PositionedEdge[] = (result.edges as any[] ?? []).map((e: any, i) => {
					const origEdge = cat.edges[i];
					const points: { x: number; y: number }[] = [];
					if (e.sections && e.sections.length > 0) {
						const sec = e.sections[0];
						points.push({ x: sec.startPoint.x, y: sec.startPoint.y });
						if (sec.bendPoints) {
							for (const bp of sec.bendPoints) {
								points.push({ x: bp.x, y: bp.y });
							}
						}
						points.push({ x: sec.endPoint.x, y: sec.endPoint.y });
					}
					return {
						id: e.id,
						points,
						from: origEdge.from,
						to: origEdge.to,
						kind: origEdge.kind,
					};
				});

				setNodes(posNodes);
				setEdges(posEdges);

				// Auto-scale canvas size based on result bounding box
				let maxX = 800;
				let maxY = 600;
				for (const n of posNodes) {
					if (n.x + n.width > maxX) maxX = n.x + n.width;
					if (n.y + n.height > maxY) maxY = n.y + n.height;
				}
				setCanvasSize({ width: maxX + 100, height: maxY + 100 });
			})
			.catch((err) => {
				console.error("Layout calculation failed:", err);
			});
	});

	// Filters
	const filteredComponents = () => {
		const cat = catalog();
		if (!cat) return [];
		const s = search().toLowerCase();
		if (!s) return cat.components;
		return cat.components.filter(
			(c) =>
				c.id.toLowerCase().includes(s) ||
				c.stacks.some((st) => st.toLowerCase().includes(s)) ||
				c.watches.some((w) => w.toLowerCase().includes(s))
		);
	};

	const selectedComponent = () => {
		const cat = catalog();
		const id = selected();
		if (!cat || !id) return null;
		return cat.components.find((c) => c.id === id);
	};

	// Dragging Handlers
	const handleMouseDown = (e: MouseEvent) => {
		const target = e.target as SVGElement;
		if (target.closest(".node")) return; // Don't drag the background if clicking on a node
		setDragging(true);
		setDragStart({ x: e.clientX - pan().x, y: e.clientY - pan().y });
	};

	const handleMouseMove = (e: MouseEvent) => {
		if (!dragging()) return;
		setPan({ x: e.clientX - dragStart().x, y: e.clientY - dragStart().y });
	};

	const handleMouseUp = () => {
		setDragging(false);
	};

	// Draw SVG Path between points
	const drawPath = (points: { x: number; y: number }[]) => {
		if (points.length < 2) return "";
		let d = `M ${points[0].x} ${points[0].y}`;
		for (let i = 1; i < points.length; i++) {
			d += ` L ${points[i].x} ${points[i].y}`;
		}
		return d;
	};

	return (
		<div class="h-full flex flex-col gap-4">
			<div class="flex items-center gap-4 border-b border-base-300 pb-3">
				<div>
					<h1 class="text-2xl font-bold tracking-tight">🗺 Component Catalog</h1>
					<p class="text-sm opacity-60">Visualizing watch causality and stack topology across environments</p>
				</div>
				<div class="ml-auto flex items-center gap-2">
					<input
						type="text"
						class="input input-sm input-bordered w-64"
						placeholder="Search components or stacks…"
						value={search()}
						onInput={(e) => setSearch(e.currentTarget.value)}
					/>
					<div class="join">
						<button class="btn btn-sm join-item" onClick={() => setZoom((z) => Math.max(0.2, z - 0.1))}>
							−
						</button>
						<button class="btn btn-sm join-item font-mono text-xs select-none">
							{Math.round(zoom() * 100)}%
						</button>
						<button class="btn btn-sm join-item" onClick={() => setZoom((z) => Math.min(3, z + 0.1))}>
							+
						</button>
						<button
							class="btn btn-sm join-item"
							onClick={() => {
								setZoom(1);
								setPan({ x: 50, y: 50 });
							}}
						>
							Reset
						</button>
					</div>
				</div>
			</div>

			<Suspense fallback={<span class="loading loading-dots" />}>
				<div class="flex-1 grid grid-cols-1 lg:grid-cols-[1fr_320px] gap-4 min-h-0">
					{/* Interactive SVG Workspace */}
					<div
						class="relative border border-base-300 bg-base-100 rounded-box overflow-hidden cursor-grab select-none select-none"
						classList={{ "cursor-grabbing": dragging() }}
						onMouseDown={handleMouseDown}
						onMouseMove={handleMouseMove}
						onMouseUp={handleMouseUp}
						onMouseLeave={handleMouseUp}
					>
						<svg
							class="w-full h-full min-h-[500px]"
							style={{
								transform: `translate(${pan().x}px, ${pan().y}px) scale(${zoom()})`,
								"transform-origin": "0 0",
							}}
							width={canvasSize().width}
							height={canvasSize().height}
						>
							<defs>
								<marker
									id="arrowhead"
									markerWidth="10"
									markerHeight="7"
									refX="8"
									refY="3.5"
									orient="auto"
								>
									<polygon points="0 0, 10 3.5, 0 7" fill="currentColor" class="text-base-content/30" />
								</marker>
								<marker
									id="arrowhead-highlight"
									markerWidth="10"
									markerHeight="7"
									refX="8"
									refY="3.5"
									orient="auto"
								>
									<polygon points="0 0, 10 3.5, 0 7" fill="currentColor" class="text-primary" />
								</marker>
							</defs>

							{/* Render Edge Links */}
							<g class="edges">
								<For each={edges()}>
									{(e) => {
										const isRelated = () =>
											!selected() || selected() === e.from || selected() === e.to;
										const isPrimary = () => selected() === e.from || selected() === e.to;

										return (
											<path
												d={drawPath(e.points)}
												fill="none"
												stroke="currentColor"
												stroke-width={isPrimary() ? "2.5" : "1.5"}
												class={
													isPrimary()
														? "text-primary"
														: e.kind === "watch"
														? "text-success/50"
														: "text-base-content/20"
												}
												classList={{
													"opacity-20": selected() !== null && !isRelated(),
												}}
												marker-end={
													isPrimary() ? "url(#arrowhead-highlight)" : "url(#arrowhead)"
												}
											/>
										);
									}}
								</For>
							</g>

							{/* Render Component Nodes */}
							<g class="nodes">
								<For each={nodes()}>
									{(n) => {
										const isSelected = () => selected() === n.id;
										const comp = () => catalog()?.components.find((c) => c.id === n.id);
										const isFiltered = () =>
											filteredComponents().some((fc) => fc.id === n.id);

										return (
											<g
												class="node cursor-pointer"
												transform={`translate(${n.x}, ${n.y})`}
												onClick={() => setSelected((prev) => (prev === n.id ? null : n.id))}
											>
												<rect
													width={n.width}
													height={n.height}
													rx="8"
													class="fill-base-200 stroke-base-300 transition-colors"
													classList={{
														"stroke-primary fill-primary/10": isSelected(),
														"opacity-30": selected() !== null && !isSelected(),
														"border-dashed": !isFiltered(),
													}}
													stroke-width={isSelected() ? "2.5" : "1.5"}
												/>
												<text
													x="15"
													y="30"
													class="font-semibold text-xs fill-base-content"
													classList={{ "fill-primary": isSelected() }}
												>
													{n.id}
												</text>
												<text x="15" y="48" class="text-[10px] fill-base-content/60">
													{comp()?.stacks.length ?? 0} stacks | {comp()?.watches.length ?? 0}{" "}
													watches
												</text>
											</g>
										);
									}}
								</For>
							</g>
						</svg>
					</div>

					{/* Side Details Panel */}
					<div class="border border-base-300 bg-base-200/50 p-4 rounded-box flex flex-col gap-4">
						<Show
							when={selectedComponent()}
							fallback={
								<div class="h-full flex flex-col items-center justify-center text-center p-4">
									<span class="text-3xl">🗺</span>
									<h2 class="font-bold mt-2">Select a component</h2>
									<p class="text-xs opacity-60">Click any node on the graph to inspect its stacks and watch configurations.</p>
								</div>
							}
						>
							{(comp) => (
								<div class="space-y-4">
									<div>
										<span class="text-xs font-bold text-primary uppercase tracking-wide">
											Component Details
										</span>
										<h2 class="text-lg font-extrabold">{comp().id}</h2>
									</div>

									{/* Member Stacks list */}
									<div class="space-y-2">
										<h3 class="text-xs font-bold opacity-70">Stacks ({comp().stacks.length})</h3>
										<div class="bg-base-100 border border-base-300 rounded-field p-2 space-y-1 max-h-48 overflow-auto">
											<For each={comp().stacks}>
												{(s) => <div class="text-xs font-mono truncate">{s}</div>}
											</For>
										</div>
									</div>

									{/* Watch Patterns List */}
									<div class="space-y-2">
										<h3 class="text-xs font-bold opacity-70">
											Watch triggers ({comp().watches.length})
										</h3>
										<div class="bg-base-100 border border-base-300 rounded-field p-2 space-y-1 max-h-48 overflow-auto">
											<Show
												when={comp().watches.length > 0}
												fallback={<div class="text-xs opacity-50 italic">No watch globs</div>}
											>
												<For each={comp().watches}>
													{(w) => <div class="text-xs font-mono truncate">{w}</div>}
												</For>
											</Show>
										</div>
									</div>

									{/* Dynamic Causality Links */}
									<div class="space-y-2">
										<h3 class="text-xs font-bold opacity-70">Connected components</h3>
										<div class="space-y-1">
											<For
												each={catalog()?.edges.filter(
													(e) => e.from === comp().id || e.to === comp().id
												)}
											>
												{(edge) => {
													const other =
														edge.from === comp().id ? edge.to : edge.from;
													const dir = edge.from === comp().id ? "➜" : "←";
													return (
														<button
															class="btn btn-ghost btn-xs w-full justify-start text-xs font-mono truncate"
															onClick={() => setSelected(other)}
														>
															<span class="text-primary mr-1">{dir}</span> {other}
														</button>
													);
												}}
											</For>
										</div>
									</div>
								</div>
							)}
						</Show>
					</div>
				</div>
			</Suspense>
		</div>
	);
}
