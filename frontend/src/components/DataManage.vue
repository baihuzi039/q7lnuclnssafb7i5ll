<template>
  <n-layout style="padding: 10px;">
    <n-card title="本地日线数据库" size="small" style="margin-bottom: 10px;">
      <n-space vertical size="large">
        <n-space :size="30">
          <n-statistic label="股票总数" :value="status.totalStocks || 0" />
          <n-statistic label="已同步股票" :value="status.syncedStocks || 0" />
          <n-statistic label="日线总行数" :value="status.totalRows || 0" />
          <n-statistic label="最新交易日">
            <template #default>{{ status.latestDay || '-' }}</template>
          </n-statistic>
          <n-statistic label="上次同步">
            <template #default>{{ status.lastSyncTime || '本次运行尚未同步' }}</template>
          </n-statistic>
        </n-space>
        <n-space>
          <n-button size="small" type="primary" ghost :loading="status.running" @click="syncNow">
            {{ status.running ? '同步中...' : '立即同步日线' }}
          </n-button>
          <n-button size="small" @click="loadStatus">刷新状态</n-button>
        </n-space>
        <n-text depth="3" style="font-size: 12px;">
          说明：软件会在每个工作日 17:40 自动增量同步全市场日线，启动后 2 分钟也会补跑一次。
          同步数据供回测与选股使用，不发网络请求、不消耗 AI 额度。首次同步全市场约需 10-20 分钟。
        </n-text>
        <n-progress v-if="status.totalStocks > 0" type="line"
                    :percentage="syncPercent" indicator-placement="inside" processing />
      </n-space>
    </n-card>

    <n-card title="本地K线查询（验证数据完整性）" size="small">
      <n-space>
        <n-input v-model:value="queryCode" size="small" placeholder="股票代码，如 600519.SH"
                 style="width: 220px;" @keyup.enter="queryKLine" />
        <n-button size="small" type="primary" ghost @click="queryKLine">查询</n-button>
      </n-space>
      <n-data-table size="small" style="margin-top: 10px;" max-height="420"
                    :columns="klineColumns" :data="klineRows" :pagination="{pageSize: 20}" />
    </n-card>
  </n-layout>
</template>

<script setup>
import {computed, h, onBeforeUnmount, onMounted, ref} from 'vue'
import {GetDailyKLineSyncStatus, GetLocalDailyKLine, SyncDailyKLineNow}
  from '../../wailsjs/go/main/App'
import {EventsOn, EventsOff} from '../../wailsjs/runtime'

const status = ref({})
const queryCode = ref('')
const klineRows = ref([])

const syncPercent = computed(() => {
  if (!status.value.totalStocks) return 0
  return Math.round((status.value.syncedStocks || 0) / status.value.totalStocks * 100)
})

const klineColumns = [
  {title: '日期', key: 'day', width: 110},
  {title: '开盘', key: 'open', width: 80},
  {title: '收盘', key: 'close', width: 80},
  {title: '最高', key: 'high', width: 80},
  {title: '最低', key: 'low', width: 80},
  {title: '涨跌幅%', key: 'pctChange', width: 90},
  {title: '换手率%', key: 'turnover', width: 90},
]

function loadStatus() {
  GetDailyKLineSyncStatus().then((res) => { status.value = res })
}

function syncNow() {
  status.value.running = true
  SyncDailyKLineNow().then((res) => { if (window.$message) window.$message.info(res) })
}

function queryKLine() {
  let code = (queryCode.value || '').trim()
  if (!code) return
  if (/^\d{6}$/.test(code)) {
    code = code.startsWith('6') ? code + '.SH' : code + '.SZ'
  }
  GetLocalDailyKLine(code, 120).then((rows) => { klineRows.value = rows || [] })
}

onMounted(() => {
  loadStatus()
  EventsOn('dailyKLineSyncDone', () => { loadStatus() })
})
onBeforeUnmount(() => { EventsOff('dailyKLineSyncDone') })
</script>
