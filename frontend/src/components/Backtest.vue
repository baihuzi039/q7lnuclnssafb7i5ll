<template>
  <n-layout style="padding: 10px;">
    <n-card title="策略回测（T+1 今买明卖）" size="small" style="margin-bottom: 10px;">
      <n-space vertical size="medium">
        <n-space>
          <n-select v-model:value="cfg.strategy" size="small" style="width: 220px;" :options="strategyOptions" />
          <n-input v-model:value="cfg.stockCode" size="small" placeholder="股票代码如 600519.SH（回测数据管理页查）" style="width: 240px;" />
          <n-input-number v-model:value="cfg.days" size="small" :min="60" :max="4000" style="width: 150px;">
            <template #prefix>最近</template>
            <template #suffix>根日K</template>
          </n-input-number>
          <n-button size="small" type="primary" :loading="running" @click="run">开始回测</n-button>
        </n-space>
        <n-text depth="3" style="font-size: 12px;">
          规则：信号日收盘确认，次日收盘卖出；费用按佣金万2.5双边+卖出印花税千1。数据全部读本地库，无网络请求。
        </n-text>
      </n-space>
    </n-card>

    <n-card v-if="result" title="回测结果" size="small">
      <n-space :size="30" style="margin-bottom: 10px;">
        <n-statistic label="交易次数" :value="result.totalTrades" />
        <n-statistic label="胜率">
          <template #default>{{ result.winRate.toFixed(1) }}%</template>
        </n-statistic>
        <n-statistic label="平均收益">
          <template #default>{{ result.avgReturn.toFixed(2) }}%</template>
        </n-statistic>
        <n-statistic label="最大单笔">
          <template #default>{{ result.maxReturn.toFixed(2) }}%</template>
        </n-statistic>
        <n-statistic label="最小单笔">
          <template #default>{{ result.minReturn.toFixed(2) }}%</template>
        </n-statistic>
        <n-statistic label="盈亏比" :value="result.profitLoss.toFixed(2)" />
        <n-statistic label="累计收益(复利)">
          <template #default>{{ result.totalReturn.toFixed(2) }}%</template>
        </n-statistic>
        <n-statistic label="耗时">
          <template #default>{{ result.costSeconds.toFixed(2) }}s</template>
        </n-statistic>
      </n-space>
      <n-data-table size="small" max-height="380" :columns="columns" :data="result.trades" :pagination="{pageSize: 15}" />
    </n-card>
  </n-layout>
</template>

<script setup>
import {h, reactive, ref} from 'vue'
import {NTag} from 'naive-ui'
import {RunBacktest} from '../../wailsjs/go/main/App'

const running = ref(false)
const result = ref(null)
const cfg = reactive({strategy: 'ma_cross', stockCode: '', days: 500})

const strategyOptions = [
  {label: '均线金叉（5上穿20 + 涨幅>2%）', value: 'ma_cross'},
  {label: '竞价异动（高开2~7% + 量比>3）', value: 'auction_gap'},
  {label: '放量突破（量比>2.5 + 20日新高）', value: 'volume_breakout'},
]

const columns = [
  {title: '股票', key: 'stockCode', width: 110, render: (r) => h('span', r.stockName ? `${r.stockName}(${r.stockCode})` : r.stockCode)},
  {title: '信号日', key: 'signalDay', width: 110},
  {title: '买入价', key: 'buyPrice', width: 90},
  {title: '卖出价', key: 'sellPrice', width: 90},
  {title: '净收益', key: 'pct', width: 100, render: (r) => h(NTag, {size: 'small', type: r.pct >= 0 ? 'success' : 'error'}, {default: () => r.pct.toFixed(2) + '%'})},
]

function run() {
  if (!cfg.stockCode.trim()) return
  running.value = true
  RunBacktest({...cfg, stockCode: cfg.stockCode.trim()}).then((res) => {
    result.value = res
    running.value = false
  }).catch(() => { running.value = false })
}
</script>
