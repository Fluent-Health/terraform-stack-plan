import { A } from "@solidjs/router";
import { ErrorBoundary, ParentProps, Show, Suspense, createResource } from "solid-js";
import { Unauthorized, api, loginURL } from "./api/client";

/**
 * Shell is the app frame: navbar + login gate. /api/me decides whether a
 * session exists; a 401 anywhere in the tree falls back to the sign-in card
 * (the backend holds all auth — the SPA only reacts to it).
 */
export function Shell(props: ParentProps) {
  const [me] = createResource(() => api.me().catch((e) => (e instanceof Unauthorized ? null : Promise.reject(e))));
  return (
    <div class="min-h-screen bg-base-200">
      <div class="navbar bg-base-100 shadow-sm px-4">
        <div class="flex-1 gap-4">
          <A href="/" class="text-lg font-bold">
            tfstackplan
          </A>
          <A href="/approvals" class="link link-hover text-sm">
            approvals
          </A>
        </div>
        <Show when={me()}>
          {(m) => (
            <div class="flex items-center gap-3 text-sm opacity-80">
              <span>{m().email}</span>
              <button
                class="btn btn-ghost btn-xs"
                onClick={() => api.logout().then(() => location.assign("/"))}
              >
                sign out
              </button>
            </div>
          )}
        </Show>
      </div>
      <main class="p-4 max-w-6xl mx-auto">
        <Suspense fallback={<span class="loading loading-dots loading-md" />}>
          <Show when={me.loading || me()} fallback={<SignIn />}>
            <ErrorBoundary
              fallback={(err) =>
                err instanceof Unauthorized ? <SignIn /> : <div class="alert alert-error">{String(err)}</div>
              }
            >
              {props.children}
            </ErrorBoundary>
          </Show>
        </Suspense>
      </main>
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
