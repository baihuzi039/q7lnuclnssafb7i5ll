#!/bin/bash
# 发包前真实编译核查（v3制度）：用真实Go编译器做类型级检查，比自制脚本强一个维度
# 用法: bash 发包前真实编译核查.sh  [改动文件...]（不传则核查全部app_track*.go）
set -e
TC=/tmp/typecheck
# v9b.3: models改动也同步(修复: Builtin字段类改动需同步模型)
WS=/home/lingxi/workspace/go-stock
# 环境自愈：typecheck丢失时从.ci备份重建（含go工具链检查）
if [ ! -f "$TC/go.mod" ]; then
  echo "[i] typecheck环境丢失，从.ci备份重建..."
  mkdir -p "$TC" && cp -r "$WS/.ci/." "$TC/"
fi
if [ ! -x /tmp/go124/bin/go ]; then
  echo "[i] Go工具链丢失，重新下载..."
  curl -sL -o /tmp/go.tgz https://golang.google.cn/dl/go1.24.10.linux-amd64.tar.gz --max-time 55
  mkdir -p /tmp/go124 && tar xzf /tmp/go.tgz -C /tmp/go124 --strip-components=1
fi
export PATH=/tmp/go124/bin:$PATH GOCACHE=/tmp/gocache5 GOTOOLCHAIN=auto GOPROXY=https://goproxy.cn,direct GOROOT=/tmp/go124

# 同步最新改动文件到typecheck环境（并做本地适配patch）
for f in app_track.go app_track_stats.go app_track_export.go app_track_overnight.go app_track_niuzhi.go; do
  [ "$f" = "app_track.go" ] && cp "$WS/backend/models/track.go" "$TC/models/track.go" 2>/dev/null
  [ -f "$WS/$f" ] && cp "$WS/$f" "$TC/$f"
done
cd "$TC"
# patch：包名/import适配（python处理，幂等）
python3 - <<'PYEOF'
import re
for fp in ['app_track.go','app_track_stats.go','app_track_export.go','app_track_overnight.go','app_track_niuzhi.go']:
    t=open(fp,encoding='utf-8').read()
    t=re.sub(r'^package \w+','package main',t,count=1,flags=re.M)
    t=t.replace('"go-stock/backend/models"','"typecheck/models"').replace("go-stock/backend/db","typecheck/db").replace("go-stock/backend/data","typecheck/data").replace('"go-stock/backend/logger"','"typecheck/logger"')
    # 确保models/db import存在
    if '"typecheck/models"' not in t:
        t=t.replace('import (\n','import (\n\t"typecheck/models"\n',1)
    if '"typecheck/db"' not in t:
        t=t.replace('import (\n','import (\n\t"typecheck/db"\n',1)
    open(fp,'w',encoding='utf-8').write(t)
# export文件不用logger
t=open('app_track_export.go',encoding='utf-8').read()
t=t.replace('\n\t"typecheck/logger"','',1)
open('app_track_export.go','w',encoding='utf-8').write(t)
PYEOF
# vet整个包（含models/logger/db桩）
if go vet ./... 2>&1 | tee /tmp/vet_last.log | grep -v "^#" | grep -q .; then
  echo "FAIL 真实编译器发现错误："
  grep "^vet:" /tmp/vet_last.log || cat /tmp/vet_last.log
  exit 1
fi
echo "PASS 真实Go编译器类型检查通过（vet-exit=0）"
