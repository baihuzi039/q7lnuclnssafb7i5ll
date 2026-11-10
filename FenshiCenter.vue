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
          <n-data-table :columns="watchColumns" :data="watchlist" :max-height="520" size="small" :row-props="rowProps" striped />
        </n-card>
      </div>

      <!-- 右侧：大分时图 -->
      <div class="right-pane">
        <n-card size="small" v-if="result" class="mb12" :title="'分时分析 · ' + result.name + '（' + result.code + '）'">
          <div class="quote-line">
            <span class="price" :class="result.changePct >= 0 ? 'up' : 'down'">{{ result.price.toFixed(2) }}</span>
            <span :class="result.changePct >= 0 ? 'up' : 'down'"> {{ result.changePct.toFixed(2) }}%</span>
            <span class="dim">高开 {{ result.gapPct.toFixed(2) }}% · 量比 {{ result.volumeRate.toFixed(2) }} · 均价 {{ result.avgPrice.toFixed(2) }}（{{ result.aboveVwapP >= 0 ? '+' : '' }}{{ result.aboveVwapP.toFixed(2) }}%）· 15分钟动量 {{ result.mom15.toFixed(2) }}%</span>
          </div>
          <svg class="trend-svg" :viewBox="'0 0 720 320'" preserveAspectRatio="none">
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
  { title: "代码", key: "code", width: 76 },
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

async function addStock() {
  const code = (manualCode.value || "").trim();
  if (!code) return;
  adding.value = true;
  try {
    const msg = await FenshiWatchlistAdd(code);
    if (msg && msg.indexOf("成功") >= 0) {
      manualCode.value = "";
      await loadWatchlist();
      await analyze(code);
    }
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
    const y = 316 - ((t[col] - min) / (max - min || 1)) * 308;
    return x.toFixed(1) + "," + y.toFixed(1);
  }).join(" ");
}
</script>

<style scoped>
.fenshi-center { padding: 12px; }
.mb12 { margin-bottom: 12px; }
.layout { display: grid; grid-template-columns: 300px 1fr; gap: 12px; }
.left-pane { min-width: 0; }
.right-pane { min-width: 0; }
.add-row { display: flex; gap: 8px; margin-bottom: 8px; }
.quote-line { font-size: 15px; margin-bottom: 6px; }
.quote-line .price { font-size: 24px; font-weight: bold; margin-right: 6px; }
.up { color: #e04b4b; }
.down { color: #22a45d; }
.dim { color: #888; font-size: 12px; margin-left: 8px; }
.trend-svg { width: 100%; height: 320px; background: #111; border-radius: 4px; }
.advice-box { background: #1f2937; color: #d1e7ff; padding: 8px 10px; border-radius: 4px; margin: 8px 0; font-size: 13px; }
</style>
