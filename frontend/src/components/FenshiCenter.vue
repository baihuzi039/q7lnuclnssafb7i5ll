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
          <!-- v9f.20.3：三栏 grid，物理隔离互不重叠：时钟左 | 名称中 | 行情右 -->
          <div class="top-row">
            <div class="big-clock">
              <span class="clock-time">{{ clockStr }}</span>
              <span class="clock-date">{{ clockDate }}</span>
            </div>
            <div class="chart-title">
              <span class="title-name">{{ result.name }}</span>
              <span class="title-code">（{{ stripPrefix(result.code) }}）</span>
            </div>
            <div class="quote-line">
              <span class="price" :class="result.changePct >= 0 ? 'up' : 'down'">{{ result.price.toFixed(2) }}</span>
              <span :class="result.changePct >= 0 ? 'up' : 'down'"> {{ result.changePct.toFixed(2) }}%</span>
              <span class="dim">高开 {{ result.gapPct.toFixed(2) }}% · 量比 {{ result.volumeRate.toFixed(2) }} · 均价 {{ result.avgPrice.toFixed(2) }}（{{ result.aboveVwapP >= 0 ? '+' : '' }}{{ result.aboveVwapP.toFixed(2) }}%）· 15分钟动量 {{ result.mom15.toFixed(2) }}%</span>
            </div>
          </div>

          <div
            class="chart-wrap"
            @mousemove="onChartMove"
            @mouseleave="onChartLeave"
            @click="onChartClick"
            @dblclick="onChartDblClick"
          >
            <svg class="trend-svg" viewBox="0 0 860 400" preserveAspectRatio="none">
              <!-- 横向网格 + 纵轴刻度（黑体） -->
              <g v-for="(g, gi) in gridLines" :key="'g' + gi">
                <line :x1="CH.x0" :x2="CH.x1" :y1="g.y" :y2="g.y" class="grid-line" />
                <text :x="CH.x0 - 8" :y="g.y + 4" class="axis-text hei" text-anchor="end">{{ g.priceLabel }}</text>
                <text :x="CH.x0 - 8" :y="g.y + 18" class="axis-text hei" :class="g.pctClass" text-anchor="end">{{ g.pctLabel }}</text>
              </g>
              <!-- 轴边界 -->
              <line :x1="CH.x0" :x2="CH.x0" :y1="CH.y0" :y2="CH.y1" class="axis-line" />
              <line :x1="CH.x1" :x2="CH.x1" :y1="CH.y0" :y2="CH.y1" class="axis-line" />
              <line :x1="CH.x0" :x2="CH.x1" :y1="CH.y1" :y2="CH.y1" class="axis-line" />
              <!-- 横轴：每半小时刻度（黑体） -->
              <g v-for="(t, ti) in xTicks" :key="'t' + ti">
                <line :x1="t.x" :x2="t.x" :y1="CH.y0" :y2="CH.y1" class="grid-line" />
                <text :x="t.x" :y="CH.y1 + 18" class="axis-text hei" text-anchor="middle">{{ t.label }}</text>
              </g>
              <!-- 昨收基准虚线 -->
              <line v-if="prevCloseY >= CH.y0 && prevCloseY <= CH.y1" :x1="CH.x0" :x2="CH.x1" :y1="prevCloseY" :y2="prevCloseY" class="preclose-line" />
              <!-- 均价线 + 价格线 -->
              <polyline :points="vwapPath" fill="none" stroke="#e6b800" stroke-width="1.3" />
              <polyline :points="pricePath" fill="none" stroke="#4aa3ff" stroke-width="1.6" />
              <!-- 最新价小红点 -->
              <circle v-if="lastDot" :cx="lastDot.x" :cy="lastDot.y" r="3.5" fill="#ff4d4f" class="last-dot" />
              <!-- 十字线（悬停或固定时显示） -->
              <g v-if="cross">
                <line :x1="cross.x" :x2="cross.x" :y1="CH.y0" :y2="CH.y1" class="cross-line" />
                <line :x1="CH.x0" :x2="CH.x1" :y1="cross.y" :y2="cross.y" class="cross-line" />
                <circle :cx="cross.x" :cy="cross.y" r="3" fill="#fff" />
                <!-- 数据提示框 -->
                <g v-if="crossInfo">
                  <rect :x="tipX" :y="CH.y0 + 4" width="150" height="72" rx="4" class="tip-rect" />
                  <text :x="tipX + 10" :y="CH.y0 + 24" class="tip-text strong">{{ crossInfo.time }}</text>
                  <text :x="tipX + 10" :y="CH.y0 + 42" class="tip-text">{{ crossInfo.price }}</text>
                  <text :x="tipX + 10" :y="CH.y0 + 58" class="tip-text">{{ crossInfo.avg }}</text>
                  <text :x="tipX + 10" :y="CH.y0 + 72" class="tip-text" :fill="crossInfo.pctColor">{{ crossInfo.pct }}</text>
                </g>
              </g>
              <!-- 图例 -->
              <text :x="CH.x0 + 8" y="20" class="legend-text hei" fill="#4aa3ff">— 价格</text>
              <text :x="CH.x0 + 68" y="20" class="legend-text hei" fill="#e6b800">— 均价</text>
              <!-- 模式提示（固定时显示） -->
              <text v-if="pinned" :x="CH.x1 - 8" y="20" class="legend-text hei" fill="#ff9800" text-anchor="end">已固定 · 双击解除</text>
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
import { ref, computed, onMounted, onUnmounted, h } from "vue";
import { NCard, NButton, NDataTable, NTag, NInput, NSpace, NEmpty, NAlert } from "naive-ui";
import { FenshiWatchlist, FenshiWatchlistAdd, FenshiWatchlistRemove, FenshiAnalyzeStock, FenshiNowTime } from "../../wailsjs/go/main/App";

const watchlist = ref([]);
const result = ref(null);
const manualCode = ref("");
const adding = ref(false);
const analyzing = ref(false);
const analyzeError = ref("");

const watchColumns = [
  { title: "代码", key: "code", width: 76, render(row) { return h("span", null, stripPrefix(row.code)); } },
  { title: "名称", key: "name", width: 92 },
  {
    title: "",
    key: "del",
    width: 36,
    render(row) {
      return h(NButton, { size: "tiny", quaternary: true, type: "error", onClick: (e) => { e.stopPropagation(); removeStock(row.code); } }, { default: () => "删" });
    },
  },
];

// 剥 sh/sz/bj 前缀（展示用；列表与标题统一走这里）
function stripPrefix(code) {
  if (!code) return "";
  return String(code).replace(/^(sh|sz|bj)/i, "");
}

function rowProps(row) {
  return { style: "cursor:pointer", onClick: () => analyze(row.code) };
}

async function loadWatchlist() {
  try { watchlist.value = await FenshiWatchlist(); } catch (e) { watchlist.value = []; }
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

onMounted(() => {
  loadWatchlist();
  startClock();
});
onUnmounted(stopClock);

async function analyze(code) {
  analyzing.value = true;
  analyzeError.value = "";
  cross.value = null;       // 换股时清掉旧十字线
  pinned.value = false;     // 并恢复跟随模式
  await syncClock();        // v9f.20：换股时重新校时，保证时间连贯准确
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

// ==================== 大时钟（v9f.20） ====================
// 准确性设计：以后端返回的本机系统时间为基准计算 offset；
// 每次换股时重新校时；每 250ms 用 performance.now() 平滑推进，避免整秒跳变
const clockStr = ref("--:--:--");
const clockDate = ref("");
let clockOffset = 0;   // backendNow - localNow，ms
let clockTimer = null;
let syncing = false;

async function syncClock() {
  if (syncing) return;
  syncing = true;
  try {
    const t0 = performance.now();
    const backendMs = await FenshiNowTime();
    const rtt = performance.now() - t0; // 本地调用开销（wails 桥接，<5ms）
    clockOffset = backendMs + rtt / 2 - Date.now();
  } catch (e) {
    // 校时失败退化为本地时钟（Windows 默认 NTP 同步，误差秒级内）
    clockOffset = 0;
  } finally {
    syncing = false;
  }
}

function fmtClock(ms) {
  const d = new Date(ms);
  const p = (n) => String(n).padStart(2, "0");
  const wd = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"][d.getDay()];
  clockDate.value = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${wd}`;
  clockStr.value = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function startClock() {
  syncClock();
  fmtClock(Date.now() + clockOffset);
  clockTimer = setInterval(() => fmtClock(Date.now() + clockOffset), 250);
  // 每 5 分钟重新校时一次，防漂移
  setInterval(syncClock, 5 * 60 * 1000);
}
function stopClock() {
  if (clockTimer) clearInterval(clockTimer);
  clockTimer = null;
}

// ==================== 图表几何（v9f.20） ====================
const CH = { x0: 52, x1: 856, y0: 8, y1: 352 };

const pricePath = computed(() => ptsPath("now"));
const vwapPath = computed(() => ptsPath("avg"));

function valueRange(trend) {
  let min = Infinity, max = -Infinity;
  const consider = (v) => { if (v > 0) { if (v > max) max = v; if (v < min) min = v; } };
  for (const t of trend) { consider(t.now); consider(t.avg); }
  const pc = result.value ? result.value.prevClose : 0;
  if (pc > 0) consider(pc);
  if (!isFinite(min)) return { min: 0, max: 1 };
  // v9f.20：以昨收为轴的对称量程（通达信风格），且保底至少 ±1% 摆动空间，避免波动小时图形糊满整屏
  if (pc > 0) {
    let half = Math.max(Math.abs(max - pc), Math.abs(pc - min), pc * 0.012);
    return { min: pc - half * 1.1, max: pc + half * 1.1 };
  }
  const pad = (max - min) * 0.05 || max * 0.01 || 0.01;
  return { min: min - pad, max: max + pad };
}

function yOf(v, min, max) { return CH.y1 - ((v - min) / (max - min || 1)) * (CH.y1 - CH.y0); }

// 由 "09:30" 格式时间算分钟偏移（跳过午休：下午 = 120 + (分钟 - 13:00)）
function minuteOffset(timeStr) {
  const m = String(timeStr || "").match(/^(\d{1,2}):(\d{2})/);
  if (!m) return 0;
  const mins = parseInt(m[1]) * 60 + parseInt(m[2]);
  const amEnd = 11 * 60 + 30, pmStart = 13 * 60;
  if (mins <= 11 * 60 + 30) return mins - (9 * 60 + 30); // 上午：9:30起
  return 120 + Math.max(0, mins - pmStart);               // 下午：120起续算
}

// v9f.20.4：曲线按分钟偏移定位（与固定横轴刻度对齐），盘中未走完的右侧留白
function ptsPath(col) {
  const r = result.value;
  const trend = r ? r.trend : [];
  if (!trend || trend.length < 2) return "";
  const { min, max } = valueRange(trend);
  return trend.map((t) => {
    const off = Math.max(0, Math.min(240, minuteOffset(t.time)));
    const x = CH.x0 + (off / 240) * (CH.x1 - CH.x0);
    return x.toFixed(1) + "," + yOf(t[col], min, max).toFixed(1);
  }).join(" ");
}

// 纵轴 4 条网格线：价格+涨跌幅
const gridLines = computed(() => {
  const r = result.value;
  if (!r || !r.trend || r.trend.length < 2) return [];
  const { min, max } = valueRange(r.trend);
  const lines = [];
  const N = 8; // v9f.20.2：纵轴格子细分（8格，更多刻度）
  for (let k = 1; k <= N; k++) {
    const v = min + ((max - min) * k) / (N + 1);
    const y = yOf(v, min, max);
    const pct = r.prevClose > 0 ? ((v - r.prevClose) / r.prevClose) * 100 : 0;
    lines.push({ y, priceLabel: v.toFixed(2), pctLabel: (pct >= 0 ? "+" : "") + pct.toFixed(2) + "%", pctClass: pct > 0.001 ? "up" : (pct < -0.001 ? "down" : "") });
  }
  return lines;
});

const prevCloseY = computed(() => {
  const r = result.value;
  if (!r || !r.trend || r.trend.length < 2 || !r.prevClose) return -999;
  const { min, max } = valueRange(r.trend);
  return yOf(r.prevClose, min, max);
});

// 横轴：每半小时一个刻度（A股 4 小时：9:30-11:30, 13:00-15:00）
const xTicks = computed(() => {
  const r = result.value;
  if (!r || !r.trend || r.trend.length < 2) return [];
  // v9f.20.4 修复：横轴刻度固定按全天 241 点定位（9:30~11:30 + 13:00~15:00），
  // 不随盘中已返回的数据长度缩放，位置固定不动；
  // 曲线绘制同样按“真实分钟偏移”定位（见 ptsPath），未走完的右侧留白
  const FULL = 241; // 全天 9:30 起 241 个分钟点
  const label = (m) => {
    if (m === 120) return "11:30/13:00";
    const total = m <= 120 ? 9 * 60 + 30 + m : 13 * 60 + (m - 120);
    const h = Math.floor(total / 60) % 24, mm = total % 60;
    return `${h}:${String(mm).padStart(2, "0")}`;
  };
  const mins = [0, 30, 60, 90, 120, 150, 180, 210, 240];
  return mins.map((m) => {
    const x = CH.x0 + (m / 240) * (CH.x1 - CH.x0);
    return { x, label: label(m) };
  });
});

// 最新价小红点
const lastDot = computed(() => {
  const r = result.value;
  const trend = r ? r.trend : [];
  if (!trend || trend.length < 2) return null;
  const { min, max } = valueRange(trend);
  const off = Math.max(0, Math.min(240, minuteOffset(trend[trend.length - 1].time)));
  return { x: CH.x0 + (off / 240) * (CH.x1 - CH.x0), y: yOf(trend[trend.length - 1].now, min, max) };
});

// ==================== 十字线交互（v9f.20 通达信式） ====================
// 默认跟随模式：移动鼠标即跟随；单击=固定在那一刻（数据不再随移动变）；双击=解除固定恢复跟随
const cross = ref(null);        // {x, y}
const pinned = ref(false);      // true=单击固定中
const crossInfo = ref(null);    // {time, price, avg, pct, pctColor}
let hoverIdx = -1;
let pinIdx = -1;

function chartIndexFromEvent(e) {
  const r = result.value;
  if (!r || !r.trend || r.trend.length < 2) return -1;
  const svg = e.currentTarget.querySelector("svg");
  if (!svg) return -1;
  const rect = svg.getBoundingClientRect();
  // viewBox 860 宽，preserveAspectRatio=none → 线性映射
  const vx = ((e.clientX - rect.left) / rect.width) * 860;
  if (vx < CH.x0 || vx > CH.x1) return -1;
  // v9f.20.4：由鼠标 x 反算分钟偏移，再找最接近的数据点（与曲线定位一致）
  const off = Math.round(((vx - CH.x0) / (CH.x1 - CH.x0)) * 240);
  const n = r.trend.length;
  let best = 0, bestDiff = Infinity;
  for (let i = 0; i < n; i++) {
    const d = Math.abs(minuteOffset(r.trend[i].time) - off);
    if (d < bestDiff) { bestDiff = d; best = i; }
  }
  return best;
}

function buildCross(idx) {
  const r = result.value;
  if (!r || idx < 0) return;
  const trend = r.trend;
  const { min, max } = valueRange(trend);
  const t = trend[idx];
  const off = Math.max(0, Math.min(240, minuteOffset(t.time)));
  const x = CH.x0 + (off / 240) * (CH.x1 - CH.x0);
  // 十字线横线放在该点价格位置
  const y = yOf(t.now, min, max);
  cross.value = { x, y };
  const pct = r.prevClose > 0 ? ((t.now - r.prevClose) / r.prevClose) * 100 : 0;
  crossInfo.value = {
    time: String(t.time || ""),
    price: `价 ${t.now.toFixed(2)}  均 ${(t.avg || 0).toFixed(2)}`,
    avg: `幅 ${(pct >= 0 ? "+" : "") + pct.toFixed(2)}%`,
    pct: "",
    pctColor: "#999",
  };
}

// 提示框 x：靠右时翻到十字线左侧，防溢出图外
const tipX = computed(() => {
  if (!cross.value) return 0;
  return cross.value.x > 860 - 170 ? cross.value.x - 160 : cross.value.x + 10;
});

function onChartMove(e) {
  // 固定模式下移动不更新数据（通达信行为），但仍显示十字线
  if (pinned.value) return;
  const idx = chartIndexFromEvent(e);
  if (idx < 0) return;
  hoverIdx = idx;
  buildCross(idx);
}

function onChartLeave() {
  if (!pinned.value) { cross.value = null; crossInfo.value = null; hoverIdx = -1; }
}

function onChartClick() {
  // 单击：固定当前那一刻（数据冻结）
  if (!pinned.value && hoverIdx >= 0) {
    pinned.value = true;
    pinIdx = hoverIdx;
    buildCross(pinIdx);
  }
}

function onChartDblClick() {
  // 双击：解除固定，恢复移动跟随
  pinned.value = false;
  pinIdx = -1;
}
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
/* v9f.20.3：三栏 grid（1fr auto 1fr），中栏真居中，左右栏各自独立永不重叠 */
.top-row { display: grid; grid-template-columns: 1fr auto 1fr; align-items: center; gap: 12px; margin-bottom: 6px; }
.big-clock { justify-self: start; line-height: 1.2; }
.chart-title { justify-self: center; display: flex; align-items: baseline; gap: 2px; white-space: nowrap; }
.title-name { font-size: 26px; font-weight: 800; color: #f5c542; letter-spacing: 2px; text-shadow: 0 0 10px rgba(245,197,66,.35); }
.title-code { font-size: 17px; font-weight: 700; color: #7eb8ff; }
.quote-line { justify-self: end; text-align: right; font-size: 12px; line-height: 1.5; }
.quote-line .dim { margin-left: 0; }
.quote-line .price { font-size: 18px; font-weight: bold; margin-right: 6px; }
.up { color: #e04b4b; }
.down { color: #22a45d; }
.dim { color: #888; font-size: 12px; margin-left: 8px; }
/* 大时钟（显眼醒目） */
.clock-time { display: block; font-family: "Microsoft YaHei", "SimHei", sans-serif; font-size: 30px; font-weight: 700; color: #00e5a0; letter-spacing: 2px; font-variant-numeric: tabular-nums; text-shadow: 0 0 12px rgba(0,229,160,.45); }
.clock-date { font-size: 12px; color: #7ea8a0; }
.chart-wrap { width: 100%; cursor: crosshair; }
.trend-svg { width: 100%; height: auto; aspect-ratio: 860 / 400; background: #111; border-radius: 4px; display: block; }
.grid-line { stroke: #2a2a2a; stroke-width: 1; }
.axis-line { stroke: #555; stroke-width: 1; }
/* v9f.20：坐标全部黑体 */
.hei { font-family: "SimHei", "Microsoft YaHei", sans-serif; }
.axis-text { fill: #e8e8e8; font-size: 12px; font-weight: 600; }
.axis-text.up { fill: #ff6b6b; }
.axis-text.down { fill: #4ade80; }
.preclose-line { stroke: #666; stroke-width: 1; stroke-dasharray: 4 3; }
.legend-text { font-size: 12px; }
.last-dot { filter: drop-shadow(0 0 3px rgba(255,77,79,.8)); }
.cross-line { stroke: #888; stroke-width: 1; stroke-dasharray: 5 3; pointer-events: none; }
.tip-rect { fill: rgba(20,20,20,.92); stroke: #4aa3ff; stroke-width: 1; }
.tip-text { fill: #ddd; font-size: 12px; font-family: "SimHei", "Microsoft YaHei", sans-serif; }
.tip-text.strong { fill: #fff; font-weight: 700; font-size: 14px; }
/* v9f.20.4：背景改纯黑与图表区一致 */
.advice-box { background: #111; color: #d1e7ff; padding: 8px 10px; border-radius: 4px; margin: 8px 0; font-size: 13px; }
</style>
