<template>
  <n-layout style="padding: 10px;">
    <n-card title="策略跟踪" size="small" style="margin-bottom: 10px;">
      <n-space vertical size="large">
        <n-tabs type="segment" v-model:value="activeStrategy" @update:value="onSwitchStrategy">
          <n-tab v-for="s in strategies" :key="s.id" :name="String(s.id)">{{ s.name }}</n-tab>
        </n-tabs>

        <!-- 统计卡片 -->
        <n-space :size="24" v-if="stat.strategyId">
          <n-statistic label="批次数" :value="stat.batchCount || 0" />
          <n-statistic label="总票数" :value="stat.itemCount || 0" />
          <n-statistic label="次日胜率%">
            <template #default>{{ fmt2(stat.hold1WinRate) }}</template>
          </n-statistic>
          <n-statistic label="次日均涨%">
            <template #default>{{ fmt2(stat.hold1AvgPct) }}</template>
          </n-statistic>
          <n-statistic label="次日最高均涨%">
            <template #default>{{ fmt2(stat.nextHighAvg) }}</template>
          </n-statistic>
          <n-statistic label="冲高胜率%">
            <template #default>{{ fmt2(stat.nextHighWin) }}</template>
          </n-statistic>
          <n-statistic label="5日均涨%">
            <template #default>{{ fmt2(stat.hold5AvgPct) }}</template>
          </n-statistic>
          <n-statistic label="5日胜率%">
            <template #default>{{ fmt2(stat.hold5WinRate) }}</template>
          </n-statistic>
        </n-space>

        <!-- 操作区 -->
        <n-space>
          <n-button size="small" type="primary" ghost @click="showAddBatch = true">新建批次</n-button>
          <template v-if="isOvernightTab">
            <n-date-picker v-model:value="overnightDateTs" type="date" size="small" style="width: 150px"
                           :is-date-disabled="disableFutureDates" placeholder="选股日期" />
            <n-button size="small" type="error" ghost :loading="overnightLoading" @click="runOvernight">一夜选股(14:50后)</n-button>
            <n-button size="small" type="warning" ghost :loading="overnightCloseLoading" @click="runOvernightClose">收盘记录</n-button>
          </template>
          <n-button size="small" type="success" ghost :loading="importing" @click="importInbox">
            {{ importing ? '导入中...' : '扫描收件箱导入' }}
          </n-button>
          <n-button size="small" type="info" ghost @click="openInboxDir">打开收件箱文件夹</n-button>
          <n-button size="small" type="warning" ghost :loading="exporting" @click="exportCSV">导出 CSV</n-button>
          <n-button size="small" type="success" ghost :loading="exportingXlsx" @click="exportXlsxTop">导出 Excel</n-button>
          <n-button size="small" @click="refresh">刷新</n-button>
        </n-space>
        <n-text depth="3" style="font-size: 12px;">
          自动流程：把选股结果 txt 丢进收件箱（文件名 = 策略名_日期.txt，如 一夜持股_20261107.txt），
          软件每日 17:40 数据同步后自动导入并回填涨幅；也可点「扫描收件箱导入」立即处理。
          每行格式：代码 [名称] [评分]，如 600519.SH 贵州茅台 9.2
        </n-text>

        <!-- 批次列表 -->
        <n-data-table size="small" max-height="200" :columns="batchColumns" :data="batches"
                      :pagination="false" @update:checked-row-keys="onBatchCheck" />
        <n-space v-if="checkedBatches.length">
          <n-text>已选批次 {{ checkedBatches.length }} 个</n-text>
          <n-button size="tiny" type="error" ghost @click="delCheckedBatches">删除选中批次</n-button>
        </n-space>

        <!-- 明细表 -->
        <n-data-table size="small" max-height="380" :columns="itemColumns" :data="items"
                      :pagination="{pageSize: 50}" />
      </n-space>
    </n-card>

    <!-- 新建批次弹窗 -->
    <n-modal v-model:show="showAddBatch" preset="card" title="新建批次" style="width: 560px;">
      <n-space vertical>
        <n-input v-model:value="newBatchDate" placeholder="批次日期，如 2026-11-07（空=今天）" />
        <n-input v-model:value="newBatchCodes" type="textarea" rows="8"
                 placeholder="每行一只票：代码 [名称] [评分]&#10;600519.SH 贵州茅台 9.2&#10;000001.SZ 平安银行 8.1" />
        <n-button type="primary" :loading="creating" @click="createBatch">创建并回填</n-button>
      </n-space>
    </n-modal>

    <!-- 一夜选股/收盘记录结果弹窗 -->
    <n-modal v-model:show="showOvernightModal" :title="overnightModalTitle" preset="card" style="width: 760px;">
      <n-space vertical>
        <n-alert :type="overnightScan && overnightScan.weakDay ? 'warning' : 'info'" :bordered="false">
          {{ overnightScan ? overnightScan.note : '' }}
        </n-alert>
        <n-data-table v-if="overnightScan && overnightScan.cands && overnightScan.cands.length"
                      size="small" max-height="360" :columns="overnightCandColumns"
                      :data="overnightScan.cands.slice(0, 8).map((c, i) => ({
                        idx: i + 1, code: c.code, name: c.name, price: c.price,
                        net: c.net, oldScore: c.oldScore, v3Score: c.v3Score,
                        mark: (c.star === '★' ? '★' : (c.star === '半★' ? '半★' : '')) + (c.strict || '') + (c.ztFlag ? ' !涨停难买' : ''),
                        weightTxt: c.weight > 0 ? Math.round(c.weight * 100) + '%' : (c.v3Score < 0 ? '不参与' : '-')
                      }))" />
        <n-empty v-else-if="overnightScan && overnightScan.trading" description="无信号" />
      </n-space>
    </n-modal>
  </n-layout>
</template>

<script setup>
import {onBeforeUnmount, onMounted, ref, h, computed} from 'vue'
import {
  ListTrackStrategies, AddTrackStrategy, DelTrackStrategy, GetTrackBatches,
  GetRecentTrackItems, GetTrackStats, CreateTrackBatch, ImportTrackInboxFolder,
  OpenTrackInboxDir, ExportTrackCSV, ExportTrackExcel, DeleteTrackBatch, DeleteTrackItem,
  RunOvernightScan, RunOvernightAppendCloseSheet, RunOvernightScanJSON
} from '../../wailsjs/go/main/App'
import {EventsOn, EventsOff} from '../../wailsjs/runtime'

const strategies = ref([])
const activeStrategy = ref('')
const stat = ref({})
const batches = ref([])
const items = ref([])
const checkedBatches = ref([])
const importing = ref(false)
const exporting = ref(false)
const creating = ref(false)
const showAddBatch = ref(false)
const overnightLoading = ref(false)
const overnightCloseLoading = ref(false)
const overnightResult = ref('')
const showOvernightModal = ref(false)
const overnightModalTitle = ref('一夜选股结果')
const overnightScan = ref(null)   // 结构化结果(RunOvernightScanJSON)
const overnightDateTs = ref(Date.now())
const isOvernightTab = computed(() => {
  const s = strategies.value.find(x => String(x.id) === activeStrategy.value)
  return !!s && /一夜/.test(s.name || '')
})

function disableFutureDates(ts) { return ts > Date.now() }

function fmtDay(ts) {
  const d = new Date(ts)
  const p = (n) => String(n).padStart(2, '0')
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate())
}

function runOvernight() {
  overnightLoading.value = true
  overnightScan.value = null
  RunOvernightScanJSON(fmtDay(overnightDateTs.value)).then((res) => {
    overnightLoading.value = false
    overnightScan.value = res || null
    overnightModalTitle.value = '一夜选股结果 (' + fmtDay(overnightDateTs.value) + ')'
    showOvernightModal.value = true
  }).catch((e) => {
    overnightLoading.value = false
    overnightModalTitle.value = '一夜选股结果'
    overnightScan.value = { trading: false, note: '失败: ' + e, cands: [] }
    showOvernightModal.value = true
  })
}

function runOvernightClose() {
  overnightCloseLoading.value = true
  RunOvernightAppendCloseSheet('', fmtDay(overnightDateTs.value), '').then((res) => {
    overnightCloseLoading.value = false
    overnightModalTitle.value = '收盘记录结果'
    overnightScan.value = { trading: true, note: res || '无返回', cands: [] }
    showOvernightModal.value = true
  }).catch((e) => {
    overnightCloseLoading.value = false
    overnightModalTitle.value = '收盘记录结果'
    overnightScan.value = { trading: true, note: '失败: ' + e, cands: [] }
    showOvernightModal.value = true
  })
}

const overnightCandColumns = [
  { title: '#', key: 'idx', width: 40 },
  { title: '代码', key: 'code', width: 80 },
  { title: '名称', key: 'name', width: 100 },
  { title: '现价', key: 'price', width: 70 },
  { title: '净涨%#', key: 'net', width: 70 },
  { title: '旧评分', key: 'oldScore', width: 65 },
  { title: 'v3', key: 'v3Score', width: 50 },
  { title: '标记', key: 'mark', width: 80 },
  { title: '仓位', key: 'weightTxt', width: 70 }
]
const newBatchDate = ref('')
const newBatchCodes = ref('')

const fmt2 = (v) => (v === undefined || v === null || v === 0 ? '-' : Number(v).toFixed(2))

const mkCol = (title, key, width) => ({title, key, width: width || 90})

const batchColumns = [
  {type: 'selection'},
  mkCol('批次日期', 'tradeDate', 110),
  mkCol('来源', 'source', 70),
  h('备注', 'remark'),
  {
    title: '操作', key: 'op', width: 150,
    render(row) {
      return h('div', null, [
        h('a', {
          style: 'color:#63e2b7;cursor:pointer;margin-right:12px',
          onClick: () => exportExcel(row)
        }, '导出Excel'),
        h('a', {
          style: 'color:#e88080;cursor:pointer',
          onClick: () => delBatch(row)
        }, '删除')
      ])
    }
  },
]

const itemColumns = [
  mkCol('日期', 'tradeDate', 105),
  mkCol('代码', 'stockCode', 100),
  mkCol('名称', 'stockName', 90),
  mkCol('评分', 'score', 70),
  mkCol('次日开%', 'nextOpenPct', 85),
  mkCol('次日高%', 'nextHighPct', 85),
  mkCol('次日收%', 'nextClosePct', 85),
  mkCol('1日%', 'hold1Pct', 75),
  mkCol('2日%', 'hold2Pct', 75),
  mkCol('3日%', 'hold3Pct', 75),
  mkCol('4日%', 'hold4Pct', 75),
  mkCol('5日%', 'hold5Pct', 75),
  mkCol('6日%', 'hold6Pct', 75),
  mkCol('7日%', 'hold7Pct', 75),
  mkCol('8日%', 'hold8Pct', 75),
  {
    title: '操作', key: 'op', width: 70,
    render(row) {
      return h('a', {
        style: 'color:#e88080;cursor:pointer',
        onClick: () => delItem(row)
      }, '删除')
    }
  },
]

const pctColor = (row, key) => {
  const v = row[key]
  if (v === undefined || v === null || v === 0) return {color: 'rgba(128,128,128,0.5)'}
  return {color: v > 0 ? '#e88080' : '#63e2b7', fontWeight: 600}
}

// 上色（正确渲染 VNode；旧写法多包了一层函数导致函数源码被当字符串渲染成乱码）
for (const k of ['nextOpenPct', 'nextHighPct', 'nextClosePct', 'hold1Pct', 'hold2Pct', 'hold3Pct', 'hold4Pct', 'hold5Pct', 'hold6Pct', 'hold7Pct', 'hold8Pct']) {
  const col = itemColumns.find(c => c.key === k)
  col.render = (row) => {
    const v = row[k]
    const text = (v === undefined || v === null || v === 0) ? '-' : Number(v).toFixed(2)
    return h('span', {style: pctColor(row, k)}, text)
  }
}

function loadStrategies() {
  ListTrackStrategies().then((res) => {
    strategies.value = res || []
    if (strategies.value.length && !activeStrategy.value) {
      activeStrategy.value = String(strategies.value[0].id)
      refresh()
    }
  })
}

function onSwitchStrategy() {
  checkedBatches.value = []
  refresh()
}

function refresh() {
  if (!activeStrategy.value) return
  const id = Number(activeStrategy.value)
  GetTrackBatches(id).then((res) => { batches.value = res || [] })
  GetRecentTrackItems(id, 200).then((res) => { items.value = res || [] })
  GetTrackStats().then((res) => {
    stat.value = (res || []).find(s => String(s.strategyId) === activeStrategy.value) || {}
  })
}

function importInbox() {
  importing.value = true
  ImportTrackInboxFolder().then((res) => {
    importing.value = false
    const msg = res || '完成'
    if (window.$message) { window.$message.info(msg, {duration: 6000}) }
    else { alert(msg) } // 兜底：message组件未初始化时用系统弹窗，绝不静默
    refresh()
  }).catch((e) => {
    importing.value = false
    const msg = '扫描导入失败: ' + (e && e.message ? e.message : e)
    if (window.$message) { window.$message.error(msg, {duration: 6000}) } else { alert(msg) }
  })
}

function openInboxDir() {
  OpenTrackInboxDir().then((dir) => {
    if (window.$message) window.$message.info('收件箱: ' + dir)
  })
}

function exportCSV() {
  exporting.value = true
  ExportTrackCSV(Number(activeStrategy.value)).then((res) => {
    exporting.value = false
    if (window.$message) window.$message.success(res)
  }).catch(() => { exporting.value = false })
}

function createBatch() {
  creating.value = true
  CreateTrackBatch(Number(activeStrategy.value), newBatchDate.value, newBatchCodes.value).then((res) => {
    creating.value = false
    if (window.$message) window.$message.info(res)
    showAddBatch.value = false
    newBatchCodes.value = ''
    refresh()
  }).catch(() => { creating.value = false })
}

function delItem(row) {
  DeleteTrackItem(row.id).then(() => { refresh() })
}

// 按原版模板导出该批次为Excel（需模板文件 data/templates/<策略名>.xlsx）
const exportingXlsx = ref(false)
// 顶部按钮：导出当前策略最新批次（原版模板格式）
function exportXlsxTop() {
  if (!batches.value.length) {
    window.$message ? window.$message.warning('当前策略没有批次，先导入或新建批次') : alert('当前策略没有批次')
    return
  }
  const latest = batches.value[0] // 已按日期倒序，取最新
  exportExcel(latest)
}
function exportExcel(row) {
  const sid = Number(activeStrategy.value)
  ExportTrackExcel(sid, row.id).then((res) => {
    const msg = res || ''
    if (msg.includes('export') && msg.includes('.xlsx')) {
      window.$message ? window.$message.success('已导出: ' + msg, {duration: 6000}) : alert('已导出: ' + msg)
    } else {
      window.$message ? window.$message.warning(msg, {duration: 8000}) : alert(msg)
    }
  }).catch((e) => {
    const msg = '导出失败: ' + (e && e.message ? e.message : e)
    window.$message ? window.$message.error(msg, {duration: 8000}) : alert(msg)
  })
}

function onBatchCheck(keys) {
  checkedBatches.value = keys
}

function delCheckedBatches() {
  if (!checkedBatches.value.length) return
  Promise.all(checkedBatches.value.map((id) => DeleteTrackBatch(id))).then(() => {
    checkedBatches.value = []
    refresh()
  })
}

onMounted(() => {
  loadStrategies()
  EventsOn('dailyKLineSyncDone', () => { refresh() })
})
onBeforeUnmount(() => { EventsOff('dailyKLineSyncDone') })
</script>
