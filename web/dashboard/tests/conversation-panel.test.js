import test from "node:test";
import assert from "node:assert/strict";

import {
  clampConversationPanelWidth,
  conversationAnchorScrollTop,
  conversationMessageNavigationTarget,
  conversationPinnedToBottom,
  conversationOpensFullscreen,
  conversationPanelBounds,
  conversationPanelWidthFromPointer,
} from "../src/pages/audit-logs/conversation-panel.js";

test("desktop panel bounds preserve usable content and panel widths", () => {
  assert.deepEqual(conversationPanelBounds(1440, 246), { min: 320, max: 834 });
  assert.equal(clampConversationPanelWidth(520, 1440, 246), 520);
  assert.equal(clampConversationPanelWidth(1000, 1440, 246), 834);
});

test("dragging the left edge shares width with content", () => {
  assert.equal(conversationPanelWidthFromPointer(900, 1440, 246), 540);
  assert.equal(conversationPanelWidthFromPointer(1100, 1440, 246), 340);
  assert.equal(conversationPanelWidthFromPointer(1300, 1440, 246), 320);
});

test("compact shells keep both the panel and a content strip visible", () => {
  const bounds = conversationPanelBounds(375, 60);
  assert.deepEqual(bounds, { min: 182, max: 187 });
  assert.equal(clampConversationPanelWidth(520, 375, 60), 187);
});

test("panel bounds stay continuous across the former compact breakpoint", () => {
  assert.deepEqual(conversationPanelBounds(679), { min: 320, max: 442 });
  assert.deepEqual(conversationPanelBounds(680), { min: 320, max: 443 });
  assert.equal(clampConversationPanelWidth(520, 679), 442);
  assert.equal(clampConversationPanelWidth(520, 680), 443);
});

test("message navigation steps from the message nearest the viewport top", () => {
  const tops = [-80, 40, 180, 320];
  assert.deepEqual(conversationMessageNavigationTarget(tops, 20, 300, 1), {
    index: 1,
    align: "start",
  });
  assert.deepEqual(conversationMessageNavigationTarget(tops, 20, 300, -1), {
    index: 0,
    align: "start",
  });
  assert.deepEqual(conversationMessageNavigationTarget(tops, 185, 300, 1), {
    index: 3,
    align: "start",
  });
  assert.deepEqual(conversationMessageNavigationTarget(tops, 185, 400, 1), {
    index: 3,
    align: "end",
  });
  assert.deepEqual(conversationMessageNavigationTarget(tops, 185, 400, -1), {
    index: 1,
    align: "start",
  });
  assert.deepEqual(conversationMessageNavigationTarget(tops, 400, 500, 1), {
    index: 3,
    align: "end",
  });
});

test("the drawer opens fullscreen only on phone-width viewports", () => {
  assert.equal(conversationOpensFullscreen(390), true);
  assert.equal(conversationOpensFullscreen(768), true);
  assert.equal(conversationOpensFullscreen(769), false);
  assert.equal(conversationOpensFullscreen(1440), false);
  assert.equal(conversationOpensFullscreen(undefined), true);
});

test("a followed transcript stays pinned only while the view sits near the bottom", () => {
  // scrollHeight 1000, viewport 400: at the bottom (600) and just above it.
  assert.equal(conversationPinnedToBottom(600, 1000, 400), true);
  assert.equal(conversationPinnedToBottom(560, 1000, 400), true);
  // Scrolled up to read history: appends must leave the view alone.
  assert.equal(conversationPinnedToBottom(500, 1000, 400), false);
  assert.equal(conversationPinnedToBottom(0, 1000, 400), false);
  // Content shorter than the viewport is always pinned.
  assert.equal(conversationPinnedToBottom(0, 300, 400), true);
});

test("anchor scrolling centers the message inside the drawer's own container", () => {
  // Container at y=100 scrolled to 200; target at y=700, 100px tall, viewport 400.
  assert.equal(conversationAnchorScrollTop(200, 400, 100, 700, 100), 650);
  // Never above the top.
  assert.equal(conversationAnchorScrollTop(0, 400, 100, 120, 40), 0);
});
