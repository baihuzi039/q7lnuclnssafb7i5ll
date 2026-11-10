<template>
  <n-layout style="padding: 10px;">
    <n-card title="本地日线数据库" size="small" style="margin-bottom: 10px;">

      <n-space vertical size="large">
        <n-space :size="24">
          <n-statistic label="沪深A股" :value="status.totalAStock || 0" />
          <n-statistic label="北交所" :value="status.totalBJ || 0" />
          <n-statistic label="场内ETF" :value="status.totalEtf || 0" />
        </n-space>
        <n-space :size="24" align="center">
          <n-statistic label="已同步" :value="status.syncedStocks || 0" />
          <n-statistic label="日线总行数" :value="status.totalRows || 0" />
          <n-statistic label="最新交易日">
            <template #default>{{ status.latestDay || '-' }}</template>
          </n-statistic>
          <div class="last-sync-chip">
            <div class="last-sync-label">上次同步</div>
            <div class="last-sync-value">{{ status.lastSyncTime || (status.latestDay ? '未记录（库内最新 ' + status.latestDay + '）' : '本次运行尚未同步') }}</div>
          </div>
        </n-space>
        <n-space :size="24">
          <n-statistic label="竞价记录" :value="auctionStat.rows || 0" />
          <n-statistic label="竞价天数" :value="auctionStat.days || 0" />
          <n-statistic label="竞价最新日">
            <template #default>{{ auctionStat.latest || '-' }}</template>
          </n-statistic>
          <n-statistic v-if="status.totalOther > 0" label="异常代码" :value="status.totalOther" />
        </n-space>
        <n-space>
          <n-button size="small" type="primary" ghost :loading="status.running" @click="syncNow">
            {{ status.running ? '同步中...' : '接口同步增量' }}
          </n-button>
          <n-button size="small" type="success" ghost :loading="importing" @click="pickImport">
            {{ importing ? '导入中...' : '导入通达信数据包' }}
          </n-button>
          <n-button size="small" type="info" ghost :loading="importingAuction" @click="pickAuctionImport">
            {{ importingAuction ? '导入中...' : '导入竞价CSV' }}
          </n-button>
          <n-button size="small" type="info" ghost @click="snapAuction">抓今日竞价快照</n-button>
          <n-button size="small" type="warning" ghost @click="exportBackup">导出数据库备份</n-button>
          <n-button size="small" @click="loadStatus">刷新状态</n-button>
        </n-space>
        <n-text depth="3" style="font-size: 12px;">
          说明：进度条 = 已同步 ÷（沪深A股+北交所），ETF与退市代码拉不到日线属正常，不计入分母。
          软件会在每个工作日 17:40 自动增量同步，启动后 2 分钟也会补跑一次。数据供回测与选股使用，零 AI 消耗。
        </n-text>
        <n-progress v-if="status.totalStocks > 0" type="line"
                    :percentage="syncPercent" :status="syncPercent >= 100 ? 'success' : 'default'" indicator-placement="inside" />
        <n-text depth="3" style="font-size: 12px;">
          数据覆盖 {{ status.syncedStocks || 0 }} 只代码（含已退市），当前在市同步目标 {{ status.totalStocks || 0 }} 只（沪深A股+北交所）。
        </n-text>
      </n-space>
    </n-card>

    <n-card title="分类导出 CSV" size="small" style="margin-bottom: 10px;">
      <n-space vertical size="medium">
        <n-space>
          <n-select v-model:value="expType" size="small" style="width: 160px;" :options="expTypeOptions" />
          <n-input v-model:value="expCode" size="small" placeholder="股票代码（空=全部）" style="width: 180px;" />
          <n-date-picker v-model:value="expRange" size="small" type="daterange" clearable />
          <n-button size="small" type="primary" ghost :loading="exporting" @click="doExport">导出 CSV</n-button>
        </n-space>
        <n-text depth="3" style="font-size: 12px;">
          日线支持按股票+日期区间筛选；导出的 CSV 可直接发灵犀做深度分析。整库备份请用上方「导出数据库备份」。
        </n-text>
      </n-space>
    </n-card>

    <n-card title="本地K线查询（验证数据完整性）" size="small">
      <n-space>
        <n-input v-model:value="queryCode" size="small" placeholder="股票代码，如 600519.SH"
                 style="width: 220px;" @keyup.enter="queryKLine" />
        <n-button size="small" type="primary" ghost :loading="klineLoading" @click="queryKLine">查询</n-button>
      </n-space>
      <n-data-table size="small" style="margin-top: 10px;" max-height="420"
                    :columns="klineColumns" :data="klineRows" :pagination="{pageSize: 20}" />
    </n-card>
  </n-layout>
</template>

<script setup>
import {computed, h, onBeforeUnmount, onMounted, ref} from 'vue'
import {GetDailyKLineSyncStatus, GetLocalDailyKLine, SyncDailyKLineNow, ImportTdxDayFolder, ExportDatabaseBackup, ExportDataCSV, ImportAuctionCSV, SnapshotAuctionNow, GetAuctionCount, FetchSingleStockKLine}
  from '../../wailsjs/go/main/App'
import {EventsOn, EventsOff} from '../../wailsjs/runtime'
import {SelectDirectory} from '../../wailsjs/go/main/App'

const status = ref({})
const queryCode = ref('')
const klineRows = ref([])
const klineLoading = ref(false)

const syncPercent = computed(() => {
  // 已同步含历史退市代码，可能大于在市目标数，封顶100%防虚高
  if (!status.value.totalStocks) return 0
  return Math.min(100, Math.round((status.value.syncedStocks || 0) / status.value.totalStocks * 100))
})

// [v9e.9.4] 最新日期排最前（默认降序）；涨跌幅红涨绿跌
const klineColumns = [
  {title: '日期', key: 'day', width: 110, sorter: 'default', defaultSortOrder: 'descend'},
  {title: '开盘', key: 'open', width: 80},
  {title: '收盘', key: 'close', width: 80},
  {title: '最高', key: 'high', width: 80},
  {title: '最低', key: 'low', width: 80},
  // [v9e.4] 口径修复: pct_change库内为百分数(1.12=+1.12%), turnover库内为小数(0.0033=0.33%)需×100
  {title: '涨跌幅%', key: 'pctChange', width: 90, sorter: 'default',
   render(row) {
     const v = row.pctChange ?? 0
     const txt = (v).toFixed(2)
     if (v > 0) return h('span', {style: 'color: #e88080; font-weight: bold'}, txt)   // 红涨
     if (v < 0) return h('span', {style: 'color: #63e2b7; font-weight: bold'}, txt)   // 绿跌
     return txt
   }},
  // [v9e.9.5] 东财f61换手率已是百分数(21.03=21.03%)，直接显示；不再×100（此前×100导致965/2103异常值）
  {title: '换手率%', key: 'turnover', width: 90, sorter: 'default', render(row) { const t=row.turnover ?? 0; return t>0 ? t.toFixed(2) : '-' }},
]

function loadStatus() {
  GetDailyKLineSyncStatus().then((res) => { status.value = res })
}

function syncNow() {
  status.value.running = true
  SyncDailyKLineNow().then((res) => { if (window.$message) window.$message.info(res) })
}

const exporting = ref(false)
const expType = ref('daily')
const expCode = ref('')
const expRange = ref(null)
const expTypeOptions = [
  {label: '日线数据', value: 'daily'},
  {label: '竞价数据', value: 'auction'},
  {label: '自选股', value: 'follow'},
  {label: 'AI推荐记录', value: 'recommend'},
  {label: '回测结果', value: 'backtest'},
]
function fmtDay(ts) {
  const d = new Date(ts)
  return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0')
}
function doExport() {
  exporting.value = true
  const req = {
    dataType: expType.value,
    stockCode: (expCode.value || '').trim(),
    startDate: expRange.value ? fmtDay(expRange.value[0]) : '',
    endDate: expRange.value ? fmtDay(expRange.value[1]) : '',
  }
  ExportDataCSV(req).then((res) => {
    exporting.value = false
    if (res) { if (window.$message) window.$message.info(res) }
  }).catch(() => { exporting.value = false })
}

function exportBackup() {
  ExportDatabaseBackup().then((res) => {
    if (res) { if (window.$message) window.$message.success(res) }
  })
}

const importingAuction = ref(false)
const auctionStat = ref({})
function pickAuctionImport() {
  // 复用目录选择：竞价CSV通常在一个文件夹里，逐个文件选也可
  SelectDirectory().then((dir) => {
    if (!dir) return
    importingAuction.value = true
    if (window.$message) window.$message.info('开始导入竞价数据')
    // 后端 ImportAuctionCSV 支持目录：目录会逐个读，非csv会报错跳过；传目录让后端递归
    ImportAuctionCSV([dir]).then((res) => {
      importingAuction.value = false
      if (window.$message) window.$message.success(res)
      loadAuctionStat()
    }).catch(() => { importingAuction.value = false })
  })
}
function snapAuction() {
  SnapshotAuctionNow().then((res) => {
    if (window.$message) window.$message.info(res || '指令已发出，但无返回信息，请重试或看控制台日志')
  }).catch((e) => {
    if (window.$message) window.$message.error('竞价快照调用失败：' + e)
  })
}
function loadAuctionStat() {
  GetAuctionCount().then((res) => { auctionStat.value = res })
}

const importing = ref(false)
function pickImport() {
  SelectDirectory().then((dir) => {
    if (!dir) return
    importing.value = true
    if (window.$message) window.$message.info('开始导入通达信日线数据，请勿关闭软件')
    ImportTdxDayFolder([dir]).then((res) => {
      importing.value = false
      if (window.$message) window.$message.success(res)
      loadStatus()
    }).catch(() => { importing.value = false })
  })
}

function queryKLine() {
  let code = (queryCode.value || '').trim()
  if (!code) return
  if (/^\d{6}$/.test(code)) {
    code = code.startsWith('6') ? code + '.SH' : code + '.SZ'
  }
  klineLoading.value = true
  GetLocalDailyKLine(code, 120).then((rows) => {
    klineRows.value = rows || []
    if (!rows || rows.length < 120) {
      // v9e.5：本地K线不足时自动联网补抓250个交易日入库后重新查询
      if (window.$message) window.$message.info('本地K线不足 ' + (rows ? rows.length : 0) + ' 根，正在联网补抓约250个交易日...')
      FetchSingleStockKLine(code, 250).then((n) => {
        if (n > 0) {
          if (window.$message) window.$message.success('已补抓 ' + n + ' 根日K')
          return GetLocalDailyKLine(code, 120).then((r) => { klineRows.value = r || [] })
        }
        if (window.$message) window.$message.warning('联网补抓失败，仅显示本地已有数据')
      }).catch(() => {
        if (window.$message) window.$message.warning('联网补抓失败，仅显示本地已有数据')
      }).finally(() => { klineLoading.value = false })
    } else {
      klineLoading.value = false
    }
  })
}

onMounted(() => {
  loadStatus()
  loadAuctionStat()
  EventsOn('dailyKLineSyncDone', () => { loadStatus() })
  EventsOn('auctionImportDone', () => { loadAuctionStat() })
  EventsOn('auctionSnapshotDone', () => { loadAuctionStat() })
  EventsOn('tdxImportDone', () => { loadStatus() })
  EventsOn('auctionSnapshotSkip', (msg) => { if (window.$message && msg) window.$message.warning(msg) })
})
onBeforeUnmount(() => { EventsOff('dailyKLineSyncDone') })
</script>

<style scoped>
.last-sync-chip {
  padding: 8px 16px;
  border-radius: 8px;
  background: rgba(24, 160, 88, 0.12);
  border: 1px solid rgba(24, 160, 88, 0.35);
  text-align: center;
}
.last-sync-label {
  font-size: 14px;
  color: rgba(255, 255, 255, 0.82);
}
.last-sync-value {
  font-size: 24px;
  font-weight: 600;
  color: #63e2b7;
  white-space: nowrap;
}
</style>
