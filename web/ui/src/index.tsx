/* @refresh reload */
import { render } from "solid-js/web";
import { Route, Router } from "@solidjs/router";
import "./app.css";
import { loadThemeMode, applyTheme } from "./theme";
import { Shell } from "./Shell";
import { Prs } from "./pages/Prs";
import { Ops } from "./pages/Ops";
import { PrView } from "./pages/PrView";
import { ExecutionView } from "./pages/ExecutionView";

applyTheme(loadThemeMode()); // apply persisted theme before first paint

render(
  () => (
    <Router root={Shell}>
      <Route path="/" component={Prs} />
      <Route path="/ops" component={Ops} />
      <Route path="/pr/:n" component={PrView} />
      <Route path="/t/:tier/e/:id" component={ExecutionView} />
    </Router>
  ),
  document.getElementById("root")!,
);
