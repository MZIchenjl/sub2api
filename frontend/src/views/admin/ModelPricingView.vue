<template>
  <AppLayout>
    <div class="space-y-4">
      <!-- 1. models.dev 同步状态栏（参考 cc-switch ModelsDevAutoSyncPanel） -->
      <div
        class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div class="flex min-w-0 items-start gap-3">
            <div
              class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary-50 text-primary-600 dark:bg-primary-900/25 dark:text-primary-400"
            >
              <Icon name="refresh" size="md" :class="syncing ? 'animate-spin' : ''" />
            </div>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
                  {{ t('admin.modelPricing.syncPanelTitle') }}
                </h2>
                <span
                  :class="[
                    'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
                    syncStatus?.config.auto_sync_enabled
                      ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/25 dark:text-emerald-300'
                      : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
                  ]"
                >
                  <span
                    :class="[
                      'h-1.5 w-1.5 rounded-full',
                      syncStatus?.config.auto_sync_enabled ? 'bg-emerald-500' : 'bg-gray-400'
                    ]"
                  />
                  {{
                    syncStatus?.config.auto_sync_enabled
                      ? t('admin.modelPricing.autoSyncEnabled')
                      : t('admin.modelPricing.autoSyncDisabled')
                  }}
                </span>
                <span
                  v-if="syncStatus?.meta.last_sync_at"
                  class="text-xs text-gray-500 dark:text-gray-400"
                  :title="formatFullDate(syncStatus.meta.last_sync_at)"
                >
                  {{ t('admin.modelPricing.lastSyncedAt', { time: formatFullDate(syncStatus.meta.last_sync_at) }) }}
                </span>
                <span v-else class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.modelPricing.neverSynced') }}
                </span>
              </div>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                <template v-if="syncStatus?.cache_loaded">
                  {{
                    t('admin.modelPricing.cacheStats', {
                      providers: syncStatus.meta.provider_count,
                      models: syncStatus.meta.model_group_count,
                      entries: syncStatus.meta.raw_entry_count
                    })
                  }}
                </template>
                <template v-else>
                  {{ t('admin.modelPricing.noCacheWarning') }}
                </template>
              </p>
            </div>
          </div>

          <div class="flex flex-wrap items-center gap-2.5">
            <label class="inline-flex cursor-pointer items-center gap-2 text-xs text-gray-600 dark:text-gray-300">
              <span>{{ t('admin.modelPricing.autoSyncToggle') }}</span>
              <span :class="(savingSyncConfig || !syncStatus) ? 'pointer-events-none opacity-50' : ''">
                <Toggle
                  :modelValue="Boolean(syncStatus?.config.auto_sync_enabled)"
                  @update:modelValue="handleToggleAutoSync"
                />
              </span>
            </label>

            <select
              :value="syncStatus?.config.sync_interval_hours ?? 24"
              :disabled="savingSyncConfig || !syncStatus"
              class="input h-8 w-auto min-w-[108px] py-1 text-xs"
              :aria-label="t('admin.modelPricing.syncIntervalLabel')"
              @change="handleIntervalChange"
            >
              <option v-for="hours in intervalOptions" :key="hours" :value="hours">
                {{ t('admin.modelPricing.everyNHours', { hours }) }}
              </option>
            </select>

            <button
              type="button"
              class="btn btn-secondary h-8 px-3 text-xs"
              @click="openSyncSettingsDialog"
            >
              <Icon name="cog" size="sm" class="mr-1.5" />
              {{ t('admin.modelPricing.syncSettings') }}
            </button>

            <button
              type="button"
              class="btn btn-primary h-8 px-3 text-xs"
              :disabled="syncing"
              @click="handleSyncNow"
            >
              <Icon name="refresh" size="sm" class="mr-1.5" :class="syncing ? 'animate-spin' : ''" />
              {{ syncing ? t('admin.modelPricing.syncing') : t('admin.modelPricing.syncNow') }}
            </button>
          </div>
        </div>

        <div
          v-if="syncStatus?.meta.last_sync_status === 'failed' && syncStatus.meta.last_sync_error"
          class="mt-3 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-700 dark:border-red-900/40 dark:bg-red-900/20 dark:text-red-300"
        >
          <Icon name="exclamationCircle" size="sm" class="mt-0.5 shrink-0" />
          <div class="min-w-0">
            <span class="font-medium">{{ t('admin.modelPricing.syncErrorTitle') }}：</span>
            <span class="break-all">{{ syncStatus.meta.last_sync_error }}</span>
          </div>
        </div>
      </div>

      <!-- 2. 计费分组作用域切换（默认价格 vs 各分组独立编辑） -->
      <div
        class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.modelPricing.scopeSectionTitle') }}
            </h3>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.modelPricing.scopeSectionHint') }}
            </p>
          </div>
        </div>

        <div class="mt-3 flex flex-wrap items-center gap-2">
          <!-- 全局默认作用域 -->
          <button
            type="button"
            :class="[
              'inline-flex items-center gap-2 rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors',
              selectedGroupId === 0
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-500 dark:bg-primary-900/30 dark:text-primary-300'
                : 'border-gray-200 bg-gray-50 text-gray-700 hover:bg-gray-100 dark:border-dark-600 dark:bg-dark-700/60 dark:text-gray-300 dark:hover:bg-dark-700'
            ]"
            @click="selectGroupScope(0)"
          >
            <span>{{ t('admin.modelPricing.defaultScope') }}</span>
            <span
              v-if="(syncStatus?.default_override_count ?? 0) > 0"
              class="rounded-full bg-primary-100 px-1.5 py-0.5 text-[11px] font-medium text-primary-700 dark:bg-primary-900/50 dark:text-primary-300"
            >
              {{ t('admin.modelPricing.defaultModifiedBadge', { count: syncStatus?.default_override_count ?? 0 }) }}
            </span>
          </button>

          <!-- 各 sub2api 分组 -->
          <button
            v-for="group in groups"
            :key="group.id"
            type="button"
            :class="[
              'inline-flex items-center gap-2 rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors',
              selectedGroupId === group.id
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-500 dark:bg-primary-900/30 dark:text-primary-300'
                : 'border-gray-200 bg-white text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700'
            ]"
            @click="selectGroupScope(group.id)"
          >
            <span class="max-w-[180px] truncate">{{ group.name }}</span>
            <span
              class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] uppercase text-gray-500 dark:bg-dark-700 dark:text-gray-400"
            >
              {{ group.platform }}
            </span>
            <span
              v-if="groupOverrideCount(group.id) > 0"
              class="rounded-full bg-amber-100 px-1.5 py-0.5 text-[11px] font-medium text-amber-800 dark:bg-amber-900/35 dark:text-amber-300"
            >
              {{ t('admin.modelPricing.groupOverrideBadge', { count: groupOverrideCount(group.id) }) }}
            </span>
          </button>
        </div>
      </div>

      <!-- 3. 定价表主卡片（参考 cc-switch PricingConfigPanel 布局） -->
      <div
        class="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800"
      >
        <!-- 工具栏 -->
        <div
          class="flex flex-col gap-3 border-b border-gray-200 px-4 py-3 dark:border-dark-700 xl:flex-row xl:items-center xl:justify-between"
        >
          <div class="flex flex-wrap items-center gap-2">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.modelPricing.tableTitle') }}
              <span class="ml-1 font-normal text-gray-500 dark:text-gray-400">({{ pagination.total }})</span>
            </h3>
            <span class="text-xs text-gray-400 dark:text-gray-500">
              {{ t('admin.modelPricing.priceUnit') }}
            </span>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <!-- 搜索框（回车编辑精确匹配项，对齐 cc-switch 交互） -->
            <div class="relative w-full sm:w-64">
              <Icon
                name="search"
                size="sm"
                class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400"
              />
              <input
                v-model="searchQuery"
                type="text"
                :placeholder="t('admin.modelPricing.searchPlaceholder')"
                class="input h-8 pl-9 pr-7 text-xs"
                @input="handleSearchInput"
                @keydown.enter="handleSearchEnter"
              />
              <button
                v-if="searchQuery"
                type="button"
                class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600 dark:hover:text-gray-200"
                @click="clearSearch"
              >
                <Icon name="x" size="xs" />
              </button>
            </div>

            <!-- models.dev 渠道筛选 -->
            <select
              v-model="providerFilter"
              class="input h-8 w-auto min-w-[160px] max-w-[220px] py-1 text-xs"
              @change="handleFilterChange"
            >
              <option value="all">{{ t('admin.modelPricing.allProviders') }}</option>
              <option value="custom">{{ t('admin.modelPricing.customProviderOption') }}</option>
              <option v-for="p in providers" :key="p.id" :value="p.id">
                {{ p.name }} ({{ p.model_count }})
              </option>
            </select>

            <!-- 状态筛选 -->
            <div
              class="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-0.5 text-xs dark:border-dark-600 dark:bg-dark-900/50"
            >
              <button
                v-for="tab in filterTabs"
                :key="tab.value"
                type="button"
                :class="[
                  'rounded-md px-2.5 py-1 font-medium transition-colors',
                  statusFilter === tab.value
                    ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
                    : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-white'
                ]"
                @click="selectStatusFilter(tab.value)"
              >
                {{ tab.label }}
              </button>
            </div>

            <!-- 新增自定义模型 -->
            <button
              type="button"
              class="btn btn-primary h-8 px-3 text-xs"
              @click="openAddCustomModal()"
            >
              <Icon name="plus" size="sm" class="mr-1" />
              {{ t('admin.modelPricing.addCustomModel') }}
            </button>
          </div>
        </div>

        <!-- 表格主体 -->
        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-900/40">
              <tr>
                <th
                  class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.modelPricing.colModel') }}
                </th>
                <th
                  class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.modelPricing.colCachedSource') }}
                </th>
                <th
                  class="px-3 py-2.5 text-right text-xs font-medium text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.modelPricing.colInput') }}
                </th>
                <th
                  class="px-3 py-2.5 text-right text-xs font-medium text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.modelPricing.colOutput') }}
                </th>
                <th
                  class="px-3 py-2.5 text-right text-xs font-medium text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.modelPricing.colCacheRead') }}
                </th>
                <th
                  class="px-3 py-2.5 text-right text-xs font-medium text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.modelPricing.colCacheWrite') }}
                </th>
                <th
                  class="px-4 py-2.5 text-right text-xs font-medium text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.modelPricing.colActions') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700/70 dark:bg-dark-800">
              <tr v-if="loading && items.length === 0">
                <td colspan="7" class="px-4 py-12 text-center text-sm text-gray-500 dark:text-gray-400">
                  <div class="inline-flex items-center gap-2">
                    <Icon name="refresh" size="sm" class="animate-spin" />
                    <span>{{ t('common.loading', 'Loading...') }}</span>
                  </div>
                </td>
              </tr>

              <tr v-else-if="!loading && items.length === 0">
                <td colspan="7" class="px-4 py-12">
                  <EmptyState
                    :title="t('admin.modelPricing.emptyTitle')"
                    :description="t('admin.modelPricing.emptyDescription')"
                  />
                </td>
              </tr>

              <tr
                v-for="row in items"
                :key="row.model_id"
                class="transition-colors hover:bg-gray-50/80 dark:hover:bg-dark-700/40"
              >
                <!-- 原始模型 ID / 名称 / 状态标签 -->
                <td class="max-w-[300px] px-4 py-2.5 align-top">
                  <div class="flex items-center gap-1.5">
                    <span
                      class="truncate font-mono text-xs font-semibold text-gray-900 dark:text-white"
                      :title="row.model_id"
                    >
                      {{ row.model_id }}
                    </span>
                    <span
                      v-if="row.release_date"
                      class="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-500 dark:bg-dark-700 dark:text-gray-400"
                    >
                      {{ row.release_date }}
                    </span>
                  </div>
                  <div class="mt-0.5 flex flex-wrap items-center gap-1.5">
                    <span
                      v-if="row.display_name && row.display_name !== row.model_id"
                      class="truncate text-xs text-gray-500 dark:text-gray-400"
                      :title="row.display_name"
                    >
                      {{ row.display_name }}
                    </span>
                    <span :class="sourceBadgeClass(row)">
                      {{ sourceBadgeLabel(row) }}
                    </span>
                    <span
                      v-if="selectedGroupId === 0 && row.group_override_count > 0"
                      class="inline-flex items-center rounded bg-indigo-50 px-1.5 py-0.5 text-[10px] font-medium text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300"
                    >
                      {{ t('admin.modelPricing.groupsOverriddenTag', { count: row.group_override_count }) }}
                    </span>
                  </div>
                </td>

                <!-- 缓存价格源（不同渠道按原始模型 ID 分组，允许直接下拉切换） -->
                <td class="min-w-[230px] max-w-[320px] px-4 py-2.5 align-top">
                  <div v-if="row.candidates.length > 0" class="space-y-1">
                    <select
                      :value="row.active_candidate_key || ''"
                      :disabled="switchingModelId === row.model_id"
                      class="input h-8 w-full py-1 text-xs"
                      @change="handleCandidateSelect(row, $event)"
                    >
                      <option v-if="!row.active_candidate_key" value="" disabled>
                        {{ t('admin.modelPricing.customActiveOption') }}
                      </option>
                      <option
                        v-for="cand in row.candidates"
                        :key="cand.candidate_key"
                        :value="cand.candidate_key"
                      >
                        {{ formatCandidateOption(cand) }}
                      </option>
                    </select>
                    <div class="flex items-center justify-between text-[11px] text-gray-400 dark:text-gray-500">
                      <span class="truncate font-mono" :title="row.active_raw_model_id || ''">
                        {{ row.active_raw_model_id || (row.source.endsWith('custom') ? 'custom' : '') }}
                      </span>
                      <span v-if="row.candidate_count > 1" class="ml-2 shrink-0">
                        {{ t('admin.modelPricing.candidatesCount', { count: row.candidate_count }) }}
                      </span>
                    </div>
                  </div>
                  <span
                    v-else
                    class="inline-flex items-center rounded bg-purple-50 px-2 py-0.5 text-xs font-medium text-purple-700 dark:bg-purple-900/30 dark:text-purple-300"
                  >
                    {{ t('admin.modelPricing.customOnlyBadge') }}
                  </span>
                </td>

                <!-- 输入单价 -->
                <td class="whitespace-nowrap px-3 py-2.5 text-right align-top font-mono text-xs">
                  <div class="font-medium text-gray-900 dark:text-white">
                    {{ formatUnitPrice(row.effective_pricing.input_per_million) }}
                  </div>
                  <div
                    v-if="showDefaultComparison(row, 'input_per_million')"
                    class="mt-0.5 text-[10px] text-gray-400 line-through"
                  >
                    {{ formatUnitPrice(row.default_pricing!.input_per_million) }}
                  </div>
                </td>

                <!-- 输出单价 -->
                <td class="whitespace-nowrap px-3 py-2.5 text-right align-top font-mono text-xs">
                  <div class="font-medium text-gray-900 dark:text-white">
                    {{ formatUnitPrice(row.effective_pricing.output_per_million) }}
                  </div>
                  <div
                    v-if="showDefaultComparison(row, 'output_per_million')"
                    class="mt-0.5 text-[10px] text-gray-400 line-through"
                  >
                    {{ formatUnitPrice(row.default_pricing!.output_per_million) }}
                  </div>
                </td>

                <!-- 缓存读取 -->
                <td class="whitespace-nowrap px-3 py-2.5 text-right align-top font-mono text-xs">
                  <div class="text-gray-700 dark:text-gray-200">
                    {{ formatUnitPrice(row.effective_pricing.cache_read_per_million) }}
                  </div>
                  <div
                    v-if="showDefaultComparison(row, 'cache_read_per_million')"
                    class="mt-0.5 text-[10px] text-gray-400 line-through"
                  >
                    {{ formatUnitPrice(row.default_pricing!.cache_read_per_million) }}
                  </div>
                </td>

                <!-- 缓存写入 + 1h / 长上下文标记 -->
                <td class="whitespace-nowrap px-3 py-2.5 text-right align-top font-mono text-xs">
                  <div class="text-gray-700 dark:text-gray-200">
                    {{ formatUnitPrice(row.effective_pricing.cache_write_per_million) }}
                  </div>
                  <div
                    v-if="showDefaultComparison(row, 'cache_write_per_million')"
                    class="mt-0.5 text-[10px] text-gray-400 line-through"
                  >
                    {{ formatUnitPrice(row.default_pricing!.cache_write_per_million) }}
                  </div>
                  <div
                    v-if="
                      row.effective_pricing.cache_write_1h_per_million != null &&
                      row.effective_pricing.cache_write_1h_per_million > 0
                    "
                    class="mt-0.5 text-[10px] text-gray-400"
                  >
                    {{
                      t('admin.modelPricing.cache1hTag', {
                        price: formatUnitPrice(row.effective_pricing.cache_write_1h_per_million)
                      })
                    }}
                  </div>
                  <div
                    v-if="
                      (row.effective_pricing.long_context_threshold ?? 0) > 0 &&
                      ((row.effective_pricing.long_context_input_multiplier ?? 0) > 1 ||
                        (row.effective_pricing.long_context_output_multiplier ?? 0) > 1)
                    "
                    class="mt-0.5 text-[10px] text-amber-600 dark:text-amber-400"
                  >
                    {{
                      t('admin.modelPricing.longContextTag', {
                        tokens: Math.round((row.effective_pricing.long_context_threshold ?? 0) / 1000),
                        inMult: row.effective_pricing.long_context_input_multiplier || 1,
                        outMult: row.effective_pricing.long_context_output_multiplier || 1
                      })
                    }}
                  </div>
                </td>

                <!-- 操作列 -->
                <td class="whitespace-nowrap px-4 py-2.5 text-right align-top">
                  <div class="inline-flex items-center justify-end gap-1">
                    <button
                      type="button"
                      class="rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
                      :title="t('admin.modelPricing.editPricing')"
                      @click="openEditModal(row)"
                    >
                      <Icon name="edit" size="sm" />
                    </button>
                    <button
                      v-if="canResetRow(row)"
                      type="button"
                      class="rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                      :title="resetButtonTitle(row)"
                      @click="confirmReset(row)"
                    >
                      <Icon :name="row.is_custom_only && selectedGroupId === 0 ? 'trash' : 'refresh'" size="sm" />
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 分页 -->
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </div>
    </div>

    <!-- 编辑 / 新增自定义模型定价弹窗 -->
    <BaseDialog
      :show="editDialog.open"
      :title="
        editDialog.isNew
          ? t('admin.modelPricing.addPricingTitle')
          : t('admin.modelPricing.editPricingTitle', { model: editDialog.form.model_id })
      "
      width="wide"
      @close="closeEditModal"
    >
      <form id="model-pricing-edit-form" class="space-y-4" @submit.prevent="submitEditModal">
        <!-- 作用域提示条 -->
        <div
          class="rounded-lg border border-primary-200 bg-primary-50/70 px-3 py-2 text-xs text-primary-800 dark:border-primary-800/60 dark:bg-primary-900/20 dark:text-primary-200"
        >
          <template v-if="selectedGroupId === 0">
            {{ t('admin.modelPricing.editingScopeDefault') }}
          </template>
          <template v-else>
            {{ t('admin.modelPricing.editingScopeGroup', { group: selectedGroupName }) }}
          </template>
        </div>

        <!-- 从缓存渠道快速填入 -->
        <div
          v-if="editDialog.candidates.length > 0"
          class="rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-900/40"
        >
          <label class="mb-1.5 block text-xs font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.modelPricing.prefillFromCache') }}
          </label>
          <select
            v-model="editDialog.prefillCandidateKey"
            class="input h-8 text-xs"
            @change="applyPrefillCandidate"
          >
            <option value="">{{ t('admin.modelPricing.selectCachedCandidatePlaceholder') }}</option>
            <option
              v-for="cand in editDialog.candidates"
              :key="cand.candidate_key"
              :value="cand.candidate_key"
            >
              {{ formatCandidateOption(cand) }}
            </option>
          </select>
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.modelPricing.modelIdLabel') }}
            </label>
            <input
              v-model="editDialog.form.model_id"
              type="text"
              :disabled="!editDialog.isNew"
              :placeholder="t('admin.modelPricing.modelIdPlaceholder')"
              class="input font-mono text-xs"
              required
            />
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.modelPricing.displayNameLabel') }}
            </label>
            <input
              v-model="editDialog.form.display_name"
              type="text"
              :placeholder="t('admin.modelPricing.displayNamePlaceholder')"
              class="input text-xs"
            />
          </div>
        </div>

        <!-- 核心 4 项单价（USD / 1M tokens） -->
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.modelPricing.inputCostLabel') }}
            </label>
            <input
              v-model.number="editDialog.form.input_per_million"
              type="number"
              step="0.0001"
              min="0"
              class="input font-mono text-xs"
              required
            />
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.modelPricing.outputCostLabel') }}
            </label>
            <input
              v-model.number="editDialog.form.output_per_million"
              type="number"
              step="0.0001"
              min="0"
              class="input font-mono text-xs"
              required
            />
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.modelPricing.cacheReadCostLabel') }}
            </label>
            <input
              v-model.number="editDialog.form.cache_read_per_million"
              type="number"
              step="0.0001"
              min="0"
              class="input font-mono text-xs"
            />
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.modelPricing.cacheWriteCostLabel') }}
            </label>
            <input
              v-model.number="editDialog.form.cache_write_per_million"
              type="number"
              step="0.0001"
              min="0"
              class="input font-mono text-xs"
            />
          </div>
        </div>

        <!-- 高级选项：1h 缓存 & 长上下文阶梯 -->
        <details class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <summary class="cursor-pointer text-xs font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.modelPricing.advancedOptions') }}
          </summary>
          <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <label class="mb-1 block text-xs text-gray-600 dark:text-gray-400">
                {{ t('admin.modelPricing.cacheWrite1hLabel') }}
              </label>
              <input
                v-model="editDialog.cacheWrite1hInput"
                type="number"
                step="0.0001"
                min="0"
                class="input font-mono text-xs"
                placeholder="0"
              />
            </div>
            <div>
              <label class="mb-1 block text-xs text-gray-600 dark:text-gray-400">
                {{ t('admin.modelPricing.longContextThresholdLabel') }}
              </label>
              <input
                v-model.number="editDialog.form.long_context_threshold"
                type="number"
                step="1000"
                min="0"
                class="input font-mono text-xs"
              />
            </div>
            <div>
              <label class="mb-1 block text-xs text-gray-600 dark:text-gray-400">
                {{ t('admin.modelPricing.longContextInputMultLabel') }}
              </label>
              <input
                v-model.number="editDialog.form.long_context_input_multiplier"
                type="number"
                step="0.1"
                min="0"
                class="input font-mono text-xs"
              />
            </div>
            <div>
              <label class="mb-1 block text-xs text-gray-600 dark:text-gray-400">
                {{ t('admin.modelPricing.longContextOutputMultLabel') }}
              </label>
              <input
                v-model.number="editDialog.form.long_context_output_multiplier"
                type="number"
                step="0.1"
                min="0"
                class="input font-mono text-xs"
              />
            </div>
          </div>
        </details>
      </form>

      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-secondary" :disabled="editDialog.saving" @click="closeEditModal">
            {{ t('common.cancel', 'Cancel') }}
          </button>
          <button
            type="submit"
            form="model-pricing-edit-form"
            class="btn btn-primary"
            :disabled="editDialog.saving"
          >
            {{ editDialog.saving ? t('common.saving', 'Saving...') : t('common.save', 'Save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- 同步配置弹窗 -->
    <BaseDialog
      :show="syncSettingsDialog.open"
      :title="t('admin.modelPricing.syncSettingsTitle')"
      width="normal"
      @close="syncSettingsDialog.open = false"
    >
      <form id="sync-settings-form" class="space-y-4" @submit.prevent="submitSyncSettings">
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.modelPricing.autoSyncToggle') }}
          </span>
          <Toggle v-model="syncSettingsDialog.form.auto_sync_enabled" />
        </div>

        <div>
          <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.modelPricing.syncIntervalLabel') }}
          </label>
          <select v-model.number="syncSettingsDialog.form.sync_interval_hours" class="input text-sm">
            <option v-for="hours in intervalOptions" :key="hours" :value="hours">
              {{ t('admin.modelPricing.everyNHours', { hours }) }}
            </option>
          </select>
        </div>

        <div>
          <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.modelPricing.modelsDevUrlLabel') }}
          </label>
          <input
            v-model="syncSettingsDialog.form.models_dev_url"
            type="url"
            class="input font-mono text-xs"
            placeholder="https://models.dev/api.json"
            required
          />
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.modelPricing.modelsDevUrlHint') }}
          </p>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="savingSyncConfig"
            @click="syncSettingsDialog.open = false"
          >
            {{ t('common.cancel', 'Cancel') }}
          </button>
          <button
            type="submit"
            form="sync-settings-form"
            class="btn btn-primary"
            :disabled="savingSyncConfig"
          >
            {{ savingSyncConfig ? t('common.saving', 'Saving...') : t('common.save', 'Save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- 重置 / 删除确认弹窗 -->
    <ConfirmDialog
      :show="resetDialog.open"
      :title="t('admin.modelPricing.resetConfirmTitle')"
      :message="resetDialogMessage"
      :danger="Boolean(resetDialog.target?.is_custom_only && selectedGroupId === 0)"
      @confirm="executeReset"
      @cancel="resetDialog.open = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import Pagination from '@/components/common/Pagination.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { adminAPI } from '@/api/admin'
import type {
  ModelPricingListItem,
  ModelsDevCandidate,
  ModelsDevProviderSummary,
  PricingSyncConfig,
  PricingSyncStatus
} from '@/api/admin/modelPricing'
import type { AdminGroup } from '@/types'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()

const intervalOptions = [6, 12, 24, 48, 72, 168]

const syncStatus = ref<PricingSyncStatus | null>(null)
const syncing = ref(false)
const savingSyncConfig = ref(false)

const groups = ref<AdminGroup[]>([])
const selectedGroupId = ref<number>(0)

const loading = ref(false)
const items = ref<ModelPricingListItem[]>([])
const providers = ref<ModelsDevProviderSummary[]>([])
const searchQuery = ref('')
const providerFilter = ref('all')
const statusFilter = ref<'all' | 'overridden' | 'multi_provider'>('all')
const switchingModelId = ref<string | null>(null)

const pagination = reactive({
  page: 1,
  page_size: 50,
  total: 0
})

let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null

const filterTabs = computed(() => [
  { value: 'all' as const, label: t('admin.modelPricing.filterAll') },
  { value: 'overridden' as const, label: t('admin.modelPricing.filterOverridden') },
  { value: 'multi_provider' as const, label: t('admin.modelPricing.filterMultiProvider') }
])

const selectedGroupName = computed(() => {
  if (selectedGroupId.value === 0) return t('admin.modelPricing.defaultScopeShort')
  const g = groups.value.find((item) => item.id === selectedGroupId.value)
  return g ? g.name : `#${selectedGroupId.value}`
})

/** 参考 cc-switch formatUnitPrice：$4.00 / $0.075（至少两位小数，最多四位小数） */
function formatUnitPrice(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '$0.00'
  const fixed = value.toFixed(4).replace(/0+$/, '')
  const [whole, frac = ''] = fixed.split('.')
  return `$${whole}.${frac.padEnd(2, '0')}`
}

function formatFullDate(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

function formatCandidateOption(cand: ModelsDevCandidate): string {
  const prices = `${formatUnitPrice(cand.pricing.input_per_million)} / ${formatUnitPrice(cand.pricing.output_per_million)}`
  const rec = cand.is_recommended ? ` [${t('admin.modelPricing.recommendedTag')}]` : ''
  return `${cand.provider_name} · ${prices}${rec}`
}

function groupOverrideCount(groupId: number): number {
  return syncStatus.value?.group_override_counts?.[String(groupId)] ?? 0
}

function sourceBadgeLabel(row: ModelPricingListItem): string {
  switch (row.source) {
    case 'default_auto':
      return t('admin.modelPricing.sourceDefaultAuto')
    case 'default_provider':
      return t('admin.modelPricing.sourceDefaultProvider')
    case 'default_custom':
      return t('admin.modelPricing.sourceDefaultCustom')
    case 'inherited_default':
      return t('admin.modelPricing.sourceInheritedDefault')
    case 'group_provider':
      return t('admin.modelPricing.sourceGroupProvider')
    case 'group_custom':
      return t('admin.modelPricing.sourceGroupCustom')
    default:
      return row.source
  }
}

function sourceBadgeClass(row: ModelPricingListItem): string {
  const base = 'inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium'
  switch (row.source) {
    case 'group_custom':
    case 'group_provider':
      return `${base} bg-amber-100 text-amber-800 dark:bg-amber-900/35 dark:text-amber-300`
    case 'default_custom':
      return `${base} bg-purple-100 text-purple-800 dark:bg-purple-900/35 dark:text-purple-300`
    case 'default_provider':
      return `${base} bg-blue-100 text-blue-800 dark:bg-blue-900/35 dark:text-blue-300`
    case 'inherited_default':
      return `${base} bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400`
    default:
      return `${base} bg-emerald-50 text-emerald-700 dark:bg-emerald-900/25 dark:text-emerald-300`
  }
}

function showDefaultComparison(
  row: ModelPricingListItem,
  field: 'input_per_million' | 'output_per_million' | 'cache_read_per_million' | 'cache_write_per_million'
): boolean {
  if (selectedGroupId.value <= 0 || !row.is_group_overridden || !row.default_pricing) return false
  return Math.abs((row.effective_pricing[field] ?? 0) - (row.default_pricing[field] ?? 0)) > 1e-9
}

function canResetRow(row: ModelPricingListItem): boolean {
  if (selectedGroupId.value > 0) {
    return row.is_group_overridden
  }
  return row.is_default_modified || row.is_custom_only
}

function resetButtonTitle(row: ModelPricingListItem): string {
  if (selectedGroupId.value > 0) return t('admin.modelPricing.resetToDefault')
  if (row.is_custom_only) return t('admin.modelPricing.deleteCustomModel')
  return t('admin.modelPricing.resetToCache')
}

async function loadStatus() {
  try {
    syncStatus.value = await adminAPI.modelPricing.getStatus()
  } catch (err: any) {
    appStore.showError(err?.message || 'Failed to load model pricing status')
  }
}

async function loadGroups() {
  try {
    const res = await adminAPI.groups.getAll()
    groups.value = Array.isArray(res) ? res : []
  } catch {
    groups.value = []
  }
}

async function loadModels() {
  loading.value = true
  try {
    const res = await adminAPI.modelPricing.listModels({
      group_id: selectedGroupId.value > 0 ? selectedGroupId.value : undefined,
      search: searchQuery.value.trim() || undefined,
      provider: providerFilter.value !== 'all' ? providerFilter.value : undefined,
      filter: statusFilter.value,
      page: pagination.page,
      page_size: pagination.page_size
    })
    items.value = res.items || []
    pagination.total = res.total || 0
    if (Array.isArray(res.providers)) {
      providers.value = res.providers
    }
  } catch (err: any) {
    appStore.showError(err?.message || 'Failed to load model pricing catalog')
  } finally {
    loading.value = false
  }
}

function selectGroupScope(groupId: number) {
  if (selectedGroupId.value === groupId) return
  selectedGroupId.value = groupId
  pagination.page = 1
  void loadModels()
}

function handleSearchInput() {
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  searchDebounceTimer = setTimeout(() => {
    pagination.page = 1
    void loadModels()
  }, 220)
}

function handleSearchEnter() {
  const trimmed = searchQuery.value.trim().toLowerCase()
  if (!trimmed) return
  const exact = items.value.find((item) => item.model_id.toLowerCase() === trimmed)
  if (exact) {
    openEditModal(exact)
  } else {
    pagination.page = 1
    void loadModels()
  }
}

function clearSearch() {
  searchQuery.value = ''
  pagination.page = 1
  void loadModels()
}

function handleFilterChange() {
  pagination.page = 1
  void loadModels()
}

function selectStatusFilter(value: 'all' | 'overridden' | 'multi_provider') {
  if (statusFilter.value === value) return
  statusFilter.value = value
  pagination.page = 1
  void loadModels()
}

function handlePageChange(page: number) {
  pagination.page = page
  void loadModels()
}

function handlePageSizeChange(pageSize: number) {
  pagination.page_size = pageSize
  pagination.page = 1
  void loadModels()
}

async function saveSyncConfigPatch(patch: Partial<PricingSyncConfig>) {
  if (!syncStatus.value) return
  savingSyncConfig.value = true
  try {
    const next: PricingSyncConfig = {
      ...syncStatus.value.config,
      ...patch
    }
    syncStatus.value = await adminAPI.modelPricing.updateSyncConfig(next)
    appStore.showSuccess(t('admin.modelPricing.syncConfigSaved'))
  } catch (err: any) {
    appStore.showError(t('admin.modelPricing.syncConfigFailed', { error: err?.message || String(err) }))
  } finally {
    savingSyncConfig.value = false
  }
}

function handleToggleAutoSync(enabled: boolean) {
  void saveSyncConfigPatch({ auto_sync_enabled: enabled })
}

function handleIntervalChange(event: Event) {
  const val = Number((event.target as HTMLSelectElement).value)
  if (!Number.isFinite(val) || val <= 0) return
  void saveSyncConfigPatch({ sync_interval_hours: val })
}

async function handleSyncNow() {
  syncing.value = true
  try {
    const updated = await adminAPI.modelPricing.syncNow()
    syncStatus.value = updated
    appStore.showSuccess(
      t('admin.modelPricing.syncSuccess', {
        providers: updated.meta.provider_count,
        models: updated.meta.model_group_count,
        entries: updated.meta.raw_entry_count
      })
    )
    await loadModels()
  } catch (err: any) {
    await loadStatus()
    appStore.showError(t('admin.modelPricing.syncFailed', { error: err?.message || String(err) }))
  } finally {
    syncing.value = false
  }
}

async function handleCandidateSelect(row: ModelPricingListItem, event: Event) {
  const candidateKey = (event.target as HTMLSelectElement).value
  if (!candidateKey || candidateKey === row.active_candidate_key) return

  switchingModelId.value = row.model_id
  try {
    await adminAPI.modelPricing.selectProvider({
      group_id: selectedGroupId.value > 0 ? selectedGroupId.value : undefined,
      model_id: row.model_id,
      candidate_key: candidateKey
    })
    appStore.showSuccess(t('admin.modelPricing.switchProviderSuccess', { model: row.model_id }))
    await Promise.all([loadStatus(), loadModels()])
  } catch (err: any) {
    appStore.showError(t('admin.modelPricing.switchProviderFailed', { error: err?.message || String(err) }))
    await loadModels()
  } finally {
    switchingModelId.value = null
  }
}

// --- Edit / Add Modal ---
const editDialog = reactive({
  open: false,
  isNew: false,
  saving: false,
  candidates: [] as ModelsDevCandidate[],
  prefillCandidateKey: '',
  cacheWrite1hInput: '' as string | number,
  form: {
    model_id: '',
    display_name: '',
    input_per_million: 0,
    output_per_million: 0,
    cache_read_per_million: 0,
    cache_write_per_million: 0,
    long_context_threshold: 0,
    long_context_input_multiplier: 0,
    long_context_output_multiplier: 0
  }
})

function openAddCustomModal(initialModelId = '') {
  editDialog.isNew = true
  editDialog.candidates = []
  editDialog.prefillCandidateKey = ''
  editDialog.cacheWrite1hInput = ''
  editDialog.form = {
    model_id: initialModelId,
    display_name: '',
    input_per_million: 0,
    output_per_million: 0,
    cache_read_per_million: 0,
    cache_write_per_million: 0,
    long_context_threshold: 0,
    long_context_input_multiplier: 0,
    long_context_output_multiplier: 0
  }
  editDialog.open = true
}

function openEditModal(row: ModelPricingListItem) {
  editDialog.isNew = false
  editDialog.candidates = row.candidates || []
  editDialog.prefillCandidateKey = ''
  const p = row.effective_pricing
  editDialog.cacheWrite1hInput = p.cache_write_1h_per_million != null ? String(p.cache_write_1h_per_million) : ''
  editDialog.form = {
    model_id: row.model_id,
    display_name: row.display_name || '',
    input_per_million: p.input_per_million ?? 0,
    output_per_million: p.output_per_million ?? 0,
    cache_read_per_million: p.cache_read_per_million ?? 0,
    cache_write_per_million: p.cache_write_per_million ?? 0,
    long_context_threshold: p.long_context_threshold ?? 0,
    long_context_input_multiplier: p.long_context_input_multiplier ?? 0,
    long_context_output_multiplier: p.long_context_output_multiplier ?? 0
  }
  editDialog.open = true
}

function closeEditModal() {
  if (editDialog.saving) return
  editDialog.open = false
}

function applyPrefillCandidate() {
  const cand = editDialog.candidates.find((c) => c.candidate_key === editDialog.prefillCandidateKey)
  if (!cand) return
  editDialog.form.display_name = cand.display_name || editDialog.form.display_name
  editDialog.form.input_per_million = cand.pricing.input_per_million ?? 0
  editDialog.form.output_per_million = cand.pricing.output_per_million ?? 0
  editDialog.form.cache_read_per_million = cand.pricing.cache_read_per_million ?? 0
  editDialog.form.cache_write_per_million = cand.pricing.cache_write_per_million ?? 0
  editDialog.form.long_context_threshold = cand.pricing.long_context_threshold ?? 0
  editDialog.form.long_context_input_multiplier = cand.pricing.long_context_input_multiplier ?? 0
  editDialog.form.long_context_output_multiplier = cand.pricing.long_context_output_multiplier ?? 0
}

async function submitEditModal() {
  const modelId = editDialog.form.model_id.trim()
  if (!modelId) {
    appStore.showError(t('admin.modelPricing.modelIdRequired'))
    return
  }

  const nums = [
    editDialog.form.input_per_million,
    editDialog.form.output_per_million,
    editDialog.form.cache_read_per_million,
    editDialog.form.cache_write_per_million,
    editDialog.form.long_context_threshold,
    editDialog.form.long_context_input_multiplier,
    editDialog.form.long_context_output_multiplier
  ]
  if (nums.some((n) => !Number.isFinite(n) || n < 0)) {
    appStore.showError(t('admin.modelPricing.invalidPriceValue'))
    return
  }

  let cacheWrite1h: number | null = null
  const raw1h = String(editDialog.cacheWrite1hInput ?? '').trim()
  if (raw1h !== '') {
    const parsed = Number(raw1h)
    if (!Number.isFinite(parsed) || parsed < 0) {
      appStore.showError(t('admin.modelPricing.invalidPriceValue'))
      return
    }
    cacheWrite1h = parsed
  }

  editDialog.saving = true
  try {
    await adminAPI.modelPricing.upsertOverride({
      group_id: selectedGroupId.value > 0 ? selectedGroupId.value : undefined,
      model_id: modelId,
      display_name: editDialog.form.display_name.trim() || undefined,
      input_per_million: Number(editDialog.form.input_per_million),
      output_per_million: Number(editDialog.form.output_per_million),
      cache_read_per_million: Number(editDialog.form.cache_read_per_million || 0),
      cache_write_per_million: Number(editDialog.form.cache_write_per_million || 0),
      cache_write_1h_per_million: cacheWrite1h,
      long_context_threshold: Number(editDialog.form.long_context_threshold || 0),
      long_context_input_multiplier: Number(editDialog.form.long_context_input_multiplier || 0),
      long_context_output_multiplier: Number(editDialog.form.long_context_output_multiplier || 0)
    })
    appStore.showSuccess(t('admin.modelPricing.saveSuccess'))
    editDialog.open = false
    await Promise.all([loadStatus(), loadModels()])
  } catch (err: any) {
    appStore.showError(t('admin.modelPricing.saveFailed', { error: err?.message || String(err) }))
  } finally {
    editDialog.saving = false
  }
}

// --- Sync Settings Dialog ---
const syncSettingsDialog = reactive({
  open: false,
  form: {
    auto_sync_enabled: true,
    sync_interval_hours: 24,
    models_dev_url: 'https://models.dev/api.json'
  }
})

function openSyncSettingsDialog() {
  if (syncStatus.value) {
    syncSettingsDialog.form = { ...syncStatus.value.config }
  }
  syncSettingsDialog.open = true
}

async function submitSyncSettings() {
  await saveSyncConfigPatch(syncSettingsDialog.form)
  syncSettingsDialog.open = false
}

// --- Reset / Delete Confirm ---
const resetDialog = reactive({
  open: false,
  target: null as ModelPricingListItem | null
})

const resetDialogMessage = computed(() => {
  const row = resetDialog.target
  if (!row) return ''
  if (selectedGroupId.value > 0) {
    return t('admin.modelPricing.resetGroupConfirmMessage', {
      group: selectedGroupName.value,
      model: row.model_id
    })
  }
  return t('admin.modelPricing.resetDefaultConfirmMessage', {
    model: row.model_id,
    extra: row.is_custom_only
      ? t('admin.modelPricing.resetDefaultDeleteExtra')
      : t('admin.modelPricing.resetDefaultRestoreExtra')
  })
})

function confirmReset(row: ModelPricingListItem) {
  resetDialog.target = row
  resetDialog.open = true
}

async function executeReset() {
  const row = resetDialog.target
  if (!row) return
  resetDialog.open = false
  try {
    await adminAPI.modelPricing.resetOverride(
      row.model_id,
      selectedGroupId.value > 0 ? selectedGroupId.value : undefined
    )
    appStore.showSuccess(t('admin.modelPricing.resetSuccess'))
    await Promise.all([loadStatus(), loadModels()])
  } catch (err: any) {
    appStore.showError(t('admin.modelPricing.resetFailed', { error: err?.message || String(err) }))
  }
}

onMounted(() => {
  void Promise.all([loadStatus(), loadGroups(), loadModels()])
})
</script>
