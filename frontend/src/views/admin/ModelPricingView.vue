<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="space-y-3">
          <!-- Primary Toolbar (matches ChannelsView / GroupsView layout & components) -->
          <div class="flex flex-col justify-between gap-4 lg:flex-row lg:items-start">
            <!-- Left: Search + System Group + Platform + Status Filters -->
            <div class="flex flex-1 flex-wrap items-center gap-3">
              <div class="relative w-full sm:w-64">
                <Icon
                  name="search"
                  size="md"
                  class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500"
                />
                <input
                  v-model="searchQuery"
                  type="text"
                  :placeholder="t('admin.modelPricing.searchPlaceholder')"
                  class="input pl-10"
                  @input="handleSearch"
                />
              </div>

              <!-- System Group Selector -->
              <Select
                v-model="selectedGroupId"
                :options="groupSelectOptions"
                :placeholder="t('admin.modelPricing.groupScopePlaceholder')"
                searchable
                class="w-full sm:w-60"
                @change="handleGroupChange"
              >
                <template #selected="{ option }">
                  <span v-if="option" class="inline-flex items-center gap-1.5 truncate">
                    <PlatformIcon
                      v-if="option.platform"
                      :platform="String(option.platform)"
                      size="xs"
                    />
                    <span class="truncate">{{ option.label }}</span>
                    <span
                      v-if="Number(option.overrideCount || 0) > 0"
                      class="rounded-full bg-primary-100 px-1.5 py-0.5 text-[11px] font-medium text-primary-700 dark:bg-primary-900/40 dark:text-primary-300"
                    >
                      {{ option.overrideCount }}
                    </span>
                  </span>
                </template>
                <template #option="{ option, selected }">
                  <div class="flex min-w-0 flex-1 items-center justify-between gap-2">
                    <span class="inline-flex items-center gap-1.5 truncate">
                      <PlatformIcon
                        v-if="option.platform"
                        :platform="String(option.platform)"
                        size="xs"
                      />
                      <span class="truncate">{{ option.label }}</span>
                    </span>
                    <div class="flex flex-shrink-0 items-center gap-1.5">
                      <span
                        v-if="Number(option.overrideCount || 0) > 0"
                        class="rounded-full bg-primary-100 px-1.5 py-0.5 text-[11px] font-medium text-primary-700 dark:bg-primary-900/40 dark:text-primary-300"
                      >
                        {{ option.overrideCount }}
                      </span>
                      <Icon
                        v-if="selected"
                        name="check"
                        size="sm"
                        class="text-primary-500"
                        :stroke-width="2"
                      />
                    </div>
                  </div>
                </template>
              </Select>

              <!-- System Platform Filter (auto-locked when single-platform group is active) -->
              <Select
                v-model="selectedPlatform"
                :options="platformSelectOptions"
                :placeholder="t('admin.modelPricing.platformPlaceholder')"
                :disabled="isPlatformLockedByGroup"
                class="w-full sm:w-44"
                @change="handleFilterChange"
              >
                <template #selected="{ option }">
                  <span v-if="option" class="inline-flex items-center gap-1.5 truncate">
                    <PlatformIcon
                      v-if="option.value && option.value !== 'all'"
                      :platform="String(option.value)"
                      size="xs"
                    />
                    <span class="truncate">{{ option.label }}</span>
                  </span>
                </template>
                <template #option="{ option, selected }">
                  <div class="flex min-w-0 flex-1 items-center justify-between gap-2">
                    <span class="inline-flex items-center gap-1.5 truncate">
                      <PlatformIcon
                        v-if="option.value && option.value !== 'all'"
                        :platform="String(option.value)"
                        size="xs"
                      />
                      <span class="truncate">{{ option.label }}</span>
                    </span>
                    <Icon
                      v-if="selected"
                      name="check"
                      size="sm"
                      class="text-primary-500"
                      :stroke-width="2"
                    />
                  </div>
                </template>
              </Select>

              <!-- Status Filter -->
              <Select
                v-model="statusFilter"
                :options="statusFilterOptions"
                :placeholder="t('admin.modelPricing.filterPlaceholder')"
                class="w-full sm:w-40"
                @change="handleFilterChange"
              />
            </div>

            <!-- Right: Sync Settings, Sync Now (with confirmation), Add Custom Model -->
            <div class="flex w-full flex-shrink-0 flex-wrap items-center justify-end gap-3 lg:w-auto">
              <button
                type="button"
                @click="openSyncSettingsDialog"
                class="btn btn-secondary"
              >
                <Icon name="cog" size="md" class="mr-2" />
                {{ t('admin.modelPricing.syncSettings') }}
              </button>
              <button
                type="button"
                @click="openSyncConfirmDialog"
                :disabled="syncing"
                class="btn btn-secondary"
              >
                <Icon name="refresh" size="md" class="mr-2" :class="syncing ? 'animate-spin' : ''" />
                {{ syncing ? t('admin.modelPricing.syncing') : t('admin.modelPricing.syncNow') }}
              </button>
              <button
                type="button"
                @click="openCreateDialog"
                class="btn btn-primary"
              >
                <Icon name="plus" size="md" class="mr-2" />
                {{ t('admin.modelPricing.addCustomModel') }}
              </button>
            </div>
          </div>

          <!-- Compact Sync & Precedence Info Strip -->
          <div class="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-gray-200 bg-gray-50/70 px-4 py-2.5 text-xs text-gray-600 dark:border-dark-700 dark:bg-dark-800/60 dark:text-gray-400">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span class="inline-flex items-center gap-1.5 font-medium text-gray-700 dark:text-gray-200">
                <span
                  :class="[
                    'h-2 w-2 rounded-full',
                    syncStatus?.config.auto_sync_enabled ? 'bg-emerald-500' : 'bg-gray-400'
                  ]"
                />
                {{
                  syncStatus?.config.auto_sync_enabled
                    ? t('admin.modelPricing.autoSyncOn', { hours: syncStatus.config.sync_interval_hours })
                    : t('admin.modelPricing.autoSyncOff')
                }}
              </span>
              <span v-if="syncStatus?.meta">
                {{
                  t('admin.modelPricing.syncSummary', {
                    models: syncStatus.meta.model_count,
                    channels: syncStatus.meta.channel_count
                  })
                }}
              </span>
              <span
                v-if="syncStatus && syncStatus.locked_channel_count > 0"
                class="inline-flex items-center gap-1 font-medium text-amber-700 dark:text-amber-300"
              >
                <Icon name="lock" size="xs" />
                {{ t('admin.modelPricing.lockedSummary', { count: syncStatus.locked_channel_count }) }}
              </span>
              <button
                v-if="syncStatus && syncStatus.missing_pricing_count > 0"
                type="button"
                @click="filterByMissingPricing"
                class="inline-flex items-center gap-1 rounded-full bg-rose-100 px-2 py-0.5 font-medium text-rose-700 transition-colors hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-300 dark:hover:bg-rose-900/50"
              >
                <Icon name="exclamationCircle" size="xs" />
                {{ t('admin.modelPricing.missingSummary', { count: syncStatus.missing_pricing_count }) }}
              </button>
              <span v-if="syncStatus?.meta.last_sync_at">
                {{ t('admin.modelPricing.lastSyncAt', { time: formatTime(syncStatus.meta.last_sync_at) }) }}
              </span>
              <span v-else>{{ t('admin.modelPricing.neverSynced') }}</span>
              <span
                v-if="syncStatus?.meta.last_sync_status === 'failed' && syncStatus.meta.last_sync_error"
                class="text-red-600 dark:text-red-400"
              >
                {{ t('admin.modelPricing.syncFailed', { error: syncStatus.meta.last_sync_error }) }}
              </span>
            </div>

            <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span v-if="selectedGroup" class="font-medium text-primary-600 dark:text-primary-400">
                {{ t('admin.modelPricing.groupScopeHint', { name: selectedGroup.name }) }}
              </span>
              <span>{{ t('admin.modelPricing.priorityHint') }}</span>
            </div>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="tableRows"
          :loading="loading"
          row-key="_rowKey"
          :row-class="getTableRowClass"
        >
          <!-- Column 1: Model & Default Channel (Level 1) / Other Channels (Level 2) -->
          <template #cell-model="{ row }">
            <!-- Level 1: Model + Default Channel Row -->
            <div v-if="row._rowType === 'model'" class="flex max-w-[360px] items-start gap-2 whitespace-normal">
              <button
                v-if="getOtherChannels(row.item).length > 0"
                type="button"
                @click="toggleModelExpand(row.item.model_id)"
                class="mt-0.5 flex h-5 w-5 flex-shrink-0 items-center justify-center rounded text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                :aria-expanded="isModelExpanded(row.item.model_id)"
                :title="
                  isModelExpanded(row.item.model_id)
                    ? t('admin.modelPricing.actions.collapseChannels')
                    : t('admin.modelPricing.actions.expandChannels')
                "
              >
                <Icon
                  name="chevronRight"
                  size="sm"
                  :class="['transition-transform duration-150', isModelExpanded(row.item.model_id) && 'rotate-90']"
                />
              </button>
              <span v-else-if="hasAnyExpandableModel" class="w-5 flex-shrink-0" />

              <div class="min-w-0 flex-1 space-y-1">
                <div class="flex flex-wrap items-center gap-1.5">
                  <span class="break-all font-mono text-sm font-medium text-gray-900 dark:text-white">
                    {{ row.item.model_id }}
                  </span>
                  <span :class="sourceBadgeClass(row.item.source)">
                    {{ t('admin.modelPricing.sources.' + row.item.source) }}
                  </span>
                  <button
                    v-if="getOtherChannels(row.item).length > 0"
                    type="button"
                    @click="toggleModelExpand(row.item.model_id)"
                    class="inline-flex items-center gap-1 rounded-md border border-gray-200 bg-gray-100 px-1.5 py-0.5 text-[11px] font-medium text-gray-600 transition-colors hover:border-primary-300 hover:bg-primary-50 hover:text-primary-700 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-300 dark:hover:border-primary-700 dark:hover:bg-primary-900/30 dark:hover:text-primary-300"
                  >
                    {{ t('admin.modelPricing.badges.otherChannelCount', { count: getOtherChannels(row.item).length }) }}
                  </button>
                  <span
                    v-if="row.item.locked_channel_count > 0"
                    class="inline-flex items-center gap-0.5 rounded-full bg-amber-100 px-1.5 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
                  >
                    <Icon name="lock" size="xs" />
                    {{ t('admin.modelPricing.badges.lockedCount', { count: row.item.locked_channel_count }) }}
                  </span>
                  <span
                    v-if="row.item.is_custom_only"
                    class="inline-flex items-center rounded-full bg-purple-100 px-2 py-0.5 text-[11px] font-medium text-purple-700 dark:bg-purple-900/30 dark:text-purple-300"
                  >
                    {{ t('admin.modelPricing.badges.customOnly') }}
                  </span>
                  <span
                    v-if="selectedGroupId === 0 && row.item.group_override_count > 0"
                    class="inline-flex items-center rounded-full bg-primary-100 px-2 py-0.5 text-[11px] font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                  >
                    {{ t('admin.modelPricing.badges.groupOverrides', { count: row.item.group_override_count }) }}
                  </span>
                </div>

                <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                  <span
                    v-if="!row.item.is_custom_only && row.item.default_channel_name"
                    class="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[11px] font-medium text-gray-700 dark:bg-dark-700 dark:text-gray-300"
                  >
                    {{ t('admin.modelPricing.badges.defaultChannelSource', { channel: row.item.default_channel_name }) }}
                  </span>
                  <span
                    v-if="getDefaultChannelRawModelId(row.item)"
                    class="truncate font-mono text-[11px] text-gray-400 dark:text-gray-500"
                    :title="getDefaultChannelRawModelId(row.item)"
                  >
                    {{ getDefaultChannelRawModelId(row.item) }}
                  </span>
                  <span
                    v-if="row.item.display_name && row.item.display_name !== row.item.model_id"
                    class="truncate"
                    :title="row.item.display_name"
                  >
                    {{ row.item.display_name }}
                  </span>
                  <span
                    v-if="row.item.effective_pricing.long_context_threshold"
                    class="rounded bg-amber-50 px-1.5 py-0.5 font-mono text-[11px] text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
                  >
                    {{
                      t('admin.modelPricing.badges.longContext', {
                        tokens: Math.round(row.item.effective_pricing.long_context_threshold / 1000),
                        inMult: row.item.effective_pricing.long_context_input_multiplier || 1,
                        outMult: row.item.effective_pricing.long_context_output_multiplier || 1
                      })
                    }}
                  </span>
                </div>
              </div>
            </div>

            <!-- Level 2: Non-Default Account-Type Channel Sub-Row -->
            <div
              v-else
              class="ml-2.5 flex max-w-[340px] flex-col gap-0.5 border-l-2 border-gray-200 pl-4 whitespace-normal dark:border-dark-600"
            >
              <div class="flex flex-wrap items-center gap-1.5">
                <span class="text-xs font-medium text-gray-800 dark:text-gray-100">
                  {{ row.channel.channel_name }}
                </span>
                <span
                  v-if="row.channel.is_overridden"
                  class="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
                >
                  {{ t('admin.modelPricing.badges.channelCustom') }}
                </span>
              </div>
              <div
                class="truncate font-mono text-[11px] text-gray-500 dark:text-gray-400"
                :title="row.channel.raw_model_id"
              >
                {{ row.channel.raw_model_id }}
              </div>
            </div>
          </template>

          <!-- Column 2: Platform + Default Lock Status (Level 1) / Channel Lock Status (Level 2) -->
          <template #cell-platform="{ row }">
            <div v-if="row._rowType === 'model'" class="flex flex-col items-start gap-1">
              <span
                :class="[
                  'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium',
                  platformBadgeLightClass(row.item.platform)
                ]"
              >
                <PlatformIcon :platform="row.item.platform" size="xs" />
                {{ t('admin.groups.platforms.' + row.item.platform, platformDisplayName(row.item.platform)) }}
              </span>
              <span
                v-if="selectedGroupId === 0 && !row.item.is_custom_only && !row.item.is_pricing_missing"
                :class="[
                  'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium',
                  row.item.is_default_locked
                    ? 'bg-amber-100/90 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
                    : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
                ]"
              >
                <Icon :name="row.item.is_default_locked ? 'lock' : 'unlock'" size="xs" />
                <span>
                  {{ row.item.is_default_locked ? t('admin.modelPricing.lock.locked') : t('admin.modelPricing.lock.unlocked') }}
                </span>
              </span>
            </div>

            <span
              v-else
              :class="[
                'inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium',
                row.channel.is_locked
                  ? 'bg-amber-100/90 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
              ]"
            >
              <Icon :name="row.channel.is_locked ? 'lock' : 'unlock'" size="xs" />
              <span>
                {{ row.channel.is_locked ? t('admin.modelPricing.lock.locked') : t('admin.modelPricing.lock.unlocked') }}
              </span>
            </span>
          </template>

          <!-- Column 3: Input Price -->
          <template #cell-input_price="{ row }">
            <template v-if="row._rowType === 'model'">
              <span v-if="row.item.is_pricing_missing" class="font-mono text-sm text-gray-400 dark:text-gray-500">—</span>
              <template v-else>
                <div class="font-mono text-sm font-medium text-gray-900 dark:text-white">
                  ${{ formatPrice(row.item.effective_pricing.input_per_million) }}
                </div>
                <div
                  v-if="row.item.is_group_overridden && row.item.default_pricing"
                  class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
                >
                  {{ t('admin.modelPricing.badges.defaultRef', { value: formatPrice(row.item.default_pricing.input_per_million) }) }}
                </div>
                <div
                  v-else-if="selectedGroupId === 0 && hasSyncedPriceDiff(row.item.effective_pricing.input_per_million, getDefaultChannelSyncedPricing(row.item)?.input_per_million)"
                  class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
                >
                  {{ t('admin.modelPricing.badges.syncedRef', { value: formatPrice(getDefaultChannelSyncedPricing(row.item)?.input_per_million) }) }}
                </div>
              </template>
            </template>
            <template v-else>
              <div class="font-mono text-xs font-medium text-gray-800 dark:text-gray-100">
                ${{ formatPrice(row.channel.pricing.input_per_million) }}
              </div>
              <div
                v-if="hasSyncedPriceDiff(row.channel.pricing.input_per_million, row.channel.synced_pricing?.input_per_million)"
                class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
              >
                {{ t('admin.modelPricing.badges.syncedRef', { value: formatPrice(row.channel.synced_pricing?.input_per_million) }) }}
              </div>
            </template>
          </template>

          <!-- Column 4: Output Price -->
          <template #cell-output_price="{ row }">
            <template v-if="row._rowType === 'model'">
              <span v-if="row.item.is_pricing_missing" class="font-mono text-sm text-gray-400 dark:text-gray-500">—</span>
              <template v-else>
                <div class="font-mono text-sm font-medium text-gray-900 dark:text-white">
                  ${{ formatPrice(row.item.effective_pricing.output_per_million) }}
                </div>
                <div
                  v-if="row.item.is_group_overridden && row.item.default_pricing"
                  class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
                >
                  {{ t('admin.modelPricing.badges.defaultRef', { value: formatPrice(row.item.default_pricing.output_per_million) }) }}
                </div>
                <div
                  v-else-if="selectedGroupId === 0 && hasSyncedPriceDiff(row.item.effective_pricing.output_per_million, getDefaultChannelSyncedPricing(row.item)?.output_per_million)"
                  class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
                >
                  {{ t('admin.modelPricing.badges.syncedRef', { value: formatPrice(getDefaultChannelSyncedPricing(row.item)?.output_per_million) }) }}
                </div>
              </template>
            </template>
            <template v-else>
              <div class="font-mono text-xs font-medium text-gray-800 dark:text-gray-100">
                ${{ formatPrice(row.channel.pricing.output_per_million) }}
              </div>
              <div
                v-if="hasSyncedPriceDiff(row.channel.pricing.output_per_million, row.channel.synced_pricing?.output_per_million)"
                class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
              >
                {{ t('admin.modelPricing.badges.syncedRef', { value: formatPrice(row.channel.synced_pricing?.output_per_million) }) }}
              </div>
            </template>
          </template>

          <!-- Column 5: Cache Read -->
          <template #cell-cache_read="{ row }">
            <template v-if="row._rowType === 'model'">
              <span v-if="row.item.is_pricing_missing" class="font-mono text-sm text-gray-400 dark:text-gray-500">—</span>
              <span v-else class="font-mono text-sm text-gray-700 dark:text-gray-300">
                ${{ formatPrice(row.item.effective_pricing.cache_read_per_million) }}
              </span>
            </template>
            <template v-else>
              <span class="font-mono text-xs text-gray-700 dark:text-gray-300">
                ${{ formatPrice(row.channel.pricing.cache_read_per_million) }}
              </span>
            </template>
          </template>

          <!-- Column 6: Cache Write -->
          <template #cell-cache_write="{ row }">
            <template v-if="row._rowType === 'model'">
              <span v-if="row.item.is_pricing_missing" class="font-mono text-sm text-gray-400 dark:text-gray-500">—</span>
              <template v-else>
                <div class="font-mono text-sm text-gray-700 dark:text-gray-300">
                  ${{ formatPrice(row.item.effective_pricing.cache_write_per_million) }}
                </div>
                <div
                  v-if="row.item.effective_pricing.cache_write_1h_per_million !== undefined && row.item.effective_pricing.cache_write_1h_per_million !== null"
                  class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
                >
                  {{ t('admin.modelPricing.badges.cache1h', { value: formatPrice(row.item.effective_pricing.cache_write_1h_per_million) }) }}
                </div>
              </template>
            </template>
            <template v-else>
              <div class="font-mono text-xs text-gray-700 dark:text-gray-300">
                ${{ formatPrice(row.channel.pricing.cache_write_per_million) }}
              </div>
              <div
                v-if="row.channel.pricing.cache_write_1h_per_million !== undefined && row.channel.pricing.cache_write_1h_per_million !== null"
                class="font-mono text-[11px] text-gray-400 dark:text-gray-500"
              >
                {{ t('admin.modelPricing.badges.cache1h', { value: formatPrice(row.channel.pricing.cache_write_1h_per_million) }) }}
              </div>
            </template>
          </template>

          <!-- Column 7: Unified Actions (consistent icon-above-label style across Level 1 & Level 2) -->
          <template #cell-actions="{ row }">
            <div v-if="row._rowType === 'model'" class="flex items-center gap-1">
              <button
                v-if="selectedGroupId === 0 && !row.item.is_custom_only && !row.item.is_pricing_missing"
                type="button"
                :disabled="isChannelLockLoading(row.item.model_id, row.item.default_channel_id || '')"
                @click="toggleDefaultLock(row.item)"
                :class="[
                  'flex min-w-[44px] flex-col items-center gap-0.5 rounded-lg px-2 py-1.5 transition-colors disabled:cursor-not-allowed disabled:opacity-50',
                  row.item.is_default_locked
                    ? 'bg-amber-50/80 text-amber-600 hover:bg-amber-100/80 dark:bg-amber-900/20 dark:text-amber-400 dark:hover:bg-amber-900/35'
                    : 'text-gray-500 hover:bg-amber-50 hover:text-amber-600 dark:hover:bg-amber-900/20 dark:hover:text-amber-400'
                ]"
                :title="
                  row.item.is_default_locked
                    ? t('admin.modelPricing.lock.unlockDefaultHint')
                    : t('admin.modelPricing.lock.lockDefaultHint')
                "
              >
                <Icon :name="row.item.is_default_locked ? 'lock' : 'unlock'" size="sm" />
                <span class="text-xs">
                  {{ row.item.is_default_locked ? t('admin.modelPricing.actions.unlock') : t('admin.modelPricing.actions.lock') }}
                </span>
              </button>
              <button
                type="button"
                @click="openEditModelDialog(row.item)"
                class="flex min-w-[44px] flex-col items-center gap-0.5 rounded-lg px-2 py-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              >
                <Icon name="edit" size="sm" />
                <span class="text-xs">{{ t('admin.modelPricing.actions.edit') }}</span>
              </button>
              <button
                v-if="canResetModelRow(row.item)"
                type="button"
                @click="promptResetModel(row.item)"
                class="flex min-w-[44px] flex-col items-center gap-0.5 rounded-lg px-2 py-1.5 text-gray-500 transition-colors hover:bg-amber-50 hover:text-amber-600 dark:hover:bg-amber-900/20 dark:hover:text-amber-400"
              >
                <Icon name="refresh" size="sm" />
                <span class="text-xs">{{ t('admin.modelPricing.actions.reset') }}</span>
              </button>
            </div>

            <div v-else class="flex items-center gap-1">
              <button
                type="button"
                :disabled="isChannelLockLoading(row.item.model_id, row.channel.channel_id)"
                @click="toggleChannelLock(row.item, row.channel)"
                :class="[
                  'flex min-w-[44px] flex-col items-center gap-0.5 rounded-lg px-2 py-1.5 transition-colors disabled:cursor-not-allowed disabled:opacity-50',
                  row.channel.is_locked
                    ? 'bg-amber-50/80 text-amber-600 hover:bg-amber-100/80 dark:bg-amber-900/20 dark:text-amber-400 dark:hover:bg-amber-900/35'
                    : 'text-gray-500 hover:bg-amber-50 hover:text-amber-600 dark:hover:bg-amber-900/20 dark:hover:text-amber-400'
                ]"
                :title="
                  row.channel.is_locked
                    ? t('admin.modelPricing.lock.unlockHint')
                    : t('admin.modelPricing.lock.lockHint')
                "
              >
                <Icon :name="row.channel.is_locked ? 'lock' : 'unlock'" size="sm" />
                <span class="text-xs">
                  {{ row.channel.is_locked ? t('admin.modelPricing.actions.unlock') : t('admin.modelPricing.actions.lock') }}
                </span>
              </button>
              <button
                type="button"
                @click="openEditChannelDialog(row.item, row.channel)"
                class="flex min-w-[44px] flex-col items-center gap-0.5 rounded-lg px-2 py-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              >
                <Icon name="edit" size="sm" />
                <span class="text-xs">{{ t('admin.modelPricing.actions.edit') }}</span>
              </button>
              <button
                v-if="row.channel.is_overridden || row.channel.is_locked"
                type="button"
                @click="promptResetChannel(row.item, row.channel)"
                class="flex min-w-[44px] flex-col items-center gap-0.5 rounded-lg px-2 py-1.5 text-gray-500 transition-colors hover:bg-amber-50 hover:text-amber-600 dark:hover:bg-amber-900/20 dark:hover:text-amber-400"
              >
                <Icon name="refresh" size="sm" />
                <span class="text-xs">{{ t('admin.modelPricing.actions.reset') }}</span>
              </button>
            </div>
          </template>

          <template #empty>
            <EmptyState
              :title="t('admin.modelPricing.empty.title')"
              :description="t('admin.modelPricing.empty.description')"
              :action-text="t('admin.modelPricing.syncNow')"
              :action-icon="false"
              @action="openSyncConfirmDialog"
            />
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="total > 0"
          :page="page"
          :total="total"
          :page-size="pageSize"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <!-- Edit / Add Custom Model or Channel Pricing Dialog -->
    <BaseDialog
      :show="showEditDialog"
      :title="editDialogTitle"
      width="normal"
      @close="showEditDialog = false"
    >
      <form id="model-pricing-edit-form" @submit.prevent="saveEditPricing" class="space-y-4">
        <div class="rounded-lg border border-primary-200 bg-primary-50/70 px-3 py-2 text-xs text-primary-800 dark:border-primary-800/40 dark:bg-primary-900/20 dark:text-primary-200">
          {{ editScopeBanner }}
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label">
              {{ t('admin.modelPricing.editModal.modelId') }} <span class="text-red-500">*</span>
            </label>
            <input
              v-model="editForm.model_id"
              type="text"
              required
              :disabled="!isCreatingModel"
              :placeholder="t('admin.modelPricing.editModal.modelIdPlaceholder')"
              class="input font-mono disabled:opacity-60"
            />
          </div>
          <div v-if="editingChannel">
            <label class="input-label">
              {{ t('admin.modelPricing.editModal.channelLabel') }}
            </label>
            <input
              :value="`${editingChannel.channel_name} (${editingChannel.channel_id})`"
              type="text"
              disabled
              class="input font-mono text-xs disabled:opacity-70"
            />
          </div>
          <div v-else>
            <label class="input-label">
              {{ t('admin.modelPricing.editModal.platform') }}
            </label>
            <Select
              v-model="editForm.platform"
              :options="editPlatformOptions"
              :disabled="!isCreatingModel || isPlatformLockedByGroup"
            />
          </div>
        </div>

        <div v-if="!editingChannel">
          <label class="input-label">
            {{ t('admin.modelPricing.editModal.displayName') }}
          </label>
          <input
            v-model="editForm.display_name"
            type="text"
            :placeholder="t('admin.modelPricing.editModal.displayNamePlaceholder')"
            class="input"
          />
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label">
              {{ t('admin.modelPricing.editModal.inputPerMillion') }}
            </label>
            <input
              v-model.number="editForm.input_per_million"
              type="number"
              min="0"
              step="any"
              class="input font-mono"
            />
          </div>
          <div>
            <label class="input-label">
              {{ t('admin.modelPricing.editModal.outputPerMillion') }}
            </label>
            <input
              v-model.number="editForm.output_per_million"
              type="number"
              min="0"
              step="any"
              class="input font-mono"
            />
          </div>
          <div>
            <label class="input-label">
              {{ t('admin.modelPricing.editModal.cacheReadPerMillion') }}
            </label>
            <input
              v-model.number="editForm.cache_read_per_million"
              type="number"
              min="0"
              step="any"
              class="input font-mono"
            />
          </div>
          <div>
            <label class="input-label">
              {{ t('admin.modelPricing.editModal.cacheWritePerMillion') }}
            </label>
            <input
              v-model.number="editForm.cache_write_per_million"
              type="number"
              min="0"
              step="any"
              class="input font-mono"
            />
          </div>
        </div>

        <!-- Channel-level Auto-Sync Lock Switch (shown for Default & Channel edits, not Group overrides) -->
        <div
          v-if="showEditLockToggle"
          class="flex items-center justify-between gap-4 rounded-xl border border-gray-200 p-3.5 dark:border-dark-700"
        >
          <div class="min-w-0">
            <div class="flex items-center gap-1.5 text-sm font-medium text-gray-900 dark:text-white">
              <Icon name="lock" size="sm" class="text-amber-500" />
              <span>{{ t('admin.modelPricing.editModal.lockLabel') }}</span>
            </div>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.modelPricing.editModal.lockHint') }}
            </p>
          </div>
          <Toggle v-model="editForm.locked" />
        </div>

        <!-- Collapsible Advanced Pricing Options -->
        <div class="rounded-xl border border-gray-200 dark:border-dark-700">
          <button
            type="button"
            @click="showAdvanced = !showAdvanced"
            class="flex w-full items-center justify-between px-3.5 py-2.5 text-left text-xs font-medium text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700/50"
          >
            <span>{{ t('admin.modelPricing.editModal.advancedTitle') }}</span>
            <Icon
              name="chevronDown"
              size="sm"
              :class="['transition-transform duration-200', showAdvanced && 'rotate-180']"
            />
          </button>
          <div
            v-if="showAdvanced"
            class="grid grid-cols-1 gap-4 border-t border-gray-200 p-3.5 dark:border-dark-700 sm:grid-cols-2"
          >
            <div>
              <label class="input-label">
                {{ t('admin.modelPricing.editModal.cacheWrite1hPerMillion') }}
              </label>
              <input
                v-model="editForm.cache_write_1h_str"
                type="number"
                min="0"
                step="any"
                class="input font-mono"
              />
            </div>
            <div>
              <label class="input-label">
                {{ t('admin.modelPricing.editModal.longContextThreshold') }}
              </label>
              <input
                v-model.number="editForm.long_context_threshold"
                type="number"
                min="0"
                step="1000"
                class="input font-mono"
              />
            </div>
            <div>
              <label class="input-label">
                {{ t('admin.modelPricing.editModal.longContextInputMult') }}
              </label>
              <input
                v-model.number="editForm.long_context_input_multiplier"
                type="number"
                min="0"
                step="0.1"
                class="input font-mono"
              />
            </div>
            <div>
              <label class="input-label">
                {{ t('admin.modelPricing.editModal.longContextOutputMult') }}
              </label>
              <input
                v-model.number="editForm.long_context_output_multiplier"
                type="number"
                min="0"
                step="0.1"
                class="input font-mono"
              />
            </div>
          </div>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="showEditDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button
            type="submit"
            form="model-pricing-edit-form"
            :disabled="savingEdit"
            class="btn btn-primary"
          >
            {{ savingEdit ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Manual Sync Overwrite Confirmation Dialog -->
    <BaseDialog
      :show="showSyncConfirmDialog"
      :title="t('admin.modelPricing.syncConfirmModal.title')"
      width="narrow"
      @close="showSyncConfirmDialog = false"
    >
      <div class="space-y-3">
        <p class="text-xs text-gray-600 dark:text-gray-300">
          {{
            t('admin.modelPricing.syncConfirmModal.summary', {
              locked: syncStatus?.locked_channel_count ?? 0,
              unlocked: syncStatus?.unlocked_custom_count ?? 0
            })
          }}
        </p>

        <div class="space-y-2">
          <label
            :class="[
              'flex cursor-pointer items-start gap-3 rounded-xl border p-3 transition-colors',
              syncOverwriteMode === 'skip_locked'
                ? 'border-primary-500 bg-primary-50/60 dark:border-primary-500 dark:bg-primary-900/20'
                : 'border-gray-200 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800/60'
            ]"
          >
            <input
              v-model="syncOverwriteMode"
              type="radio"
              value="skip_locked"
              class="mt-0.5 h-4 w-4 border-gray-300 text-primary-600 focus:ring-primary-500"
            />
            <div class="min-w-0">
              <div class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t('admin.modelPricing.syncConfirmModal.skipLockedTitle') }}
              </div>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.modelPricing.syncConfirmModal.skipLockedDesc') }}
              </p>
            </div>
          </label>

          <label
            :class="[
              'flex cursor-pointer items-start gap-3 rounded-xl border p-3 transition-colors',
              syncOverwriteMode === 'overwrite_all'
                ? 'border-amber-500 bg-amber-50/70 dark:border-amber-500 dark:bg-amber-900/20'
                : 'border-gray-200 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800/60'
            ]"
          >
            <input
              v-model="syncOverwriteMode"
              type="radio"
              value="overwrite_all"
              class="mt-0.5 h-4 w-4 border-gray-300 text-amber-600 focus:ring-amber-500"
            />
            <div class="min-w-0">
              <div class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t('admin.modelPricing.syncConfirmModal.overwriteAllTitle') }}
              </div>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.modelPricing.syncConfirmModal.overwriteAllDesc') }}
              </p>
            </div>
          </label>
        </div>

        <p class="text-[11px] text-gray-500 dark:text-gray-400">
          {{ t('admin.modelPricing.syncConfirmModal.groupSafeNote') }}
        </p>
      </div>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="showSyncConfirmDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            @click="confirmSyncNow"
            :disabled="syncing"
            :class="syncOverwriteMode === 'overwrite_all' ? 'btn btn-danger' : 'btn btn-primary'"
          >
            <Icon name="refresh" size="sm" class="mr-1.5" :class="syncing ? 'animate-spin' : ''" />
            {{ syncing ? t('admin.modelPricing.syncing') : t('admin.modelPricing.syncConfirmModal.confirmAction') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Sync Settings Dialog -->
    <BaseDialog
      :show="showSyncDialog"
      :title="t('admin.modelPricing.syncModal.title')"
      width="narrow"
      @close="showSyncDialog = false"
    >
      <form id="model-pricing-sync-form" @submit.prevent="saveSyncSettings" class="space-y-4">
        <div class="flex items-center justify-between gap-4 rounded-xl border border-gray-200 p-3.5 dark:border-dark-700">
          <div class="min-w-0">
            <div class="text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.modelPricing.syncModal.autoSyncEnabled') }}
            </div>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.modelPricing.syncModal.autoSyncHint') }}
            </p>
          </div>
          <Toggle v-model="syncForm.auto_sync_enabled" />
        </div>

        <div>
          <label class="input-label">
            {{ t('admin.modelPricing.syncModal.intervalHours') }}
          </label>
          <Select
            v-model="syncForm.sync_interval_hours"
            :options="syncIntervalOptions"
          />
        </div>

        <div>
          <label class="input-label">
            {{ t('admin.modelPricing.syncModal.modelsDevUrl') }}
          </label>
          <input
            v-model="syncForm.models_dev_url"
            type="url"
            required
            class="input font-mono text-xs"
            placeholder="https://models.dev/api.json"
          />
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.modelPricing.syncModal.modelsDevUrlHint') }}
          </p>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="showSyncDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button
            type="submit"
            form="model-pricing-sync-form"
            :disabled="savingSync"
            class="btn btn-primary"
          >
            {{ savingSync ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Reset Confirmation -->
    <ConfirmDialog
      :show="showResetConfirm"
      :title="resetConfirmTitle"
      :message="resetConfirmMessage"
      :confirm-text="t('admin.modelPricing.actions.reset')"
      :danger="false"
      @confirm="confirmReset"
      @cancel="showResetConfirm = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Select from '@/components/common/Select.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Toggle from '@/components/common/Toggle.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { groupsAPI } from '@/api/admin/groups'
import {
  modelPricingAPI,
  type AccountChannelPriceEntry,
  type ModelPricingListItem,
  type ModelPricingSource,
  type PricingSyncOverwriteMode,
  type PricingSyncStatus,
  type SupportedPlatformSummary
} from '@/api/admin/modelPricing'
import { listPlatforms, platformDisplayName } from '@/constants/platformCatalog'
import { platformBadgeLightClass } from '@/utils/platformColors'
import type { AdminGroup } from '@/types'

type TreeTableRow =
  | {
      _rowKey: string
      _rowType: 'model'
      item: ModelPricingListItem
    }
  | {
      _rowKey: string
      _rowType: 'channel'
      item: ModelPricingListItem
      channel: AccountChannelPriceEntry
    }

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const syncing = ref(false)
const savingSync = ref(false)
const savingEdit = ref(false)

const syncStatus = ref<PricingSyncStatus | null>(null)
const groups = ref<AdminGroup[]>([])
const platforms = ref<SupportedPlatformSummary[]>([])
const items = ref<ModelPricingListItem[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)

const selectedGroupId = ref<number>(0)
const selectedPlatform = ref<string>('all')
const statusFilter = ref<'all' | 'overridden' | 'custom' | 'locked' | 'missing'>('all')
const searchQuery = ref('')

// Expanded model IDs for Level 2 channel sub-rows
const expandedModelIds = ref<Set<string>>(new Set())
const lockLoadingKeys = ref<Set<string>>(new Set())

// Edit dialog state (supports Default, System Group, and Account-Type Channel)
const showEditDialog = ref(false)
const showAdvanced = ref(false)
const isCreatingModel = ref(false)
const editingIsCustomOnly = ref(false)
const editingChannel = ref<AccountChannelPriceEntry | null>(null)

// Sync dialogs state
const showSyncDialog = ref(false)
const showSyncConfirmDialog = ref(false)
const syncOverwriteMode = ref<PricingSyncOverwriteMode>('skip_locked')

// Reset confirmation state
const showResetConfirm = ref(false)
const resettingItem = ref<ModelPricingListItem | null>(null)
const resettingChannel = ref<AccountChannelPriceEntry | null>(null)

const editForm = reactive({
  model_id: '',
  platform: 'anthropic',
  display_name: '',
  locked: true,
  input_per_million: 0,
  output_per_million: 0,
  cache_read_per_million: 0,
  cache_write_per_million: 0,
  cache_write_1h_str: '',
  long_context_threshold: 0,
  long_context_input_multiplier: 0,
  long_context_output_multiplier: 0
})

const syncForm = reactive({
  auto_sync_enabled: true,
  sync_interval_hours: 24,
  models_dev_url: 'https://models.dev/api.json'
})

const selectedGroup = computed(
  () => groups.value.find((g) => g.id === selectedGroupId.value) || null
)

const isPlatformLockedByGroup = computed(() =>
  Boolean(selectedGroup.value && selectedGroup.value.platform && selectedGroup.value.platform !== 'composite')
)

const columns = computed(() => [
  { key: 'model', label: t('admin.modelPricing.columns.model') },
  { key: 'platform', label: t('admin.modelPricing.columns.platform') },
  { key: 'input_price', label: t('admin.modelPricing.columns.input'), class: 'text-right' },
  { key: 'output_price', label: t('admin.modelPricing.columns.output'), class: 'text-right' },
  { key: 'cache_read', label: t('admin.modelPricing.columns.cacheRead'), class: 'text-right' },
  { key: 'cache_write', label: t('admin.modelPricing.columns.cacheWrite'), class: 'text-right' },
  { key: 'actions', label: t('admin.modelPricing.columns.actions') }
])

const hasAnyExpandableModel = computed(() =>
  items.value.some((item) => getOtherChannels(item).length > 0)
)

const tableRows = computed<TreeTableRow[]>(() => {
  const rows: TreeTableRow[] = []
  for (const item of items.value) {
    rows.push({
      _rowKey: `model:${item.model_id}`,
      _rowType: 'model',
      item
    })
    const otherChannels = getOtherChannels(item)
    if (expandedModelIds.value.has(item.model_id) && otherChannels.length > 0) {
      for (const ch of otherChannels) {
        rows.push({
          _rowKey: `ch:${item.model_id}:${ch.channel_id}`,
          _rowType: 'channel',
          item,
          channel: ch
        })
      }
    }
  }
  return rows
})

const groupSelectOptions = computed(() => {
  const defaultCount = syncStatus.value?.default_override_count ?? 0
  const opts: Array<Record<string, unknown>> = [
    {
      value: 0,
      label: t('admin.modelPricing.defaultGroupOption'),
      platform: '',
      overrideCount: defaultCount
    }
  ]
  for (const g of groups.value) {
    opts.push({
      value: g.id,
      label: g.name,
      platform: g.platform,
      overrideCount: syncStatus.value?.group_override_counts?.[String(g.id)] ?? 0
    })
  }
  return opts
})

const platformSelectOptions = computed(() => {
  const opts: Array<{ value: string; label: string }> = [
    { value: 'all', label: t('admin.modelPricing.allPlatforms') }
  ]
  const source =
    platforms.value.length > 0
      ? platforms.value
      : listPlatforms().map((p) => ({ id: p.id, name: p.display_name, model_count: 0 }))
  for (const p of source) {
    const label = t('admin.groups.platforms.' + p.id, p.name)
    opts.push({
      value: p.id,
      label: p.model_count > 0 ? `${label} (${p.model_count})` : label
    })
  }
  return opts
})

const editPlatformOptions = computed(() =>
  listPlatforms().map((p) => ({
    value: p.id,
    label: t('admin.groups.platforms.' + p.id, p.display_name)
  }))
)

const statusFilterOptions = computed(() => [
  { value: 'all', label: t('admin.modelPricing.filterAll') },
  { value: 'missing', label: t('admin.modelPricing.filterMissing') },
  { value: 'overridden', label: t('admin.modelPricing.filterOverridden') },
  { value: 'custom', label: t('admin.modelPricing.filterCustom') },
  { value: 'locked', label: t('admin.modelPricing.filterLocked') }
])

const syncIntervalOptions = computed(() =>
  [6, 12, 24, 48, 72, 168].map((hours) => ({
    value: hours,
    label: t('admin.modelPricing.syncModal.intervalOption', { hours })
  }))
)

const editDialogTitle = computed(() => {
  if (isCreatingModel.value) return t('admin.modelPricing.editModal.createTitle')
  if (editingChannel.value) {
    return t('admin.modelPricing.editModal.editChannelTitle', {
      channel: editingChannel.value.channel_name
    })
  }
  return t('admin.modelPricing.editModal.editTitle')
})

const editScopeBanner = computed(() => {
  if (editingChannel.value) {
    return t('admin.modelPricing.editModal.scopeChannel', {
      channel: editingChannel.value.channel_name
    })
  }
  if (selectedGroup.value) {
    return t('admin.modelPricing.editModal.scopeGroup', { name: selectedGroup.value.name })
  }
  return t('admin.modelPricing.editModal.scopeDefault')
})

const showEditLockToggle = computed(() => {
  if (editingChannel.value) return true
  if (selectedGroup.value) return false
  return !isCreatingModel.value && !editingIsCustomOnly.value
})

const resetConfirmTitle = computed(() => {
  if (resettingChannel.value) return t('admin.modelPricing.resetConfirm.titleChannel')
  if (selectedGroup.value) return t('admin.modelPricing.resetConfirm.titleGroup')
  return t('admin.modelPricing.resetConfirm.titleDefault')
})

const resetConfirmMessage = computed(() => {
  if (!resettingItem.value) return ''
  if (resettingChannel.value) {
    return t('admin.modelPricing.resetConfirm.messageChannel', {
      model: resettingItem.value.model_id,
      channel: resettingChannel.value.channel_name
    })
  }
  if (selectedGroup.value) {
    return t('admin.modelPricing.resetConfirm.messageGroup', {
      group: selectedGroup.value.name,
      model: resettingItem.value.model_id
    })
  }
  return t('admin.modelPricing.resetConfirm.messageDefault', {
    model: resettingItem.value.model_id
  })
})

function getTableRowClass(row: TreeTableRow): string {
  if (row._rowType === 'channel') {
    return 'bg-gray-50/75 dark:bg-dark-800/50'
  }
  return ''
}

function isModelExpanded(modelId: string): boolean {
  return expandedModelIds.value.has(modelId)
}

function toggleModelExpand(modelId: string) {
  const next = new Set(expandedModelIds.value)
  if (next.has(modelId)) {
    next.delete(modelId)
  } else {
    next.add(modelId)
  }
  expandedModelIds.value = next
}

function isChannelLockLoading(modelId: string, channelId: string): boolean {
  return lockLoadingKeys.value.has(`${modelId}:${channelId}`)
}

function getOtherChannels(item: ModelPricingListItem): AccountChannelPriceEntry[] {
  if (!item.channel_prices?.length) return []
  return item.channel_prices.filter((ch) => !ch.is_default)
}

function getDefaultChannel(item: ModelPricingListItem): AccountChannelPriceEntry | null {
  if (!item.channel_prices?.length) return null
  return item.channel_prices.find((ch) => ch.is_default) || item.channel_prices[0] || null
}

function getDefaultChannelRawModelId(item: ModelPricingListItem): string {
  const raw = getDefaultChannel(item)?.raw_model_id?.trim() || ''
  if (!raw || raw.toLowerCase() === item.model_id.toLowerCase()) return ''
  return raw
}

function getDefaultChannelSyncedPricing(item: ModelPricingListItem) {
  return getDefaultChannel(item)?.synced_pricing ?? null
}

function hasSyncedPriceDiff(currentVal?: number | null, syncedVal?: number | null): boolean {
  if (syncedVal === undefined || syncedVal === null) return false
  return Math.abs((currentVal ?? 0) - syncedVal) > 1e-9
}

let searchTimer: ReturnType<typeof setTimeout> | null = null

function handleSearch() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    page.value = 1
    void loadModels()
  }, 250)
}

function handleGroupChange() {
  if (selectedGroup.value && selectedGroup.value.platform && selectedGroup.value.platform !== 'composite') {
    selectedPlatform.value = selectedGroup.value.platform
  } else {
    selectedPlatform.value = 'all'
  }
  page.value = 1
  void loadModels()
}

function handleFilterChange() {
  page.value = 1
  void loadModels()
}

function filterByMissingPricing() {
  statusFilter.value = 'missing'
  handleFilterChange()
}

function handlePageChange(nextPage: number) {
  page.value = nextPage
  void loadModels()
}

function handlePageSizeChange(nextSize: number) {
  pageSize.value = nextSize
  page.value = 1
  void loadModels()
}

function formatPrice(val?: number | null): string {
  if (val === undefined || val === null || Number.isNaN(val)) return '0.00'
  if (val === 0) return '0.00'
  if (val < 0.01) return val.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
  if (val < 1) return Number(val.toFixed(4)).toString()
  return Number(val.toFixed(3)).toFixed(2).replace(/(\.\d\d\d?)0+$/, '$1')
}

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

function sourceBadgeClass(source: ModelPricingSource): string {
  switch (source) {
    case 'missing_pricing':
      return 'inline-flex items-center rounded-full bg-rose-100 px-2 py-0.5 text-[11px] font-medium text-rose-700 dark:bg-rose-900/30 dark:text-rose-300'
    case 'group_custom':
      return 'inline-flex items-center rounded-full bg-primary-100 px-2 py-0.5 text-[11px] font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
    case 'default_custom':
      return 'inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    case 'inherited_default':
      return 'inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-[11px] font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300'
    default:
      return 'inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-[11px] font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  }
}

function canResetModelRow(item: ModelPricingListItem): boolean {
  if (selectedGroupId.value > 0) {
    return item.is_group_overridden
  }
  return item.is_default_modified || item.is_default_locked || item.is_custom_only
}

async function loadStatus() {
  try {
    syncStatus.value = await modelPricingAPI.getStatus()
  } catch {
    // Non-blocking
  }
}

async function loadGroups() {
  try {
    groups.value = await groupsAPI.getAll()
  } catch {
    groups.value = []
  }
}

async function loadModels() {
  loading.value = true
  try {
    const res = await modelPricingAPI.listModels({
      group_id: selectedGroupId.value > 0 ? selectedGroupId.value : undefined,
      platform: isPlatformLockedByGroup.value ? undefined : selectedPlatform.value,
      search: searchQuery.value,
      filter: statusFilter.value,
      page: page.value,
      page_size: pageSize.value
    })
    items.value = res.items || []
    total.value = res.total || 0
    if (res.platforms?.length) {
      platforms.value = res.platforms
    }
    // Auto-expand multi-channel rows when locked filter or channel search matches a non-default channel
    const needle = searchQuery.value.trim().toLowerCase()
    if (statusFilter.value === 'locked' || needle) {
      const nextExpanded = new Set(expandedModelIds.value)
      let changed = false
      for (const item of items.value) {
        const others = getOtherChannels(item)
        if (others.length === 0) continue
        const matchLockedOther = statusFilter.value === 'locked' && others.some((ch) => ch.is_locked)
        const matchSearchOther =
          Boolean(needle) &&
          !item.model_id.toLowerCase().includes(needle) &&
          others.some(
            (ch) =>
              ch.channel_name.toLowerCase().includes(needle) ||
              ch.channel_id.toLowerCase().includes(needle) ||
              ch.raw_model_id.toLowerCase().includes(needle)
          )
        if ((matchLockedOther || matchSearchOther) && !nextExpanded.has(item.model_id)) {
          nextExpanded.add(item.model_id)
          changed = true
        }
      }
      if (changed) {
        expandedModelIds.value = nextExpanded
      }
    }
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : t('admin.modelPricing.messages.loadFailed')
    appStore.showError(message)
  } finally {
    loading.value = false
  }
}

function openSyncConfirmDialog() {
  syncOverwriteMode.value = 'skip_locked'
  showSyncConfirmDialog.value = true
}

async function confirmSyncNow() {
  if (syncing.value) return
  syncing.value = true
  try {
    const res = await modelPricingAPI.syncNow(syncOverwriteMode.value)
    syncStatus.value = res
    showSyncConfirmDialog.value = false
    const skipped = res.meta.last_skipped_locked ?? 0
    const overwritten = res.meta.last_overwritten_count ?? 0
    if (skipped > 0 || overwritten > 0) {
      appStore.showSuccess(
        t('admin.modelPricing.messages.syncSuccessWithStats', {
          models: res.meta.model_count,
          skipped,
          overwritten
        })
      )
    } else {
      appStore.showSuccess(
        t('admin.modelPricing.messages.syncSuccess', {
          models: res.meta.model_count,
          channels: res.meta.channel_count
        })
      )
    }
    await loadModels()
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : t('admin.modelPricing.messages.syncError')
    appStore.showError(message)
    await loadStatus()
  } finally {
    syncing.value = false
  }
}

function openSyncSettingsDialog() {
  syncForm.auto_sync_enabled = syncStatus.value?.config.auto_sync_enabled ?? true
  syncForm.sync_interval_hours = syncStatus.value?.config.sync_interval_hours ?? 24
  syncForm.models_dev_url = syncStatus.value?.config.models_dev_url || 'https://models.dev/api.json'
  showSyncDialog.value = true
}

async function saveSyncSettings() {
  savingSync.value = true
  try {
    syncStatus.value = await modelPricingAPI.updateSyncConfig({
      auto_sync_enabled: syncForm.auto_sync_enabled,
      sync_interval_hours: Number(syncForm.sync_interval_hours) || 24,
      models_dev_url: syncForm.models_dev_url.trim()
    })
    showSyncDialog.value = false
    appStore.showSuccess(t('admin.modelPricing.messages.syncConfigSaved'))
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : t('common.error')
    appStore.showError(message)
  } finally {
    savingSync.value = false
  }
}

async function setChannelLockState(modelId: string, channelId: string, nextLocked: boolean) {
  const key = `${modelId}:${channelId}`
  if (lockLoadingKeys.value.has(key)) return
  const next = new Set(lockLoadingKeys.value)
  next.add(key)
  lockLoadingKeys.value = next

  try {
    await modelPricingAPI.setChannelLock(modelId, channelId, nextLocked)
    appStore.showSuccess(
      nextLocked
        ? t('admin.modelPricing.messages.lockUpdated')
        : t('admin.modelPricing.messages.unlockUpdated')
    )
    await Promise.all([loadStatus(), loadModels()])
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : t('common.error')
    appStore.showError(message)
  } finally {
    const done = new Set(lockLoadingKeys.value)
    done.delete(key)
    lockLoadingKeys.value = done
  }
}

async function toggleDefaultLock(item: ModelPricingListItem) {
  await setChannelLockState(item.model_id, item.default_channel_id || '', !item.is_default_locked)
}

async function toggleChannelLock(item: ModelPricingListItem, channel: AccountChannelPriceEntry) {
  await setChannelLockState(item.model_id, channel.channel_id, !channel.is_locked)
}

function openCreateDialog() {
  isCreatingModel.value = true
  editingIsCustomOnly.value = true
  editingChannel.value = null
  showAdvanced.value = false
  editForm.model_id = ''
  editForm.platform =
    selectedGroup.value && selectedGroup.value.platform !== 'composite'
      ? selectedGroup.value.platform
      : selectedPlatform.value !== 'all'
        ? selectedPlatform.value
        : 'anthropic'
  editForm.display_name = ''
  editForm.locked = true
  editForm.input_per_million = 0
  editForm.output_per_million = 0
  editForm.cache_read_per_million = 0
  editForm.cache_write_per_million = 0
  editForm.cache_write_1h_str = ''
  editForm.long_context_threshold = 0
  editForm.long_context_input_multiplier = 0
  editForm.long_context_output_multiplier = 0
  showEditDialog.value = true
}

function openEditModelDialog(item: ModelPricingListItem) {
  isCreatingModel.value = false
  editingIsCustomOnly.value = Boolean(item.is_custom_only)
  editingChannel.value = null
  editForm.model_id = item.model_id
  editForm.platform = item.platform || 'anthropic'
  editForm.display_name = item.display_name || ''
  editForm.locked =
    item.is_default_modified || item.is_default_locked ? item.is_default_locked : true
  editForm.input_per_million = item.effective_pricing.input_per_million ?? 0
  editForm.output_per_million = item.effective_pricing.output_per_million ?? 0
  editForm.cache_read_per_million = item.effective_pricing.cache_read_per_million ?? 0
  editForm.cache_write_per_million = item.effective_pricing.cache_write_per_million ?? 0
  editForm.cache_write_1h_str =
    item.effective_pricing.cache_write_1h_per_million !== undefined &&
    item.effective_pricing.cache_write_1h_per_million !== null
      ? String(item.effective_pricing.cache_write_1h_per_million)
      : ''
  editForm.long_context_threshold = item.effective_pricing.long_context_threshold ?? 0
  editForm.long_context_input_multiplier = item.effective_pricing.long_context_input_multiplier ?? 0
  editForm.long_context_output_multiplier = item.effective_pricing.long_context_output_multiplier ?? 0
  showAdvanced.value =
    editForm.cache_write_1h_str !== '' || editForm.long_context_threshold > 0
  showEditDialog.value = true
}

function openEditChannelDialog(item: ModelPricingListItem, channel: AccountChannelPriceEntry) {
  // In Global Default view, the model row and its [Default] channel sub-row represent
  // the same default channel price — unify their edit experience cleanly.
  if (channel.is_default && selectedGroupId.value === 0) {
    openEditModelDialog(item)
    return
  }
  isCreatingModel.value = false
  editingIsCustomOnly.value = false
  editingChannel.value = channel
  editForm.model_id = item.model_id
  editForm.platform = item.platform || 'anthropic'
  editForm.display_name = item.display_name || ''
  editForm.locked = channel.is_overridden || channel.is_locked ? channel.is_locked : true
  editForm.input_per_million = channel.pricing.input_per_million ?? 0
  editForm.output_per_million = channel.pricing.output_per_million ?? 0
  editForm.cache_read_per_million = channel.pricing.cache_read_per_million ?? 0
  editForm.cache_write_per_million = channel.pricing.cache_write_per_million ?? 0
  editForm.cache_write_1h_str =
    channel.pricing.cache_write_1h_per_million !== undefined &&
    channel.pricing.cache_write_1h_per_million !== null
      ? String(channel.pricing.cache_write_1h_per_million)
      : ''
  editForm.long_context_threshold = channel.pricing.long_context_threshold ?? 0
  editForm.long_context_input_multiplier = channel.pricing.long_context_input_multiplier ?? 0
  editForm.long_context_output_multiplier = channel.pricing.long_context_output_multiplier ?? 0
  showAdvanced.value =
    editForm.cache_write_1h_str !== '' || editForm.long_context_threshold > 0
  showEditDialog.value = true
}

async function saveEditPricing() {
  if (!editForm.model_id.trim()) {
    appStore.showError(t('admin.modelPricing.messages.modelIdRequired'))
    return
  }
  savingEdit.value = true
  try {
    const cache1h =
      editForm.cache_write_1h_str !== '' && !Number.isNaN(Number(editForm.cache_write_1h_str))
        ? Number(editForm.cache_write_1h_str)
        : null
    const isChannelScope = Boolean(editingChannel.value)
    await modelPricingAPI.upsertOverride({
      group_id: !isChannelScope && selectedGroupId.value > 0 ? selectedGroupId.value : null,
      channel_id: isChannelScope ? editingChannel.value?.channel_id : undefined,
      model_id: editForm.model_id.trim(),
      platform: editForm.platform,
      display_name: isChannelScope ? undefined : editForm.display_name.trim() || undefined,
      locked: showEditLockToggle.value ? editForm.locked : undefined,
      input_per_million: Number(editForm.input_per_million) || 0,
      output_per_million: Number(editForm.output_per_million) || 0,
      cache_read_per_million: Number(editForm.cache_read_per_million) || 0,
      cache_write_per_million: Number(editForm.cache_write_per_million) || 0,
      cache_write_1h_per_million: cache1h,
      long_context_threshold: Number(editForm.long_context_threshold) || 0,
      long_context_input_multiplier: Number(editForm.long_context_input_multiplier) || 0,
      long_context_output_multiplier: Number(editForm.long_context_output_multiplier) || 0
    })
    showEditDialog.value = false
    appStore.showSuccess(t('admin.modelPricing.messages.overrideSaved'))
    await Promise.all([loadStatus(), loadModels()])
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : t('common.error')
    appStore.showError(message)
  } finally {
    savingEdit.value = false
  }
}

function promptResetModel(item: ModelPricingListItem) {
  resettingItem.value = item
  resettingChannel.value = null
  showResetConfirm.value = true
}

function promptResetChannel(item: ModelPricingListItem, channel: AccountChannelPriceEntry) {
  resettingItem.value = item
  resettingChannel.value = channel
  showResetConfirm.value = true
}

async function confirmReset() {
  if (!resettingItem.value) return
  try {
    if (resettingChannel.value) {
      await modelPricingAPI.resetOverride(resettingItem.value.model_id, {
        channelId: resettingChannel.value.channel_id
      })
    } else {
      await modelPricingAPI.resetOverride(resettingItem.value.model_id, {
        groupId: selectedGroupId.value > 0 ? selectedGroupId.value : null
      })
    }
    showResetConfirm.value = false
    resettingItem.value = null
    resettingChannel.value = null
    appStore.showSuccess(t('admin.modelPricing.messages.overrideReset'))
    await Promise.all([loadStatus(), loadModels()])
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : t('common.error')
    appStore.showError(message)
  }
}

onMounted(async () => {
  await Promise.all([loadStatus(), loadGroups(), loadModels()])
})
</script>
