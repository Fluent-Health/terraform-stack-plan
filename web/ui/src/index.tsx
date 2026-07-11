/* @refresh reload */
import { render } from "solid-js/web";
import { Route, Router } from "@solidjs/router";
import "./app.css";
import { Shell } from "./Shell";
import { Home } from "./pages/Home";
import { PrView } from "./pages/PrView";
import { ExecutionView } from "./pages/ExecutionView";
import { Approvals } from "./pages/Approvals";

render(
  () => (
    <Router root={Shell}>
      <Route path="/" component={Home} />
      <Route path="/pr/:n" component={PrView} />
      <Route path="/t/:tier/e/:id" component={ExecutionView} />
      <Route path="/approvals" component={Approvals} />
    </Router>
  ),
  document.getElementById("root")!,
);
