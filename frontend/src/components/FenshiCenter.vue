<template>
  <div class="fenshi-center">
    <div class="layout">
      <!-- 左侧：自选股 -->
      <div class="left-pane">
        <n-card size="small" title="自选股" class="mb12">
          <div class="add-row">
            <n-input v-model:value="manualCode" placeholder="输入代码如 600519" size="small" @keyup.enter="addStock" clearable />
            <n-button type="primary" size="small" :loading="adding" @click="addStock">添加</n-button>
          </div>
          <div v-if="addMsg" class="add-msg" :class="addMsgOk ? 'ok' : 'err'">{{ addMsg }}</div>
          <n-data-table :columns="watchColumns" :data="watchlist" :max-height="520" size="small" :row-props="rowProps" striped />
        </n-card>
      </div>

      <!-- 右侧：大分时图 -->
      <div class="right-pane">
        <n-card size="small" v-if="result" class="mb12 chart-card">
          <!-- 大标题：代码+名称（用户指定显眼格式） -->
          <template #header>
            <div class="chart-title">
              <span class="title-name">{{ result.name }}</span>
              <span class="title-code">（{{ result.code }}）</span>
            </div>
          </template>
          <div class="quote-line">
            <span class="price" :class="result.changePct >= 0 ? 'up' : 'down'">{{ result.price.toFixed(2) }}</span>
            <span :class="result.changePct >= 0 ? 'up' : 'down'"> {{ result.changePct.toFixed(2) }}%</span>
            <span class="dim">高开 {{ result.gapPct.toFixed(2) }}% · 量比 {{ result.volumeRate.toFixed(2) }} · 均价 {{ result.avgPrice.toFixed(2) }}（{{ result.aboveVwapP >= 0 ? '+' : '' }}{{ result.aboveVwapP.toFixed(2) }}%）· 15分钟动量 {{ result.mom15.toFixed(2) }}%</span>
          </div>
          <div class="chart-wrap">
            <svg class="trend-svg" viewBox="0 0 860 400" preserveAspectRatio="none">
              <!-- 横向网格线 + 纵轴价格/涨跌幅刻度 -->
              <g v-for="(g, gi) in gridLines" :key="'g' + gi">
                <line :x1="52" :x2="856" :y1="g.y" :y2="g.y" class="grid-line" />
                <text :x="46" :y="g.y + 4" class="axis-text" text-anchor="end">{{ g.priceLabel }}</text>
                <text :x="46" :y="g.y + 16" class="axis-text" :class="g.pctClass" text-anchor="end">{{ g.pctLabel }}</text>
              </g>
              <!-- 纵轴边界线 -->
              <line :x1="52" :x2="52" :y1="8" :y2="352" class="axis-line" />
              <line :x1="856" :x2="856" :y1="8" :y2="352" class="axis-line" />
              <!-- 横轴刻度线 + 时间标签 -->
              <g v-for="(t, ti) in xTicks" :key="'t' + ti">
                <line :x1="t.x" :x2="t.x" :y1="8" :y2="352" class="grid-line" />
                <text :x="t.x" :y="368" class="axis-text" text-anchor="middle">{{ t.label }}</text>
              </g>
              <!-- 昨收基准虚线 -->
              <line v-if="prevCloseY >= 8 && prevCloseY <= 352" :x1="52" :x2="856" :y1="prevCloseY" :y2="prevCloseY" class="preclose-line" />
              <!-- 均价线 + 价格线 -->
              <polyline :points="vwapPath" fill="none" stroke="#e6b800" stroke-width="1.3" />
              <polyline :points="pricePath" fill="none" stroke="#4aa3ff" stroke-width="1.6" />
              <!-- 图例 -->
              <text x="60" y="20" class="legend-text" fill="#4aa3ff">— 价格</text>
              <text x="120" y="20" class="legend-text" fill="#e6b800">— 均价</text>
              <!-- 横轴底部边界 -->
              <line :x1="52" :x2="856" :y1="352" :y2="352" class="axis-line" />
            </svg>
          </div>
          <div class="advice-box">{{ result.advice }}</div>
          <n-space vertical size="small">
            <n-tag v-for="s in result.signals" :key="s.name" :type="s.type === 'buy' ? 'success' : (s.type === 'warn' ? 'error' : 'info')" size="small" style="white-space: normal; height: auto; padding: 4px 8px;">
              {{ s.name }}：{{ s.desc }}（{{ s.prob }}）
            </n-tag>
          </n-space>
        </n-card>
        <n-card size="small" v-else class="mb12" title="分时分析">
          <n-alert v-if="analyzeError" type="error" :title="'分析失败'" style="margin-bottom: 8px">{{ analyzeError }}</n-alert>
          <n-empty description="点击左侧自选股，或输入代码添加后分析" />
        </n-card>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, h } from "vue";
import { NCard, NButton, NDataTable, NTag, NInput, NSpace, NEmpty, NAlert } from "naive-ui";
import { FenshiWatchlist, FenshiWatchlistAdd, FenshiWatchlistRemove, FenshiAnalyzeStock } from "../../wailsjs/go/main/App";

const watchlist = ref([]);
const result = ref(null);
const manualCode = ref("");
const adding = ref(false);
const analyzing = ref(false);
const analyzeError = ref("");

const watchColumns = [
  // v9f.19：代码列去 sh/sz/bj 前缀显示（存储不动，仅展示层处理）
  {
    title: "代码", key: "code", width: 76,
    render(row) {
      return h("span", null, stripPrefix(row.code));
    },
  },
  { title: "名称", key: "name", width: 92 },
  {
    title: "",
    key: "del",
    width: 36,
    render(row) {
      return h(
        NButton,
        { size: "tiny", quaternary: true, type: "error", onClick: (e) => { e.stopPropagation(); removeStock(row.code); } },
        { default: () => "删" }
      );
    },
  },
];

// v9f.19：剥 sh/sz/bj 前缀（展示用）
function stripPrefix(code) {
  if (!code) return "";
  return String(code).replace(/^(sh|sz|bj)/i, "");
}

function rowProps(row) {
  return {
    style: "cursor:pointer",
    onClick: () => analyze(row.code),
  };
}

async function loadWatchlist() {
  try {
    watchlist.value = await FenshiWatchlist();
  } catch (e) {
    watchlist.value = [];
  }
}

const addMsg = ref("");
const addMsgOk = ref(false);

async function addStock() {
  const code = (manualCode.value || "").trim();
  if (!code) return;
  adding.value = true;
  addMsg.value = "";
  try {
    const msg = await FenshiWatchlistAdd(code);
    // v9f.18.1：无论成败都展示后端返回原因，不再静默；
    // "已经关注了"也算成功（照常分析），只有失败/错误/VIP限制才算失败
    addMsg.value = msg || "（后端无返回）";
    const failed = !msg || msg.indexOf("失败") >= 0 || msg.indexOf("错误") >= 0 || msg.indexOf("VIP") >= 0;
    addMsgOk.value = !failed;
    if (!failed) {
      manualCode.value = "";
      await loadWatchlist();
      await analyze(code);
    }
  } catch (e) {
    addMsg.value = "添加调用失败: " + (e && e.message ? e.message : e);
    addMsgOk.value = false;
  } finally {
    adding.value = false;
  }
}

async function removeStock(code) {
  await FenshiWatchlistRemove(code);
  await loadWatchlist();
}

onMounted(loadWatchlist);

async function analyze(code) {
  analyzing.value = true;
  analyzeError.value = "";
  try {
    const r = await FenshiAnalyzeStock(code);
    if (r) {
      result.value = r.errMsg ? null : r;
      if (r.errMsg) analyzeError.value = r.errMsg;
    } else {
      result.value = null;
      analyzeError.value = "未获取到分析结果（后端返回空）";
    }
  } catch (e) {
    result.value = null;
    analyzeError.value = "分析调用失败: " + (e && e.message ? e.message : e);
  } finally {
    analyzing.value = false;
  }
}

// ==================== 图表几何（v9f.19 通达信式坐标轴） ====================
// 绘图区：x 52~856（左侧留给纵轴双行标签），y 8~352（底部留给时间刻度）
const CH = { x0: 52, x1: 856, y0: 8, y1: 352 };

const pricePath = computed(() => ptsPath(result.value ? result.value.trend : [], "now"));
const vwapPath = computed(() => ptsPath(result.value ? result.value.trend : [], "avg"));

// 统一取价格范围：价格线+均价线+昨收 一起算，保证三者都在图内
function valueRange(trend) {
  let min = Infinity, max = -Infinity;
  const consider = (v) => { if (v > 0) { if (v > max) max = v; if (v < min) min = v; } };
  for (const t of trend) { consider(t.now); consider(t.avg); }
  const pc = result.value ? result.value.prevClose : 0;
  if (pc > 0) { consider(pc); }
  if (!isFinite(min)) return { min: 0, max: 1 };
  // 上下各留 5% 余量，避免线贴边
  const pad = (max - min) * 0.05 || max * 0.01 || 0.01;
  return { min: min - pad, max: max + pad };
}

function xOf(i, n) {
  return CH.x0 + (i / (n - 1)) * (CH.x1 - CH.x0);
}
function yOf(v, min, max) {
  return CH.y1 - ((v - min) / (max - min || 1)) * (CH.y1 - CH.y0);
}

function ptsPath(trend, col) {
  if (!trend || trend.length < 2) return "";
  const { min, max } = valueRange(trend);
  const n = trend.length;
  return trend.map((t, i) => xOf(i, n).toFixed(1) + "," + yOf(t[col], min, max).toFixed(1)).join(" ");
}

// 纵轴：4 条网格线（不含上下边界），通达信格式 = 价格 + 对应涨跌幅
const gridLines = computed(() => {
  const r = result.value;
  if (!r || !r.trend || r.trend.length < 2) return [];
  const { min, max } = valueRange(r.trend);
  const lines = [];
  const N = 4;
  for (let k = 1; k <= N; k++) {
    const v = min + ((max - min) * k) / (N + 1);
    const y = yOf(v, min, max);
    const pct = r.prevClose > 0 ? ((v - r.prevClose) / r.prevClose) * 100 : 0;
    lines.push({
      y,
      priceLabel: v.toFixed(2),
      pctLabel: (pct >= 0 ? "+" : "") + pct.toFixed(2) + "%",
      pctClass: pct > 0.001 ? "up" : (pct < -0.001 ? "down" : ""),
    });
  }
  return lines;
});

// 昨收基准虚线 y 坐标
const prevCloseY = computed(() => {
  const r = result.value;
  if (!r || !r.trend || r.trend.length < 2 || !r.prevClose) return -999;
  const { min, max } = valueRange(r.trend);
  return yOf(r.prevClose, min, max);
});

// 横轴：9:30~11:30/13:00~15:00 固定 5 个时间刻度（A股交易日 4 小时 240 分钟）
const xTicks = computed(() => {
  const r = result.value;
  if (!r || !r.trend || r.trend.length < 2) return [];
  // 按数据点比例定位：A 股 09:30 起 241 个分钟点，上午 121 点（09:30~11:30）
  const n = r.trend.length;
  const marks = [
    { idx: 0, label: "9:30" },
    { idx: Math.round(n * 0.25), label: "10:30" },
    { idx: Math.round(n * 0.5) - 1, label: "11:30/13:00" },
    { idx: Math.round(n * 0.75), label: "14:00" },
    { idx: n - 1, label: "15:00" },
  ];
  return marks.map((m) => ({ x: xOf(Math.min(m.idx, n - 1), n), label: m.label }));
});
</script>

<style scoped>
.fenshi-center { padding: 12px; }
.mb12 { margin-bottom: 12px; }
.layout { display: grid; grid-template-columns: 300px 1fr; gap: 12px; align-items: start; }
.left-pane { min-width: 0; }
.right-pane { min-width: 0; }
.add-row { display: flex; gap: 8px; margin-bottom: 8px; }
.add-msg { font-size: 12px; margin-bottom: 6px; }
.add-msg.ok { color: #22a45d; }
.add-msg.err { color: #e04b4b; }
/* v9f.19：大标题——名称+代码，显眼突出 */
.chart-title { display: flex; align-items: baseline; gap: 2px; }
.title-name { font-size: 22px; font-weight: 700; color: #f5c542; letter-spacing: 1px; }
.title-code { font-size: 16px; font-weight: 600; color: #7eb8ff; }
.quote-line { font-size: 15px; margin-bottom: 6px; }
.quote-line .price { font-size: 24px; font-weight: bold; margin-right: 6px; }
.up { color: #e04b4b; }
.down { color: #22a45d; }
.dim { color: #888; font-size: 12px; margin-left: 8px; }
/* v9f.19：图放大占满右侧，保持绘图区比例 */
.chart-wrap { width: 100%; }
.trend-svg { width: 100%; height: auto; aspect-ratio: 860 / 400; background: #111; border-radius: 4px; display: block; }
.grid-line { stroke: #2a2a2a; stroke-width: 1; }
.axis-line { stroke: #555; stroke-width: 1; }
.axis-text { fill: #999; font-size: 11px; font-family: monospace; }
.axis-text.up { fill: #e04b4b; }
.axis-text.down { fill: #22a45d; }
.preclose-line { stroke: #666; stroke-width: 1; stroke-dasharray: 4 3; }
.legend-text { font-size: 12px; }
.advice-box { background: #1f2937; color: #d1e7ff; padding: 8px 10px; border-radius: 4px; margin: 8px 0; font-size: 13px; }
</style>
