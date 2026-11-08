<template>
  <n-layout style="padding: 10px;">
    <n-card size="small" style="margin-bottom: 10px;">
      <n-space vertical size="large">
        <!-- v9b.4: 标题栏美化(渐变下划线)+下拉按钮移到标题栏右侧蓝框位 -->
        <div class="track-header">
          <div class="track-title">策略跟踪</div>
          <n-dropdown trigger="click" :options="customDropdownOptions" @select="onCustomMenu">
            <n-button size="small" type="primary" ghost>自定义 ▾</n-button>
          </n-dropdown>
        </div>
        <n-tabs type="segment" v-model:value="activeStrategy" @update:value="onSwitchStrategy">
          <n-tab v-for="s in builtinStrategies" :key="s.id" :name="String(s.id)">{{ s.name }}</n-tab>
          <n-tab v-if="activeIsCustom" :name="activeStrategy">{{ activeStrategyName }}</n-tab>
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
          <template v-else-if="activeIsCustom">
            <n-tooltip trigger="hover"><template #trigger><n-button size="small" type="error" ghost disabled>选股(待接入)</n-button></template>该自建策略的选股逻辑待接入，先用「新建批次/扫描收件箱导入」加自选股</n-tooltip>
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
        <n-data-table size="small" max-height="380" :columns="displayColumns" :data="displayRows"
                      :pagination="activeIsCustom ? false : {pageSize: 50}"
                      :row-class-name="activeIsCustom ? bpRowClass : undefined" />
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

    <!-- 新建策略弹窗 -->
    <n-modal v-model:show="showNewStrategy" preset="card" title="新建策略" style="width: 420px;">
      <n-space vertical>
        <n-input v-model:value="newStrategyName" placeholder="策略名称，如：我的超短1号" @keyup.enter="createStrategy" />
        <n-space justify="end">
          <n-button size="small" @click="showNewStrategy = false">取消</n-button>
          <n-button size="small" type="primary" @click="createStrategy">创建</n-button>
        </n-space>
        <n-text depth="3" style="font-size:12px">创建后即可用「新建批次/扫描收件箱导入」加自选股；选股按钮待接入策略逻辑后启用。</n-text>
      </n-space>
    </n-modal>
    <!-- 重命名策略弹窗 -->
    <n-modal v-model:show="showRenameStrategy" preset="card" title="重命名策略" style="width: 420px;">
      <n-space vertical>
        <n-input v-model:value="renameStrategyName" placeholder="新名称" @keyup.enter="renameStrategy" />
        <n-space justify="end">
          <n-button size="small" @click="showRenameStrategy = false">取消</n-button>
          <n-button size="small" type="primary" @click="renameStrategy">保存</n-button>
        </n-space>
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
  RenameTrackStrategy,
  RunOvernightScan, RunOvernightAppendCloseSheet, RunOvernightScanJSON
} from '../../wailsjs/go/main/App'
import {EventsOn, EventsOff} from '../../wailsjs/runtime'

const strategies = ref([])
const activeStrategy = ref('')
const builtinStrategies = computed(() => strategies.value.filter(s => s.builtin))
const customStrategies = computed(() => strategies.value.filter(s => !s.builtin))
const activeIsCustom = computed(() => {
  const s = strategies.value.find(x => String(x.id) === activeStrategy.value)
  return !!s && !s.builtin
})
const activeStrategyName = computed(() => {
  const s = strategies.value.find(x => String(x.id) === activeStrategy.value)
  return s ? s.name : ''
})
const showNewStrategy = ref(false)
const newStrategyName = ref('')
const showRenameStrategy = ref(false)
const renameStrategyName = ref('')
const customDropdownOptions = computed(() => {
  const opts = [{ label: '＋ 新建策略', key: '__new' }]
  for (const s of customStrategies.value) {
    opts.push({ label: s.name, key: String(s.id) })
  }
  if (activeIsCustom.value) {
    opts.push({ label: '✎ 重命名当前策略', key: '__rename' })
    opts.push({ label: '🗑 删除当前策略', key: '__delete', props: { style: 'color:#e88080' } })
  }
  return opts
})

function onCustomMenu(key) {
  if (key === '__new') {
    newStrategyName.value = ''
    showNewStrategy.value = true
  } else if (key === '__rename') {
    renameStrategyName.value = activeStrategyName.value
    showRenameStrategy.value = true
  } else if (key === '__delete') {
    const s = strategies.value.find(x => String(x.id) === activeStrategy.value)
    if (!s || s.builtin) return
    if (!confirm('确定删除策略「' + s.name + '」？其全部批次与持仓记录将一并删除，不可恢复。')) return
    DelTrackStrategy(s.id).then(() => {
      loadStrategies()
      activeStrategy.value = ''
      setTimeout(() => { if (!activeStrategy.value && strategies.value.length) { activeStrategy.value = String(strategies.value[0].id); refresh() } }, 100)
    })
  } else {
    activeStrategy.value = key
    onSwitchStrategy()
  }
}

function createStrategy() {
  const name = (newStrategyName.value || '').trim()
  if (!name) return
  AddTrackStrategy(name).then((st) => {
    showNewStrategy.value = false
    if (st && st.id) {
      loadStrategies()
      activeStrategy.value = String(st.id)
    } else {
      window.$message ? window.$message.error('创建失败(可能重名)') : alert('创建失败(可能重名)')
    }
  })
}

function renameStrategy() {
  const name = (renameStrategyName.value || '').trim()
  if (!name || !activeStrategy.value) return
  RenameTrackStrategy(Number(activeStrategy.value), name).then((res) => {
    if (res === 'OK') {
      showRenameStrategy.value = false
      loadStrategies()
    } else {
      window.$message ? window.$message.error(res || '失败') : alert(res || '失败')
    }
  })
}
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

// B点式自建策略明细列（原版口径, v9b.3）: 单日涨幅+区间求和+每日最高行
const bpColumns = [
  mkCol('序号', 'idx', 50),
  mkCol('股票代码', 'stockCode', 95),
  mkCol('名称', 'stockName', 90),
  { title: '当日净涨跌幅', key: 'dayNet', width: 105, render: r => pctCellBp(r.dayNet) },
  mkCol('收盘价', 'closePrice', 75),
  { title: '收盘涨跌幅', key: 'dayChg', width: 100, render: r => pctCellBp(r.dayChg) },
  ...[1,2,3,4,5,6,7,8].map(n => ({
    title: '持仓第' + n + '日', key: 'hold' + n, width: 90, render: r => pctCellBp(r['hold' + n])
  })),
  { title: '3日区间', key: 'sum3', width: 90, render: r => pctCellBp(r.sum3) },
  { title: '7日区间', key: 'sum7', width: 90, render: r => pctCellBp(r.sum7) },
  {
    title: '操作', key: 'op', width: 70,
    render(row) {
      return h('a', { style: 'color:#e88080;cursor:pointer', onClick: () => delItem(row) }, '删除')
    }
  },
]

// 原版格式: 小数→百分比字串, 空为'-'; 红涨绿跌; 涨停(≥9.9%)加框标
function pctCellBp(v) {
  if (v === undefined || v === null) return h('span', {style: 'color:rgba(128,128,128,0.5)'}, '-')
  const pct = (Number(v) * 100).toFixed(2) + '%'
  const zt = Number(v) >= 0.099
  const dt = Number(v) <= -0.099
  const style = 'font-weight:600;color:' + (Number(v) > 0 ? '#e88080' : Number(v) < 0 ? '#63e2b7' : 'rgba(128,128,128,0.5)')
  const text = (Number(v) > 0 ? '+' : '') + pct + (zt ? ' ⚡' : dt ? ' ⚡' : '')
  return h('span', {style: style + (zt ? ';border:1px solid #e88080;border-radius:3px;padding:0 3px' : dt ? ';border:1px solid #63e2b7;border-radius:3px;padding:0 3px' : '')}, text)
}

// 展开数据: 每股票2行(数据行+每日最高行), B点原版样式; 口径=单日涨幅(相对前日收)
const bpRows = computed(() => {
  const out = []
  let idx = 0
  for (const it of items.value) {
    idx++
    // 数据行: holdN=单日口径(由累计值反推, 后端另给单日字段时直取)
    const row = {
      idx, id: it.id, stockCode: it.stockCode, stockName: it.stockName,
      dayNet: it.dayNetPct, closePrice: it.closePrice, dayChg: it.dayChgPct,
      sum3: it.sum3Pct, sum7: it.sum7Pct
    }
    let prev = null
    for (let n = 1; n <= 8; n++) {
      const cum = it['hold' + n + 'Pct']
      row['hold' + n] = singleDayFromCum(cum, prev)
      prev = cum
    }
    out.push(row)
    // 每日最高行
    const hi = { idx: '', stockCode: '', stockName: '', dayNet: null, closePrice: '', dayChg: '每日最高:' }
    for (let n = 1; n <= 8; n++) hi['hold' + n] = it['high' + n + 'Pct']
    hi.sum3 = null; hi.sum7 = null; hi._hi = true
    out.push(hi)
  }
  return out
})

// 每日最高行淡色样式
function bpRowClass(row) { return row._hi ? 'bp-hi-row' : '' }

// 由累计(相对买入基准)反推单日: dN=(1+cumN)/(1+cumN-1)-1
function singleDayFromCum(cum, prevCum) {
  if (cum === undefined || cum === null || cum === 0) return cum === 0 ? 0 : null
  const c = Number(cum) / 100
  const p = prevCum === null || prevCum === undefined || prevCum === 0 ? 0 : Number(prevCum) / 100
  return (1 + c) / (1 + p) - 1
}

// 当前展示用列: 自建策略用B点式, 内置用原有
const displayColumns = computed(() => activeIsCustom.value ? bpColumns : itemColumns)
const displayRows = computed(() => activeIsCustom.value ? bpRows.value : items.value)

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

<style scoped>
/* v9b.4: 标题栏美化 */
.track-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-bottom: 8px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}
.track-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
  background: linear-gradient(90deg, #63e2b7, #63a2e8);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  padding-left: 10px;
  border-left: 3px solid #63e2b7;
}
/* v9b.3: 每日最高行淡显 + 表格间距美化 */
:deep(.bp-hi-row td) {
  color: rgba(128, 128, 128, 0.55) !important;
  font-size: 11px !important;
  background: rgba(255, 255, 255, 0.02);
}
:deep(.n-data-table-td) {
  padding: 6px 8px !important;
}
:deep(.n-data-table-th) {
  padding: 8px !important;
  white-space: nowrap;
}
</style>
