import { mount } from "svelte";
import "./app.css";
import App from "./App.svelte";
import { backendKind } from "./lib/api";

// The desktop shell uses a transparent window so the frosted sidebar can sample
// the wallpaper. In a plain browser there is no compositor behind the page, so
// paint the base colour instead of leaving the white canvas exposed.
if (backendKind === "mock") {
  document.documentElement.classList.add("browser-mock");
}

const target = document.getElementById("app");
if (!target) {
  throw new Error("posthaste: missing #app mount element");
}
mount(App, { target });
