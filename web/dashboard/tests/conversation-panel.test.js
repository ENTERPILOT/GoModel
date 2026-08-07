import test from "node:test";
import assert from "node:assert/strict";

import {
  clampConversationPanelWidth,
  conversationMessageNavigationTarget,
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
