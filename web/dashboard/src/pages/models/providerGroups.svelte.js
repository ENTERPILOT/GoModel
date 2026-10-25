// Which provider groups on the Models page are collapsed. Remembered per
// browser so a large inventory stays folded across visits.

import { readStored, writeStored } from "$lib/utils/storage.js";
import {
  COLLAPSED_PROVIDER_GROUPS_STORAGE_KEY,
  allGroupsCollapsed,
  parseCollapsedGroups,
  serializeCollapsedGroups,
  setGroupsCollapsed,
  toggleCollapsedGroup,
} from "./providerGroupCollapse.js";

function groupKeys(groups) {
  return (groups || []).map((group) => group?.key);
}

class ProviderGroupsState {
  collapsedKeys = $state(
    parseCollapsedGroups(readStored(COLLAPSED_PROVIDER_GROUPS_STORAGE_KEY)),
  );

  isCollapsed(group) {
    return Boolean(group && this.collapsedKeys.includes(group.key));
  }

  allCollapsed(groups) {
    return allGroupsCollapsed(this.collapsedKeys, groupKeys(groups));
  }

  toggle(group) {
    if (!group || !group.key) return;
    this.#save(toggleCollapsedGroup(this.collapsedKeys, group.key));
  }

  setAll(groups, collapsed) {
    this.#save(setGroupsCollapsed(this.collapsedKeys, groupKeys(groups), collapsed));
  }

  #save(keys) {
    this.collapsedKeys = keys;
    writeStored(
      COLLAPSED_PROVIDER_GROUPS_STORAGE_KEY,
      serializeCollapsedGroups(this.collapsedKeys),
    );
  }
}

export const providerGroups = new ProviderGroupsState();
