import { mount } from "svelte";
import "./app.css";
import App from "./App.svelte";
import { measureLockGutter } from "$lib/gutter";

measureLockGutter();
mount(App, { target: document.getElementById("app")! });
