<template>
  <div class="fenshi-center">
    <n-card size="small" class="mb12">
      <div class="toolbar">
        <n-button type="primary" :loading="scanning" @click="scanPool">扫描候选池（高开+放量）</n-button>
        <n-button :loading="loadingBoards" @click="loadBoards">热门板块</n-button>
        <n-input v-model:value="manualCode" placeholder="输入代码如 600519" size="small" style="width: 180px" @keyup.enter="analyzeManual" clearable />
        <n-button :loading="analyzing" @click="analyzeManual">分析该股</n-button>
        <span class="tip">候选池口径：高开1-7% + 量比≥3，剔除ST。建议基于2022-2025全市场分钟数据历史统计。</span>
      </div>
    </n-card>

    <div class="main-grid">
      <n-card size="small" title="候选池（按量比排序）" class="mb12">
        <n-data-table :columns="candColumns" :data="cands" :max-height="360" size="small" :row-props="rowProps" striped />
      </n-card>

      <n-card size="small" v-if="result" class="mb12" :title="'分时分析 · ' + result.name + '（' + result.code + '）'">
        <div class="quote-line">
          <span class="price" :class="result.changePct >= 0 ? 'up' : 'down'">{{ result.price.toFixed(2) }}</span>
          <span :class="result.changePct >= 0 ? 'up' : 'down'"> {{ result.changePct.toFixed(2) }}%</span>
          <span class="dim">高开 {{ result.gapPct.toFixed(2) }}% · 量比 {{ result.volumeRate.toFixed(2) }} · 均价 {{ result.avgPrice.toFixed(2) }}（{{ result.aboveVwapP >= 0 ? '+' : '' }}{{ result.aboveVwapP.toFixed(2) }}%）· 15分钟动量 {{ result.mom15.toFixed(2) }}%</span>
        </div>
        <svg class="trend-svg" :viewBox="'0 0 720 180'" preserveAspectRatio="none">
          <polyline :points="vwapPath" fill="none" stroke="#e6b800" stroke-width="1.2" />
          <polyline :points="pricePath" fill="none" stroke="#4aa3ff" stroke-width="1.6" />
        </svg>
        <div class="advice-box">{{ result.advice }}</div>
        <n-space vertical size="small">
          <n-tag v-for="s in result.signals" :key="s.name" :type="s.type === 'buy' ? 'success' : (s.type === 'warn' ? 'error' : 'info')" size="small" style="white-space: normal; height: auto; padding: 4px 8px;">
            {{ s.name }}：{{ s.desc }}（{{ s.prob }}）
          </n-tag>
        </n-space>
      </n-card>
      <n-card size="small" v-else class="mb12" title="分时分析">
        <n-alert v-if="analyzeError" type="error" :title="'分析失败'" style="margin-bottom: 8px">{{ analyzeError }}</n-alert>
        <n-empty description="点击候选池某行，或输入代码分析" />
      </n-card>

      <n-card size="small" title="热门板块（涨停家数）">
        <n-data-table :columns="boardColumns" :data="boards" :max-height="300" size="small" striped />
      </n-card>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, h } from "vue";
import { NCard, NButton, NDataTable, NTag, NInput, NSpace, NEmpty, NAlert } from "naive-ui";
import { FenshiScanCandidates, FenshiAnalyzeStock, FenshiHotBoards } from "../../wailsjs/go/main/App";

const scanning = ref(false);
const loadingBoards = ref(false);
const cands = ref([]);
const boards = ref([]);
const result = ref(null);
const manualCode = ref("");

const candColumns = [
  { title: "代码", key: "code", width: 80 },
  { title: "名称", key: "name", width: 100 },
  { title: "现价", key: "price", width: 70 },
  { title: "涨幅%", key: "changePct", width: 70 },
  { title: "量比", key: "volumeRate", width: 60 },
  { title: "换手%", key: "turnover", width: 60 },
];
const boardColumns = [
  { title: "板块", key: "f14" },
  { title: "涨幅%", key: "f3", width: 70 },
  { title: "涨停家数", key: "f104", width: 80 },
];

function rowProps(row) {
  return {
    style: "cursor:pointer",
    onClick: () => analyze(row.code),
  };
}

async function scanPool() {
  scanning.value = true;
  try {
    cands.value = await FenshiScanCandidates();
  } finally {
    scanning.value = false;
  }
}

async function loadBoards() {
  loadingBoards.value = true;
  try {
    boards.value = await FenshiHotBoards();
  } finally {
    loadingBoards.value = false;
  }
}

const analyzing = ref(false);
const analyzeError = ref("");

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

async function analyzeManual() {
  const code = (manualCode.value || "").trim();
  if (code) await analyze(code);
}

const pricePath = computed(() => ptsPath(result.value ? result.value.trend : [], "now"));
const vwapPath = computed(() => ptsPath(result.value ? result.value.trend : [], "avg"));

function ptsPath(trend, col) {
  if (!trend || trend.length < 2) return "";
  let min = Infinity, max = -Infinity;
  for (const t of trend) { const v = t[col]; if (v > max) max = v; if (v > 0 && v < min) min = v; }
  if (min === Infinity) return "";
  const n = trend.length;
  return trend.map((t, i) => {
    const x = (i / (n - 1)) * 716 + 2;
    const y = 176 - ((t[col] - min) / (max - min || 1)) * 168;
    return x.toFixed(1) + "," + y.toFixed(1);
  }).join(" ");
}
</script>

<style scoped>
.fenshi-center { padding: 12px; }
.mb12 { margin-bottom: 12px; }
.toolbar { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.tip { font-size: 12px; color: #999; }
.main-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.quote-line { font-size: 15px; margin-bottom: 6px; }
.quote-line .price { font-size: 22px; font-weight: bold; margin-right: 6px; }
.up { color: #e04b4b; }
.down { color: #22a45d; }
.dim { color: #888; font-size: 12px; margin-left: 8px; }
.trend-svg { width: 100%; height: 180px; background: #111; border-radius: 4px; }
.advice-box { background: #1f2937; color: #d1e7ff; padding: 8px 10px; border-radius: 4px; margin: 8px 0; font-size: 13px; }
</style>
