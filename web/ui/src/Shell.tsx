import { A, useNavigate } from "@solidjs/router";
import { ErrorBoundary, ParentProps, Show, Suspense, createResource } from "solid-js";
import { Unauthorized, api, loginURL } from "./api/client";
import { ThemeSwitch } from "./components/ThemeSwitch";

/**
 * Shell is the app frame: a persistent left nav rail + a login gate. /api/me
 * decides whether a session exists; a 401 anywhere in the tree falls back to the
 * sign-in card (the backend holds all auth — the SPA only reacts to it).
 */
export function Shell(props: ParentProps) {
  const [me] = createResource(() =>
    api.me().catch((e) => (e instanceof Unauthorized ? null : Promise.reject(e))),
  );
  const navigate = useNavigate();
  const jump = (e: SubmitEvent) => {
    e.preventDefault();
    const n = new FormData(e.target as HTMLFormElement).get("pr")?.toString().trim();
    if (n && /^\d+$/.test(n)) navigate(`/pr/${n}`);
  };

  return (
    <div class="min-h-screen bg-base-300 text-base-content">
      <Show
        when={me.loading || me()}
        fallback={
          <main class="p-4">
            <Suspense fallback={<span class="loading loading-dots loading-md" />}>
              <SignIn />
            </Suspense>
          </main>
        }
      >
        <div class="grid" style="grid-template-columns:196px 1fr; min-height:100vh">
          <aside class="bg-base-200 border-r border-base-300 p-3 flex flex-col gap-1">
            <A href="/" class="font-bold text-sm px-2 py-1 mb-3">
              ▸ tfstack<span class="text-primary">plan</span>
            </A>
            <A href="/" class="btn btn-ghost btn-sm justify-start" activeClass="btn-active">
              🔀 PRs
            </A>
            <A href="/ops" class="btn btn-ghost btn-sm justify-start" activeClass="btn-active">
              🛠 Ops board
            </A>
            <A href="/catalog" class="btn btn-ghost btn-sm justify-start" activeClass="btn-active">
              🗺 Component Catalog
            </A>
            <form onSubmit={jump} class="mt-2 px-1">
              <input name="pr" class="input input-xs input-bordered w-full" placeholder="Jump to PR #…" />
            </form>
            <div class="mt-auto flex flex-col gap-2 px-1">
              <ThemeSwitch />
              <Show when={me()}>
                {(m) => (
                  <div class="flex items-center gap-2 text-xs opacity-80">
                    <span class="truncate">{m().email}</span>
                    <button
                      class="btn btn-ghost btn-xs ml-auto"
                      onClick={() => api.logout().then(() => location.assign("/"))}
                    >
                      sign out
                    </button>
                  </div>
                )}
              </Show>
            </div>
          </aside>
          <main class="p-6 overflow-auto">
            <ErrorBoundary
              fallback={(err) =>
                err instanceof Unauthorized ? <SignIn /> : <div class="alert alert-error">{String(err)}</div>
              }
            >
              {props.children}
            </ErrorBoundary>
          </main>
        </div>
      </Show>
    </div>
  );
}

function SignIn() {
  return (
    <div class="card bg-base-100 max-w-md mx-auto mt-24 shadow">
      <div class="card-body items-center text-center">
        <h2 class="card-title">tfstackplan</h2>
        <p class="opacity-70">Sign in with your workspace Google account.</p>
        <a class="btn btn-primary" href={loginURL()}>
          Sign in with Google
        </a>
      </div>
    </div>
  );
}
