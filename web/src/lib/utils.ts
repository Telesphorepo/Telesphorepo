import { createCn } from "cn/config";

/**
 * Joins classes and drops the ones a later class overrides. The type scale of app.css (text-title,
 * text-section, text-label, text-body, text-meta) is a font size, so `cn("text-meta", "text-background")`
 * keeps both: without this, a type-scale class would be read as a color and remove the color.
 */
export const cn = createCn({
  extend: { classGroups: { "font-size": [{ text: ["title", "section", "label", "body", "meta"] }] } },
});

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type WithoutChild<T> = T extends { child?: any } ? Omit<T, "child"> : T;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type WithoutChildren<T> = T extends { children?: any } ? Omit<T, "children"> : T;
export type WithoutChildrenOrChild<T> = WithoutChildren<WithoutChild<T>>;
export type WithElementRef<T, U extends HTMLElement = HTMLElement> = T & { ref?: U | null };
