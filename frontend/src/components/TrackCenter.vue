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
          <n-tab v-for="s in builtinStrategies" :key="s.id" :name="String(s.id)">{{ s.name }}<n-text depth="3" style="font-size:12px; margin-left:6px;">批次:{{ latestBatchDateMap[s.id] || '无' }}</n-text></n-tab>
          <n-tab v-if="activeIsCustom" :name="activeStrategy">{{ activeStrategyName }}<n-text depth="3" style="font-size:12px; margin-left:6px;">批次:{{ latestBatchDateMap[activeStrategy] || '无' }}</n-text></n-tab>
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
          <template v-if="isOvernightTab || isNiuZhiTab">
            <!-- v9c.3: 两tab共用同一个date-picker实例(一夜上实测正常, 避免牛指重复实例的未知问题) -->
            <n-date-picker v-model:value="overnightDateTs" type="date" size="small" style="width: 150px"
                           :is-date-disabled="disableFutureDates" placeholder="选股日期" />
            <n-select v-if="isNiuZhiTab" v-model:value="niuzhiMode" size="small" style="width: 100px"
                      :options="[{label:'严选',value:'严选'},{label:'标准',value:'标准'},{label:'全勤',value:'全勤'}]" />
            <n-button v-if="isOvernightTab" size="small" type="error" ghost :loading="overnightLoading" @click="runOvernight">一夜选股(14:50后)</n-button>
            <n-button v-if="isOvernightTab" size="small" type="warning" ghost :loading="overnightCloseLoading" @click="runOvernightClose">收盘记录</n-button>
            <n-button v-if="isNiuZhiTab" size="small" type="error" ghost :loading="niuzhiLoading" @click="runNiuZhi">牛指选股(收盘后)</n-button>
          </template>
          <template v-else-if="activeIsCustom">
            <n-tooltip trigger="hover"><template #trigger><n-button size="small" type="error" ghost disabled>选股(待接入)</n-button></template>该自建策略的选股逻辑待接入，先用「新建批次/扫描收件箱导入」加自选股</n-tooltip>
          </template>
          <n-button size="small" type="success" ghost :loading="importing" @click="importInbox">
            {{ importing ? '导入中...' : '扫描收件箱导入' }}
          </n-button>
          <n-button size="small" type="info" ghost @click="openInboxDir">打开收件箱文件夹</n-button>
          <template v-if="isOvernightTab || isNiuZhiTab">
            <n-date-picker v-model:value="backfillStart" type="date" size="small" style="width:130px" :clearable="false" placement="bottom-start" />
            <n-date-picker v-model:value="backfillEnd" type="date" size="small" style="width:130px" :clearable="false" placement="bottom-start" />
            <n-tooltip trigger="hover"><template #trigger><n-button size="small" type="info" ghost :loading="backfilling" @click="doBackfill">补齐历史</n-button></template>重演选定日期范围（跳过周末/已有批次自动跳过；每日需跑一次本地扫描，范围越长耗时越久）</n-tooltip>
            <n-button size="small" type="primary" ghost :loading="querying" @click="doQueryBatches">查询批次</n-button>
            <n-button size="small" type="error" ghost :disabled="!queryRows.length" @click="clearQuery">清空查询</n-button>
          </template>
          <n-button size="small" type="warning" ghost :loading="exporting" @click="exportCSV">导出 CSV</n-button>
          <n-button size="small" type="success" ghost :loading="exportingXlsx" @click="exportXlsxTop">导出 Excel</n-button>
          <n-tooltip trigger="hover"><template #trigger><n-button size="small" type="warning" ghost :loading="fixingNames" @click="fixNames">修正名称</n-button></template>用东财实时行情核对全部批次股票名称并修正（治改名/旧名单导致的错名，几千只约需几分钟）</n-tooltip>
          <n-button size="small" :loading="refreshing" @click="refresh">{{ refreshing ? '回填中…' : '刷新' }}</n-button>
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
        <n-alert :type="(overnightModalTitle.startsWith('牛指') ? niuzhiScan : overnightScan)?.weakDay ? 'warning' : 'info'" :bordered="false">
          {{ (overnightModalTitle.startsWith('牛指') ? niuzhiScan : overnightScan)?.note || '' }}
        </n-alert>
        <n-data-table v-if="overnightModalTitle.startsWith('牛指') && niuzhiScan && niuzhiScan.cands && niuzhiScan.cands.length"
                      size="small" max-height="360" :columns="niuzhiCandColumns"
                      :data="niuzhiScan.cands.filter(c => niuzhiMode === '全勤' || (niuzhiMode === '严选' ? c.score >= 3 : c.score >= 2)).map((c, i) => ({
                        idx: i + 1, code: c.code, name: c.name, price: c.price, chg: c.chg,
                        score: c.score, dd60: c.dd60, shrink: c.volShrink ? '是' : '否',
                        amtYi: c.amtYi, posFinal: c.posFinal + '%', topTag: c.topTag || ''
                      }))" />
        <n-empty v-else-if="overnightModalTitle.startsWith('牛指') && niuzhiScan && niuzhiScan.trading" description="无信号" />
        <n-data-table v-else-if="overnightScan && overnightScan.cands && overnightScan.cands.length"
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
import {onBeforeUnmount, onMounted, ref, h, computed, watch} from 'vue'
import {
  ListTrackStrategies, AddTrackStrategy, DelTrackStrategy, GetTrackBatches,
  GetRecentTrackItems, GetTrackStats, CreateTrackBatch, ImportTrackInboxFolder,
  OpenTrackInboxDir, ExportTrackCSV, ExportTrackExcel, DeleteTrackBatch, DeleteTrackItem, RefreshAllTrackNames,
  RenameTrackStrategy,
  RunOvernightScan, RunOvernightAppendCloseSheet, RunOvernightScanJSON,
  RunNiuZhiScanJSON, BackfillTrackRange
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
// [v9f.11] 各策略最新批次日期（页签旁显示），从批次列表取最大 tradeDate
const latestBatchDateMap = computed(() => {
  const m = {}
  for (const b of (batches.value || [])) {
    const d = String(b.tradeDate || '')
    if (d && (!m[b.strategyId] || d > m[b.strategyId])) m[b.strategyId] = d.slice(5)
  }
  return m
})
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
// [v9f.17.10] 选股日期也持久化（切模块回来不重置）
watch(overnightDateTs, (v) => { try { localStorage.setItem('trackOvernightDate', String(v)) } catch {} })
try { const _od = localStorage.getItem('trackOvernightDate'); if (_od) overnightDateTs.value = Number(_od) || overnightDateTs.value } catch {}
const isOvernightTab = computed(() => {
  const s = strategies.value.find(x => String(x.id) === activeStrategy.value)
  return !!s && /一夜/.test(s.name || '')
})
// v9c: 牛指T+1专属选股(日期+三模式)
const niuzhiLoading = ref(false)
const niuzhiDateTs = ref(Date.now()) // 牛指tab上次用的日期(v9c.4各记各的)
const niuzhiMode = ref('标准')
const niuzhiScan = ref(null)
const isNiuZhiTab = computed(() => {
  const s = strategies.value.find(x => String(x.id) === activeStrategy.value)
  return !!s && /牛指/.test(s.name || '')
})
// [v9f.17.16] 牛指模式切换：批次表与明细立即按新模式重载
watch(niuzhiMode, () => {
  if (!isNiuZhiTab.value || !activeStrategy.value) return
  checkedBatches.value = []
  const id = Number(activeStrategy.value)
  GetTrackBatches(id).then((res) => { batches.value = res || [] })
  window.go.main.App.GetRecentTrackItemsMode(id, niuzhiMode.value || '', 500).then((res) => { items.value = res || [] })
})

function runNiuZhi() {
  niuzhiDateTs.value = overnightDateTs.value // 记住牛指本次用的日期
  niuzhiLoading.value = true
  RunNiuZhiScanJSON(fmtDay(overnightDateTs.value), niuzhiMode.value).then((res) => {
    niuzhiLoading.value = false
    niuzhiScan.value = res || null
    overnightModalTitle.value = '牛指选股结果 (' + fmtDay(overnightDateTs.value) + ' · ' + niuzhiMode.value + ')'
    showOvernightModal.value = true
  }).catch((e) => {
    niuzhiLoading.value = false
    overnightModalTitle.value = '牛指选股结果'
    niuzhiScan.value = { trading: false, note: '失败: ' + e, cands: [] }
    showOvernightModal.value = true
  })
}

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

const niuzhiCandColumns = [
  { title: '#', key: 'idx', width: 40 },
  { title: '代码', key: 'code', width: 80 },
  { title: '名称', key: 'name', width: 100 },
  { title: '现价', key: 'price', width: 70 },
  { title: '涨幅%', key: 'chg', width: 70 },
  { title: '评分', key: 'score', width: 55 },
  { title: '60日回撤%', key: 'dd60', width: 85 },
  { title: '缩量', key: 'shrink', width: 60 },
  { title: '成交额亿', key: 'amtYi', width: 75 },
  { title: '建议仓', key: 'posFinal', width: 70 },
  { title: '标记', key: 'topTag', width: 60 }
]
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
const displayRows = computed(() => {
  if (activeIsCustom.value) return bpRows.value
  // [v9f.12] 查询结果增量叠加在默认明细之上，点清空恢复
  return queryRows.value.length ? items.value.concat(queryRows.value) : items.value
})

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
// [v9f.17.2] 停牌日显示"停"：suspendDays="2,5"=第2、5日停牌；suspendNext=次日停牌
const holdIndex = { nextOpenPct: 0, nextHighPct: 0, nextClosePct: 0, hold1Pct: 1, hold2Pct: 2, hold3Pct: 3, hold4Pct: 4, hold5Pct: 5, hold6Pct: 6, hold7Pct: 7, hold8Pct: 8 }
for (const k of ['nextOpenPct', 'nextHighPct', 'nextClosePct', 'hold1Pct', 'hold2Pct', 'hold3Pct', 'hold4Pct', 'hold5Pct', 'hold6Pct', 'hold7Pct', 'hold8Pct']) {
  const col = itemColumns.find(c => c.key === k)
  col.render = (row) => {
    const dayIdx = holdIndex[k]
    const sus = dayIdx === 0 ? (row.suspendNext && row[k] === 0) : ((row.suspendDays || '').split(',').includes(String(dayIdx)))
    if (sus) return h('span', {style: 'color:#f0a020;fontWeight:600'}, '停')
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
  // [v9f.17.7] 先存当前策略状态，再切 key，再加载新策略状态
  // [v9f.17.12] 全程 _restoring=true：恢复赋值不触发watch保存（根因：赋值触发watch→把A的rows存进B的key→互相污染，一夜切牛指批次日期都不变）
  _restoring = true
  saveQueryState()
  const q = loadQueryState()
  queryRows.value = q.rows || []
  queryBatchIds.value = q.batchIds || []
  if (q.start) backfillStart.value = q.start
  if (q.end) backfillEnd.value = q.end
  setTimeout(() => { _restoring = false }, 0)
  // v9c.4: 共用date-picker但各tab记住各自日期——切tab时把组件值切到该tab上次用的
  const s = strategies.value.find(x => String(x.id) === activeStrategy.value)
  if (s && /牛指/.test(s.name || '')) {
    overnightDateTs.value = niuzhiDateTs.value
  } else if (s && /一夜/.test(s.name || '')) {
    niuzhiDateTs.value = overnightDateTs.value
  }
  refresh()
}

// [v9f.17.16] 刷新重构：先立即加载当前策略数据（切策略秒出该策略自己的批次），
// 回填放后台跑，完成后自动重载一次。修复两个问题：
// ①切策略表格数据要等回填完才换 → 看到的是上一个策略的旧数据（"切策略批次一样"）
// ②回填慢/卡住时表格永远不更新、转圈无尽头
const refreshing = ref(false)
function refresh() {
  if (!activeStrategy.value || refreshing.value) return
  const id = Number(activeStrategy.value)
  // ① 立即加载（不等回填）
  const mode = niuzhiMode.value || ''
  const loadAll = () => {
    GetTrackBatches(id).then((res) => { batches.value = res || [] })
    window.go.main.App.GetRecentTrackItemsMode(id, isNiuZhiTab.value ? mode : '', 500).then((res) => { items.value = res || [] })
    GetTrackStats().then((res) => {
      stat.value = (res || []).find(s => String(s.strategyId) === activeStrategy.value) || {}
    })
  }
  loadAll()
  // ② 回填后台跑，完成后重载
  refreshing.value = true
  if (window.$message) window.$message.info('正在回填涨幅数据（后台进行，完成后自动刷新表格）', { duration: 5000 })
  window.go.main.App.FillAllTrackHoldings().then(() => {
    if (String(activeStrategy.value) !== String(id)) return // 已切走，丢弃过期结果
    loadAll()
    refreshing.value = false
    if (window.$message) window.$message.success('回填完成，表格已是最新', { duration: 5000 })
  }).catch((e) => {
    refreshing.value = false
    if (window.$message) window.$message.error('回填失败: ' + e)
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

const backfilling = ref(false)
// [v9f] 修正名称
const fixingNames = ref(false)
function fixNames() {
  fixingNames.value = true
  RefreshAllTrackNames().then((res) => {
    fixingNames.value = false
    if (window.$message) window.$message.success(res || '完成')
    refresh()
  }).catch(() => { fixingNames.value = false })
}
// [v9f.11] 补齐历史：用户可选日期范围（默认最近10个自然日）；范围越长耗时越久，点击后提示
// [v9f.13] 日期范围与查询状态持久化：切模块/重启后恢复上次状态
// [v9f.14] 查询状态按策略隔离：一夜/牛指各自的查询结果互不串
const LS_KEY = 'trackQueryState.v2'
function lsKey() {
  let k = LS_KEY + ':' + String(activeStrategy.value || '')
  if (isNiuZhiTab.value && niuzhiMode.value) k += ':' + niuzhiMode.value // [v9f.17.16] 牛指三模式查询互不串
  return k
}
function loadQueryState() {
  try { return JSON.parse(localStorage.getItem(lsKey())) || {} } catch { return {} }
}
function saveQueryState() {
  try {
    localStorage.setItem(lsKey(), JSON.stringify({
      start: backfillStart.value, end: backfillEnd.value,
      rows: queryRows.value, batchIds: queryBatchIds.value
    }))
    console.log('[trackQuery] 已保存 key=', lsKey(), 'rows=', queryRows.value.length)
  } catch (e) {
    console.error('[trackQuery] 保存失败(可能超出localStorage容量):', e)
  }
}
const _qs = loadQueryState()
console.log('[trackQuery] 恢复检查 key=', lsKey(), '存了:', JSON.stringify(Object.keys(_qs)), 'rows=', (_qs.rows||[]).length, 'start=', _qs.start, 'end=', _qs.end)
const backfillStart = ref(_qs.start || Date.now() - 10*86400000)
const backfillEnd = ref(_qs.end || Date.now())
// [v9f.17.10] 日期范围改动即存（不等查询）：用户选完日期切模块，回来必须还是选的日期
// [v9f.17.12] 防竞态：恢复赋值/切策略期间暂停watch保存（否则旧策略的rows会被写进新策略的key，互相污染）
let _restoring = false
watch([backfillStart, backfillEnd], () => {
  if (_restoring) return
  saveQueryState()
})
const queryRows = ref(_qs.rows || [])
const queryBatchIds = ref(_qs.batchIds || [])

async function doBackfill() {
  const st = (strategies.value || []).find(s => String(s.id) === String(activeStrategy.value))
  if (!st) return
  const fmt = (ts) => {
    const d = new Date(ts)
    return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0')
  }
  const start = fmt(backfillStart.value)
  const end = fmt(backfillEnd.value)
  if (start > end) { if (window.$message) window.$message.warning('开始日期不能晚于结束日期'); return }
  const days = Math.round((backfillEnd.value - backfillStart.value) / 86400000) + 1
  backfilling.value = true
  try {
    if (window.$message) window.$message.info(`开始补齐 ${start} ~ ${end}（约${days}天，每日需扫描一次，请耐心等待完成提示）`)
    const msg = await BackfillTrackRange(st.name, start, end)
    if (window.$message) window.$message.info(msg || '补齐完成')
    refresh()
  } catch(e) {
    if (window.$message) window.$message.error('补齐失败: ' + e)
  } finally { backfilling.value = false }
}
// [v9f.12] 日期范围查询批次：增量叠加显示，点「清空查询」恢复（多次查询结果保留）
const querying = ref(false)
async function doQueryBatches() {
  const fmt = (ts) => {
    const d = new Date(ts)
    return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0')
  }
  const s = fmt(backfillStart.value)
  const e = fmt(backfillEnd.value)
  if (s > e) { if (window.$message) window.$message.warning('开始日期不能晚于结束日期'); return }
  // [v9f.12.1] 不依赖页面已有 batches 状态，直接拉后端全量批次再过滤（防止页面未加载/切策略导致空判断）
  let all = []
  try {
    all = await window.go.main.App.GetTrackBatches(Number(activeStrategy.value)) || []
  } catch (err) {
    if (window.$dialog) window.$dialog.error({ title: '查询失败', content: '获取批次列表失败: ' + err })
    else if (window.$message) window.$message.error('获取批次列表失败: ' + err)
    return
  }
  // [v9f.17.16] 牛指三模式批次独立：查询只命中当前模式（mode 字段匹配；无 mode 的旧数据归入"标准"）
  const curMode = isNiuZhiTab.value ? (niuzhiMode.value || '标准') : ''
  const inRange = all.filter(b => {
    const d = String(b.tradeDate || '')
    if (d < s || d > e) return false
    if (isNiuZhiTab.value) return (b.mode || '标准') === curMode
    return true
  })
  if (!inRange.length) {
    if (window.$dialog) window.$dialog.warning({ title: '范围内无批次', content: `在 ${s} ~ ${e} 范围内没有找到任何批次（当前策略共 ${all.length} 个批次）。可点「补齐历史」按此范围重演建批次，完成后再查询。` })
    else if (window.$message) window.$message.warning(`范围内无批次（${s} ~ ${e}），共${all.length}个批次。可点「补齐历史」建批次`, { duration: 10000 })
    return
  }
  querying.value = true
  try {
    const have = new Set(queryRows.value.map(r => r.id))
    const got = []
    for (const b of inRange) {
      if (queryBatchIds.value.includes(b.id)) continue
      queryBatchIds.value.push(b.id)
      const rows = await window.go.main.App.GetBatchItems(b.id)
      for (const r of (rows || [])) {
        if (have.has(r.id)) continue
        have.add(r.id)
        got.push(Object.assign({}, r, { tradeDate: b.tradeDate }))
      }
    }
    queryRows.value = queryRows.value.concat(got)
    saveQueryState()
    if (window.$dialog) window.$dialog.success({ title: '查询完成', content: `查询到 ${inRange.length} 个批次，本次新增 ${got.length} 条明细。多次查询增量叠加，点「清空查询」恢复。` })
    else if (window.$message) window.$message.success(`查询到 ${inRange.length} 个批次，新增 ${got.length} 条明细`, { duration: 8000 })
  } catch(err) {
    if (window.$message) window.$message.error('查询失败: ' + err)
  } finally { querying.value = false }
}
function clearQuery() {
  queryRows.value = []
  queryBatchIds.value = []
  saveQueryState()
  if (window.$message) window.$message.info('已清空查询结果，恢复默认列表')
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
  // [v9f.16] 新语义：导出=打开该策略唯一的回测表文件（数据目录/策略名-回测表.xlsx），打开前自动同步全部批次
  const sid = Number(activeStrategy.value)
  exporting.value = true
  window.go.main.App.OpenLatestTrackSheet(sid).then((res) => {
    exporting.value = false
    const msg = res || ''
    if (msg.includes('.xlsx')) {
      window.$message ? window.$message.success(msg, { duration: 8000 }) : alert(msg)
    } else {
      window.$message ? window.$message.warning(msg, { duration: 10000 }) : alert(msg)
    }
  }).catch((e) => {
    exporting.value = false
    const msg = '导出失败: ' + (e && e.message ? e.message : e)
    window.$message ? window.$message.error(msg, { duration: 8000 }) : alert(msg)
  })
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
  // [v9f.17.8b] 挂载后二次恢复查询状态（防 setup 阶段时序问题）
  setTimeout(() => {
    const q2 = loadQueryState()
    if ((q2.rows || []).length && !queryRows.value.length) {
      queryRows.value = q2.rows
      queryBatchIds.value = q2.batchIds || []
      console.log('[trackQuery] 挂载二次恢复 rows=', q2.rows.length)
    }
  }, 300)
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
