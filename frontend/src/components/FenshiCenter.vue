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
        <!-- v9f.23：竞价分析卡片（09:15-09:25 采集，独立于盘中建议） -->
        <n-card size="small" v-if="auctionAdv" class="mb12">
          <div class="advice-v2" :class="auctionAdv.bias === '偏多' ? 'long' : (auctionAdv.bias === '偏空' ? 'short' : 'flat')">
            <div class="adv-head">
              <span class="adv-bias">【竞价分析】{{ auctionAdv.bias }}</span>
              <span class="adv-score">高开 {{ auctionAdv.gapPct.toFixed(2) }}% · 竞价量 {{ auctionAdv.volRatio.toFixed(1) }}‰</span>
            </div>
            <div v-for="(rs, ri) in auctionAdv.reasons" :key="ri" class="adv-reason">{{ rs }}</div>
          </div>
        </n-card>
        <n-card size="small" v-if="result" class="mb12 chart-card">
          <!-- v9f.23.4：五栏布局——时钟左 | 加价黄框 | 名称中(蓝框) | 让价黄框 | 行情右(绿框) -->
          <div class="top-row">
            <div class="big-clock">
              <span class="clock-time">{{ clockStr }}</span>
              <span class="clock-date">{{ clockDate }}</span>
            </div>
            <div class="quote-line">
              <div class="live-price-row">
                <span class="title-name">{{ result.name }}</span>
                <span class="title-code">（{{ stripPrefix(result.code) }}）</span>
                <span class="limit-col">
                  <span class="limit-inline buy">加 {{ (result.price * 1.02).toFixed(2) }}</span>
                  <span class="limit-inline sell">让 {{ (result.price * 0.98).toFixed(2) }}</span>
                </span>
                <span class="live-price" :class="result.changePct >= 0 ? 'up' : 'down'" :key="result.price">{{ result.price.toFixed(2) }}</span>
                <span class="live-pct" :class="result.changePct >= 0 ? 'up' : 'down'" :key="result.changePct">{{ result.changePct >= 0 ? '+' : '' }}{{ result.changePct.toFixed(2) }}%</span>
              </div>
              <div class="dim">高开 {{ result.gapPct.toFixed(2) }}% · 量比 {{ result.volumeRate.toFixed(2) }} · 均价 {{ result.avgPrice.toFixed(2) }}（{{ result.aboveVwapP >= 0 ? '+' : '' }}{{ result.aboveVwapP.toFixed(2) }}%）· 15分钟动量 {{ result.mom15.toFixed(2) }}% · <span style="color:#c9a24a">{{ result.advice }}</span></div>
            </div>
          </div>

          <div
            class="chart-wrap"
            @mousemove="onChartMove"
            @mouseleave="onChartLeave"
            @click="onChartClick"
            @dblclick="onChartDblClick"
          >
            <svg class="trend-svg" viewBox="0 0 1420 700" preserveAspectRatio="none">
              <!-- 横向网格 + 纵轴刻度（黑体） -->
              <g v-for="(g, gi) in gridLines" :key="'g' + gi">
                <line :x1="CH.x0" :x2="CH.x1" :y1="g.y" :y2="g.y" class="grid-line" />
                <text :x="CH.x0 - 8" :y="g.y + 4" class="axis-text hei" text-anchor="end">{{ g.priceLabel }}</text>
                <!-- v9f.27.2：涨跌幅移到右轴（通达信布局） -->
                <text :x="CH.x1 + 8" :y="g.y + 4" class="axis-text hei" :class="g.pctClass" text-anchor="start">{{ g.pctLabel }}</text>
              </g>
              <!-- 轴边界 -->
              <line :x1="CH.x0" :x2="CH.x0" :y1="CH.y0" :y2="CH.y1" class="axis-line" />
              <line :x1="CH.x1" :x2="CH.x1" :y1="CH.y0" :y2="CH.y1" class="axis-line" />
              <line :x1="CH.x0" :x2="CH.x1" :y1="CH.y1" :y2="CH.y1" class="axis-line" />
              <!-- 横轴：每半小时刻度（黑体） -->
              <g v-for="(t, ti) in xTicks" :key="'t' + ti">
                <line :x1="t.x" :x2="t.x" :y1="CH.y0" :y2="CH.y1" class="grid-line" />
                <text :x="t.x" :y="VOL_AREA.y1 + 20" class="axis-text hei" text-anchor="middle">{{ t.label }}</text>
              </g>
              <!-- 昨收基准虚线 -->
              <line v-if="prevCloseY >= CH.y0 && prevCloseY <= CH.y1" :x1="CH.x0" :x2="CH.x1" :y1="prevCloseY" :y2="prevCloseY" class="preclose-line" />
              <!-- 均价线 + 价格线 -->
              <polyline :points="vwapPath" fill="none" stroke="#e6b800" stroke-width="1.3" />
              <polyline :points="pricePath" fill="none" stroke="#4aa3ff" stroke-width="1.6" />
              <!-- v9f.26.1：成交量副图（通达信标准：涨红跌绿量柱） -->
              <line :x1="CH.x0" :x2="CH.x1" :y1="VOL_AREA.y0 - 4" :y2="VOL_AREA.y0 - 4" class="axis-line" />
              <line :x1="CH.x0" :x2="CH.x1" :y1="VOL_AREA.y1" :y2="VOL_AREA.y1" class="axis-line" />
              <rect
                v-for="(b, bi) in volBars"
                :key="'vb' + bi"
                :x="b.x"
                :y="b.y"
                :width="b.w"
                :height="b.h"
                :fill="b.color"
                class="vol-bar"
              />
              <text :x="CH.x0 - 8" :y="VOL_AREA.y0 + 12" class="axis-text hei" text-anchor="end">{{ volMaxLabel }}</text>
              <text :x="CH.x0 - 8" :y="VOL_AREA.y1" class="axis-text hei" text-anchor="end">0</text>
              <!-- 最新价小红点 -->
              <circle v-if="lastDot" :cx="lastDot.x" :cy="lastDot.y" r="3.5" fill="#ff4d4f" class="last-dot" />
              <!-- 十字线（悬停或固定时显示） -->
              <g v-if="cross">
                <line :x1="cross.x" :x2="cross.x" :y1="CH.y0" :y2="CH.y1" class="cross-line" />
                <line :x1="CH.x0" :x2="CH.x1" :y1="cross.y" :y2="cross.y" class="cross-line" />
                <circle :cx="cross.x" :cy="cross.y" r="3" fill="#fff" />
                <!-- 数据提示框（v9f.23.2：价/幅红涨绿跌） -->
                <g v-if="crossInfo">
                  <rect :x="tipX" :y="CH.y0 + 4" width="150" height="88" rx="4" class="tip-rect" />
                  <text :x="tipX + 10" :y="CH.y0 + 24" class="tip-text strong">{{ crossInfo.time }}</text>
                  <text :x="tipX + 10" :y="CH.y0 + 42" class="tip-text" :style="{ fill: crossInfo.priceColor }">{{ crossInfo.price }}</text>
                  <text :x="tipX + 10" :y="CH.y0 + 58" class="tip-text">{{ crossInfo.avg }}</text>
                  <text :x="tipX + 10" :y="CH.y0 + 74" class="tip-text" :style="{ fill: crossInfo.pctColor }">{{ crossInfo.pctStr }}</text>
                </g>
              </g>
              <!-- 图例 -->
              <text :x="CH.x0 + 8" y="20" class="legend-text hei" fill="#4aa3ff">— 价格</text>
              <text :x="CH.x0 + 68" y="20" class="legend-text hei" fill="#e6b800">— 均价</text>
              <!-- 模式提示（固定时显示） -->
              <text v-if="pinned" :x="CH.x1 - 8" y="20" class="legend-text hei" fill="#ff9800" text-anchor="end">已固定 · 双击解除</text>
            </svg>
          </div>
          <!-- v9f.27.3：腾讯源行已上移到顶部高开行后面，原底部行删除 -->
          <!-- v9f.22：规则引擎实时建议 -->
          <div v-if="result.adviceV2" class="advice-v2" :class="result.adviceV2.bias === '偏多' ? 'long' : (result.adviceV2.bias === '偏空' ? 'short' : 'flat')">
            <div class="adv-head">
              <span class="adv-bias">【实时建议】{{ result.adviceV2.bias }}（置信度：{{ result.adviceV2.confidence || '低' }}）</span>
              <span class="adv-score">多空强度 {{ result.adviceV2.score > 0 ? '+' : '' }}{{ Math.round(result.adviceV2.score) }}（+看多 -看空，0 为中性）</span>
            </div>
            <div v-if="result.adviceV2.suggestion" class="adv-suggest">📌 {{ result.adviceV2.suggestion }}</div>
            <div v-for="(rs, ri) in result.adviceV2.reasons" :key="ri" class="adv-reason">{{ rs }}</div>
          </div>
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
import { FenshiWatchlist, FenshiWatchlistAdd, FenshiWatchlistRemove, FenshiAnalyzeStock, FenshiNowTime, FenshiAuctionSnap, FenshiAuctionEval, FenshiDailyCtxGet } from "../../wailsjs/go/main/App";

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

// v9f.23.4：当前正在分析的股高亮标蓝背景，一眼看出选中哪只
const selectedCode = ref("");
function rowProps(row) {
  const sel = row.code === selectedCode.value;
  return {
    class: sel ? "fs-row-selected" : "", // v9f.25.1：tr背景会被td盖住，改用class+深度选择器作用到td
    style: "cursor:pointer",
    onClick: () => analyze(row.code),
  };
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

// ==================== 盘中自动刷新（v9f.23.7） ====================
let refreshTimer = null;
let refreshing = false;
onMounted(() => {
  loadWatchlist();
  startClock();
  auctionTimer = setInterval(auctionPoll, 15000);
  // v9f.27.0：盘中每5秒自动刷新当前股分时（仅交易日 9:30-15:00），看盘曲线最多落后5秒
  refreshTimer = setInterval(autoRefresh, 5000);
});
onUnmounted(() => {
  stopClock();
  if (auctionTimer) clearInterval(auctionTimer);
  if (refreshTimer) clearInterval(refreshTimer);
});


async function autoRefresh() {
  if (refreshing || !selectedCode.value) return; // 上一轮未完或无选中股则跳过
  const now = new Date(Date.now() + clockOffset);
  const day = now.getDay();
  if (day === 0 || day === 6) return;
  const hm = now.getHours() * 100 + now.getMinutes();
  // 盘中 + 竞价时段都刷（竞价时数据会预热）
  if ((hm < 930 || hm > 1500) && !(hm >= 915 && hm <= 925)) return;
  refreshing = true;
  try {
    const r = await FenshiAnalyzeStock(selectedCode.value);
    if (r && !r.errMsg) result.value = r;
  } catch (e) { /* 静默，下一轮重试 */ }
  finally { refreshing = false; }
}

async function analyze(code) {
  analyzing.value = true;
  selectedCode.value = code; // v9f.23.4：记录选中，列表高亮
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

// ==================== 竞价分析（v9f.23） ====================
// 09:15-09:25 每 15s 采样一次腾讯快照；09:26 后汇总评分一次
// 采集目标：当前分析过的股票，无则取自选股第一只
const auctionPts = ref([]);
const auctionAdv = ref(null);
let auctionTimer = null;
let auctionDoneDate = ""; // 当日已评分标记，防止重复评分

function auctionActiveCode() {
  if (result.value && result.value.code) return result.value.code;
  if (watchlist.value && watchlist.value.length > 0) return watchlist.value[0].code;
  return "";
}

async function auctionPoll() {
  if (auctionAdv.value) return; // 当日已出结论，停止采集
  const now = new Date(Date.now() + clockOffset);
  const day = now.getDay();
  if (day === 0 || day === 6) return;
  const hm = now.getHours() * 100 + now.getMinutes();
  if (hm < 915 || hm > 926) return;
  const code = auctionActiveCode();
  if (!code) return;
  try {
    const snap = await FenshiAuctionSnap(code);
    if (!snap || !snap.inWindow || snap.price <= 0) return;
    const pts = auctionPts.value;
    const last = pts.length ? pts[pts.length - 1] : null;
    if (last && last.mins === snap.mins) return; // 同一分钟去重
    pts.push({ mins: snap.mins, price: snap.price, vol: snap.vol });
    if (pts.length > 60) pts.shift();
    if (hm >= 926 && pts.length >= 2) await auctionEvaluate(code);
  } catch (e) { /* 竞价期网络抖动忽略，下一轮重试 */ }
}

async function auctionEvaluate(code) {
  const day = new Date().toDateString();
  if (auctionDoneDate === day) return;
  auctionDoneDate = day;
  try {
    const dctx = await FenshiDailyCtxGet(code);
    const adv = await FenshiAuctionEval(auctionPts.value, dctx.prevClose, dctx.prevVol);
    if (adv) auctionAdv.value = adv;
  } catch (e) { /* 评分失败不影响主流程 */ }
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
const CH = { x0: 56, x1: 1330, y0: 8, y1: 540 }; // v9f.27.2：主图540高撑大，右侧留90px给涨跌幅轴
const VOL_AREA = { y0: 560, y1: 640 }; // v9f.27.2：量柱贴底80px，时间轴最下

const pricePath = computed(() => ptsPath("now"));
const vwapPath = computed(() => ptsPath("avg"));

// v9f.26.1：成交量副图——涨红跌绿量柱（与昨收比：高于昨收为红）
const volBars = computed(() => {
  const r = result.value;
  const trend = r ? r.trend : [];
  if (!trend || trend.length < 2) return [];
  // v9f.26.5：量柱刻度截断——按盘中位数量3倍为满刻度，巨量柱封顶，普通量柱不被压扁
  const volsAll = trend.map((t) => t.vol).filter((v) => v > 0).sort((a, b) => a - b);
  if (!volsAll.length) return [];
  const mid = volsAll[Math.floor(volsAll.length / 2)];
  const vmax = Math.max(mid * 3, 1); // v9f.26.6：满刻度固定为中位数量3倍，巨量柱封顶不再参与定刻度
  const n = trend.length;
  const span = CH.x1 - CH.x0;
  const bw = Math.max(1.5, span / 241 - 0.6);
  const bars = [];
  let prev = trend[0].now;
  for (let i = 0; i < n; i++) {
    const t = trend[i];
    const off = Math.max(0, Math.min(240, minuteOffset(t.time)));
    const x = CH.x0 + (off / 240) * span;
    const h = Math.max(1, (t.vol / vmax) * (VOL_AREA.y1 - VOL_AREA.y0));
    const up = t.now >= prev; // 与上一分钟比：涨红跌绿（通达信同口径）
    bars.push({ x: x.toFixed(1), y: (VOL_AREA.y1 - h).toFixed(1), w: bw.toFixed(1), h: h.toFixed(1), color: up ? "#e04b4b" : "#22a45d" });
    prev = t.now;
  }
  return bars;
});
const volMaxLabel = computed(() => {
  // v9f.26.5：与 volBars 的截断刻度保持一致（中位数量3倍）
  const r = result.value;
  const trend = r ? r.trend : [];
  const volsAll = (trend || []).map((t) => t.vol).filter((v) => v > 0).sort((a, b) => a - b);
  if (!volsAll.length) return "0";
  const vmax = Math.max(volsAll[Math.floor(volsAll.length / 2)] * 3, 1); // v9f.26.6：与 volBars 截断口径一致
  if (vmax >= 1e6) return (vmax / 1e6).toFixed(1) + "M";
  if (vmax >= 1e3) return (vmax / 1e3).toFixed(0) + "K";
  return String(Math.round(vmax));
});

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
  const N = 14; // v9f.27.2：纵轴14格，刻度更密（通达信风格）
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
  // viewBox 1420 宽，preserveAspectRatio=none → 线性映射
  const vx = ((e.clientX - rect.left) / rect.width) * 1420;
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
  // v9f.23.2：浮框价格与涨跌幅红涨绿跌（随鼠标位置实时变换）
  crossInfo.value = {
    time: String(t.time || ""),
    price: `价 ${t.now.toFixed(2)}`,
    priceColor: pct >= 0 ? "#e04b4b" : "#22a45d",
    avg: `均 ${(t.avg || 0).toFixed(2)}`,
    pctStr: `幅 ${(pct >= 0 ? "+" : "") + pct.toFixed(2)}%`,
    pctColor: pct >= 0 ? "#e04b4b" : "#22a45d",
  };
}

// 提示框 x：靠右时翻到十字线左侧，防溢出图外
const tipX = computed(() => {
  if (!cross.value) return 0;
  return cross.value.x > 1420 - 170 ? cross.value.x - 160 : cross.value.x + 10;
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
/* v9f.25.1：选中行高亮——naive-ui 的 td 背景优先级高，必须用 :deep 穿透作用到 td */
:deep(.fs-row-selected td) {
  background-color: rgba(0, 120, 255, 0.35) !important;
}
:deep(.fs-row-selected td:first-child) {
  border-left: 3px solid #4aa3ff;
}
:deep(.fs-row-selected span) {
  color: #7fc4ff !important;
  font-weight: 600;
}
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
/* v9f.23.7：四栏 grid——时钟 | 行情区(名称+加让价+价格+涨幅+指标) | 让价并入行情行 */
.top-row { display: grid; grid-template-columns: auto 1fr; align-items: center; gap: 18px; padding: 10px 0; margin-bottom: 6px; }
.limit-col { display: flex; flex-direction: column; gap: 3px; margin-left: 6px; flex-shrink: 0; }
.limit-inline { font-size: 13px; font-weight: 700; padding: 0px 8px; border-radius: 3px; margin-left: 0; }
.limit-inline.buy { color: #e04b4b; border: 1px solid rgba(224,75,75,.5); }
.limit-inline.sell { color: #22a45d; border: 1px solid rgba(34,164,93,.5); }
.big-clock { justify-self: start; line-height: 1.2; }
.chart-title { justify-self: center; display: flex; align-items: baseline; gap: 2px; white-space: nowrap; }
.title-name { font-size: 26px; font-weight: 800; color: #f5c542; letter-spacing: 2px; text-shadow: 0 0 10px rgba(245,197,66,.35); }
.title-code { font-size: 17px; font-weight: 700; color: #7eb8ff; }
.quote-line { justify-self: start; text-align: left; font-size: 12px; line-height: 1.5; padding-left: 36px; }
.quote-line .dim { margin-left: 0; color: #d1e7ff; display: block; margin-top: 8px; clear: both; }
/* v9f.23.2：实时价格通达信化——昨收0%红涨绿跌，与名称同字号，变动轻微闪烁 */
.live-price-row { display: flex; align-items: center; gap: 10px; height: 44px; }
/* v9f.23.6：名称样式回归独立 chart-title（金色大字居中） */
.live-price { font-size: 26px; font-weight: bold; font-family: "Microsoft YaHei", "SimHei", sans-serif; }
.live-pct { font-size: 15px; font-weight: bold; font-family: "Microsoft YaHei", "SimHei", sans-serif; }
@keyframes price-blink {
  0% { opacity: 1; }
  50% { opacity: 0.55; }
  100% { opacity: 1; }
}
.live-price, .live-pct { animation: price-blink 0.5s ease-in-out 1; }
/* 数据刷新时重触发闪烁（key 变化重建元素） */
.live-price { transition: color 0.2s; }
.up { color: #e04b4b; }
.down { color: #22a45d; }
.dim { color: #888; font-size: 12px; margin-left: 8px; }
/* 大时钟（显眼醒目） */
.clock-time { line-height: 1.1; display: block; font-family: "Microsoft YaHei", "SimHei", sans-serif; font-size: 30px; font-weight: 700; color: #00e5a0; letter-spacing: 2px; font-variant-numeric: tabular-nums; text-shadow: 0 0 12px rgba(0,229,160,.45); }
.clock-date { font-size: 12px; color: #7ea8a0; }
.chart-wrap { width: 100%; cursor: crosshair; }
.trend-svg { width: 100%; height: 76vh; aspect-ratio: 1420 / 700; background: #111; border-radius: 4px; display: block; } /* v9f.27.2：高度撑满首屏（76vh），腾讯源+实时建议被推到首屏外需下拉 */
.grid-line { stroke: #2a2a2a; stroke-width: 1; }
.vol-bar { opacity: 0.85; }
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
.advice-box { background: #111; color: #d1e7ff; padding: 8px 10px; border-radius: 4px; margin: 24px 0 8px; font-size: 13px; }
/* v9f.22：规则引擎建议卡片 */
.advice-v2 { background: #111; border-radius: 4px; margin-bottom: 8px; padding: 10px 12px; border-left: 3px solid #888; overflow: hidden; }
.advice-v2.long { border-left-color: #e04b4b; }
.advice-v2.short { border-left-color: #22a45d; }
.adv-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 6px; gap: 8px; }
.adv-head > span:first-child { flex-shrink: 0; }
.adv-bias { font-size: 16px; font-weight: 700; flex-shrink: 0; }
.advice-v2 .adv-score { flex-shrink: 1; min-width: 0; text-align: right; word-break: break-all; }
.advice-v2.long .adv-bias { color: #ff6b6b; }
.advice-v2.short .adv-bias { color: #4ade80; }
.advice-v2.flat .adv-bias { color: #d1d5db; }
.adv-score { color: #d1e7ff; font-size: 12px; word-break: break-all; }
.adv-reason { color: #c8d6e5; font-size: 13px; line-height: 1.7; word-break: break-all; white-space: normal; margin-right: 0; }
.adv-suggest { color: #ffd700; font-size: 14px; font-weight: 600; margin-top: 6px; line-height: 1.6; word-break: break-all; }
/* v9f.23.2：卡片内文字限制在容器内，不再横向溢出 */
.advice-v2, .advice-v2 * { max-width: 100%; box-sizing: border-box; overflow-wrap: anywhere; }
.adv-head, .adv-reason { overflow: hidden; }
.adv-reason { padding-right: 8px; }
/* 浮框内多空强度文字不溢出 */
.adv-head { width: 100%; }
</style>
