import test from "node:test";
import assert from "node:assert/strict";

import {
  clampConversationPanelWidth,
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
