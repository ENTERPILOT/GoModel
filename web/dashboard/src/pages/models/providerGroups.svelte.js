// Which provider groups on the Models page are collapsed. Remembered per
// browser so a large inventory stays folded across visits.

import { readStored, writeStored } from "$lib/utils/storage.js";
import {
  COLLAPSED_PROVIDER_GROUPS_STORAGE_KEY,
  parseCollapsedGroups,
  serializeCollapsedGroups,
  toggleCollapsedGroup,
} from "./providerGroupCollapse.js";

class ProviderGroupsState {
  collapsedKeys = $state(
    parseCollapsedGroups(readStored(COLLAPSED_PROVIDER_GROUPS_STORAGE_KEY)),
  );

  isCollapsed(group) {
    return Boolean(group && this.collapsedKeys.includes(group.key));
  }

  toggle(group) {
    if (!group || !group.key) return;
    this.collapsedKeys = toggleCollapsedGroup(this.collapsedKeys, group.key);
    writeStored(
      COLLAPSED_PROVIDER_GROUPS_STORAGE_KEY,
      serializeCollapsedGroups(this.collapsedKeys),
    );
  }
}

export const providerGroups = new ProviderGroupsState();
