#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
go-stock 发包前强制核查脚本（全过才允许交付，任何 FAIL = 禁止发包）
用法: python3 核查脚本_发包前必须全过.py 文件1.go 文件2.go ...
"""
import sys, re, os

REPO_PKGS = ['db','logger','models','data','convertor','strutil','simplifiedchinese','transform','runtime']

def strip_strings_comments(src):
    out = list(src); i = 0; n = len(src)
    while i < n:
        c = src[i]
        if c == '"':
            j = i+1
            while j < n:
                if src[j] == '\\': j += 2; continue
                if src[j] == '"': break
                j += 1
            for k in range(i, min(j+1, n)):
                if out[k] != '\n': out[k] = ' '
            i = j+1
        elif c == '`':
            j = src.find('`', i+1)
            j = n-1 if j < 0 else j
            for k in range(i, j+1):
                if out[k] != '\n': out[k] = ' '
            i = j+1
        elif c == "'":
            j = i+1
            while j < n:
                if src[j] == '\\': j += 2; continue
                if src[j] == "'": break
                j += 1
            for k in range(i, min(j+1, n)):
                if out[k] != '\n': out[k] = ' '
            i = j+1
        elif c == '/' and i+1 < n and src[i+1] == '/':
            j = src.find('\n', i); j = n if j < 0 else j
            for k in range(i, j): out[k] = ' '
            i = j
        elif c == '/' and i+1 < n and src[i+1] == '*':
            j = src.find('*/', i+2); j = n-2 if j < 0 else j
            for k in range(i, j+2):
                if out[k] != '\n': out[k] = ' '
            i = j+2
        else:
            i += 1
    return ''.join(out)

def line_of(src, pos): return src[:pos].count('\n')+1

def strip_strings_placeholder(src):
    """注释去成空格，字符串字面量替换为占位符 S（保参数计数正确）"""
    out = list(src); i = 0; n = len(src)
    while i < n:
        c = src[i]
        if c == '"':
            j = i+1
            while j < n:
                if src[j] == '\\': j += 2; continue
                if src[j] == '"': break
                j += 1
            out[i] = 'S'
            for k in range(i+1, min(j+1, n)):
                if out[k] != '\n': out[k] = ' '
            i = j+1
        elif c == '`':
            j = src.find('`', i+1); j = n-1 if j < 0 else j
            out[i] = 'S'
            for k in range(i+1, j+1):
                if out[k] != '\n': out[k] = ' '
            i = j+1
        elif c == "'":
            j = i+1
            while j < n:
                if src[j] == '\\': j += 2; continue
                if src[j] == "'": break
                j += 1
            out[i] = 'S'
            for k in range(i+1, min(j+1, n)):
                if out[k] != '\n': out[k] = ' '
            i = j+1
        elif c == '/' and i+1 < n and src[i+1] == '/':
            j = src.find('\n', i); j = n if j < 0 else j
            for k in range(i, j): out[k] = ' '
            i = j
        elif c == '/' and i+1 < n and src[i+1] == '*':
            j = src.find('*/', i+2); j = n-2 if j < 0 else j
            for k in range(i, j+2):
                if out[k] != '\n': out[k] = ' '
            i = j+2
        else: i += 1
    return ''.join(out)

def strip_comments_only(src):
    """只去注释（保留字符串），返回等长掩码"""
    out = list(src); i = 0; n = len(src)
    while i < n:
        c = src[i]
        if c == '"':
            j = i+1
            while j < n:
                if src[j] == '\\': j += 2; continue
                if src[j] == '"': break
                j += 1
            i = j+1
        elif c == '`':
            j = src.find('`', i+1); j = n-1 if j < 0 else j; i = j+1
        elif c == "'":
            j = i+1
            while j < n:
                if src[j] == '\\': j += 2; continue
                if src[j] == "'": break
                j += 1
            i = j+1
        elif c == '/' and i+1 < n and src[i+1] == '/':
            j = src.find('\n', i); j = n if j < 0 else j
            for k in range(i, j): out[k] = ' '
            i = j
        elif c == '/' and i+1 < n and src[i+1] == '*':
            j = src.find('*/', i+2); j = n-2 if j < 0 else j
            for k in range(i, j+2):
                if out[k] != '\n': out[k] = ' '
            i = j+2
        else: i += 1
    return ''.join(out)

def extract_imports(src, masked):
    # import 路径本身是字符串，需从原始 src 提取块（先去注释防注释内括号截断）；使用检查在 masked 上做
    imports = []
    src_nc = strip_comments_only(src)
    for m in re.finditer(r'import\s*\(([^)]*)\)', src_nc, re.S):
        # 块内注释可能含括号/字符串，先去掉再提取（路径字符串本身保留）
        block = re.sub(r'//[^\n]*', '', m.group(1))
        for lm in re.finditer(r'^\s*(?:(\w+)\s+|(_|\.)\s+)?\s*"([\w./\-]+)"', block, re.M):
            alias, blank, path = lm.group(1), lm.group(2), lm.group(3)
            imports.append((blank if blank else (alias or path.split('/')[-1]), path, m.start()))
    for m in re.finditer(r'^import\s+(?:\w+\s+)?"([\w./\-]+)"', src, re.M):
        path = m.group(1)
        imports.append((path.split('/')[-1], path, m.start()))
    return imports

def check_brackets(name, src, masked):
    fails = []
    close_map = {')':'(', ']':'[', '}':'{'}
    st = []
    for i,ch in enumerate(masked):
        if ch in '([{': st.append((ch,i))
        elif ch in ')]}':
            if not st or st[-1][0] != close_map[ch]:
                fails.append(f"  {name}:{line_of(src,i)} 括号不匹配 {ch}")
                if st: st.pop()
            else: st.pop()
    for ch,i in st:
        fails.append(f"  {name}:{line_of(src,i)} 未闭合 {ch}")
    return fails

def check_imports(name, src, masked):
    fails = []
    imports = extract_imports(src, masked)
    for alias, path, pos in imports:
        if alias == '_': continue  # 匿名导入（副作用导入）无需使用检查
        last = path.split('/')[-1]
        if '-' in last or re.match(r'^v\d+$', last): continue  # 包名≠路径尾段，无法可靠核对
        if not re.search(r'\b'+re.escape(alias)+r'\.', masked):
            fails.append(f"  {name}:{line_of(src,pos)} import {path} 未使用")
    imported = {a for a,_,_ in imports}
    std = {'fmt','os','strings','time','sort','errors','bytes','math','io','bufio','context','regexp','sync','net','path','filepath','utf16','utf8','exec','json'}
    for pkg in std:
        if re.search(r'\b'+pkg+r'\.', masked) and pkg not in imported:
            fails.append(f"  {name}: 使用 {pkg}. 但未 import")
    for alias in REPO_PKGS:
        if re.search(r'\b'+alias+r'\.', masked) and alias not in imported:
            fails.append(f"  {name}: 使用 {alias}. 但未 import")
    return fails

def count_call_args(masked, open_pos):
    """返回顶层实参个数（顶层逗号数+1；空参为0）"""
    depth = 0; commas = 0; has_content = False
    for i in range(open_pos, len(masked)):
        ch = masked[i]
        if ch in '([{': depth += 1
        elif ch in ')]}':
            depth -= 1
            if depth == 0:
                return (commas + 1) if has_content else 0, i
        elif ch == ',' and depth == 1:
            commas += 1; has_content = True  # 逗号前的段有内容
        elif not ch.isspace():
            has_content = True
    return -1, len(masked)

def collect_defs(files):
    """收集函数定义：(fn_name, nargs, file, line)"""
    defs = {}
    file_pkg = {}
    for f in files:
        src = open(f, encoding='utf-8').read()
        pm = re.search(r'^package\s+(\w+)', src, re.M)
        file_pkg[f] = pm.group(1) if pm else None
        masked = strip_strings_comments(src)
        for m in re.finditer(r'func\s+(?:\((\w+)\s+\*?(\w+)\)\s+)?(\w+)\s*\(', masked):
            recv_type = m.group(2)  # 无接收者为 None
            # 找参数列表闭括号
            depth=1; j=m.end()
            while j < len(masked) and depth>0:
                if masked[j]=='(': depth+=1
                elif masked[j]==')': depth-=1
                j+=1
            params = masked[m.end():j-1]
            # 顶层逗号分割
            d=0; nargs=0 if not params.strip() else 1
            for ch in params:
                if ch in '([{': d+=1
                elif ch in ')]}': d-=1
                elif ch==',' and d==0: nargs+=1
            key = (recv_type, m.group(3))
            defs.setdefault(key, []).append((nargs, f, line_of(src,m.start())))
            # 包级函数同时以其所属包名注册（供 pkg.Fn( 调用匹配）
            if recv_type is None and file_pkg[f]:
                defs.setdefault((file_pkg[f], m.group(3)), []).append((nargs, f, line_of(src,m.start())))
    return defs

def check_calls(name, src, masked, ph_masked, defs, repo_files):
    fails = []
    for m in re.finditer(r'(\w+)\.(\w+)\s*\(|(?<![.\w])(\w+)\s*\(', masked):
        before = masked[max(0,m.start()-80):m.start()]
        recv, fn = m.group(1), m.group(2)
        if recv is None:
            recv = m.group(3); fn = recv
            recv_type = None
        else:
            recv_type = 'App' if recv == 'a' else recv  # 仅精确匹配 a.x；其他接收者视为其他类型
        # 跳过定义处（before 截止于函数名之前，故只看结尾是否为 func 声明头）
        if re.search(r'func\s+(?:\(\w+\s+\*?\w+\)\s+)?$', before): continue
        if fn in ('if','for','switch','func','return','range','go','defer','len','cap','make','new','append','panic','recover','print','println','select','case','struct','map','interface','chan','var','const','type','import'): continue
        if fn in ('string','int','int64','float64','uint16','uint','bool','byte','rune'): continue
        key = (recv_type if recv_type else None, fn)
        if key not in defs:
            # 裸调用但该名字只是 App 方法 → 缺 a. 接收者（历史踩坑：go routine 内 a.ImportTrackInboxFolder()）
            if recv_type is None and ('App', fn) in defs:
                fails.append(f"  {name}:{line_of(src,m.start())} 裸调用 App 方法 {fn}()，缺 a. 接收者")
            continue
        defs_here = [d for d in defs[key] if d[1] in repo_files]
        if not defs_here: continue
        need = defs_here[0][0]
        got, _ = count_call_args(ph_masked, m.end()-1)
        if got >= 0 and got != need:
            fails.append(f"  {name}:{line_of(src,m.start())} {fn}() 实参 {got} 个，定义 {need} 个")
    return fails

def check_self_assign(name, src, masked):
    fails = []
    for m in re.finditer(r'(\w+(?:\s*,\s*\w+)+)\s*:=\s*([^;\n]+)', masked):
        lhs = [x.strip() for x in m.group(1).split(',')]
        rhs = [x.strip() for x in m.group(2).split(',')]
        if len(lhs) == len(rhs):
            for a,b in zip(lhs,rhs):
                if a==b:
                    fails.append(f"  {name}:{line_of(src,m.start())} 自赋值 {a} := ... {b}")
    return fails

def check_dup(name, src, masked):
    fails=[]; seen={}
    for m in re.finditer(r'func\s+(?:\(\w+\s+\*?\w+\)\s+)?(\w+)\s*\(', masked):
        fn=m.group(1)
        if fn=='main': continue
        if fn in seen: fails.append(f"  {name}:{line_of(src,m.start())} {fn} 重复定义（{seen[fn]}）")
        else: seen[fn]=line_of(src,m.start())
    return fails


def check_undefined_refs(name, src, masked, defs, repo_files):
    """类型/函数被引用但无定义（含struct类型）——trackTxtLine事故盲区。
    类型收集范围：repo_files + backend/models/*.go（同包模型）"""
    fails = []
    types = set()
    scan_files = list(repo_files)
    import glob
    scan_files += [f for f in glob.glob('backend/models/*.go')]
    for f in scan_files:
        try: s = open(f, encoding='utf-8').read()
        except: continue
        m2 = chk_strip(s)
        types |= set(re.findall(r'type\s+(\w+)\s+struct', m2))
        types |= set(re.findall(r'type\s+(\w+)\s+\[\]', m2))
        types |= set(re.findall(r'type\s+(\w+)\s+(?:int|uint|int64|float64|string|bool|rune|byte)\b', m2))
    # struct字面量引用：TrackItem{ / trackTxtLine{ —— 类型名后紧跟{
    for m in re.finditer(r'\b([a-z]\w*)\{', masked):
        tn = m.group(1)
        if tn in ('map','if','for','switch','struct'): continue
        if tn not in types:
            # 可能是复合字面量如 map[string]string{...} —— 排除
            before = masked[max(0,m.start()-30):m.start()]
            if re.search(r'map\[[^\]]*\]\s*$', before) or re.search(r'\[[\d*]*\]\s*$', before): continue
            if re.search(r'\*\s*$', before): continue  # []*T{ / *T{
            if tn in ('float64','int','int64','uint','uint64','string','bool','byte','rune','float32','error','any'): continue
            fails.append(f"  {name}:{line_of(src,m.start())} struct字面量 {tn}{{...}} 但类型未定义")
    return fails

def chk_strip(src):
    return strip_strings_comments(src)

def main():
    files = sys.argv[1:]
    if not files: print("用法: ... 文件1.go ..."); sys.exit(2)
    srcs = {f: open(f, encoding='utf-8', errors='replace').read() for f in files}
    masked_all = {f: strip_strings_comments(s) for f,s in srcs.items()}
    ph_all = {f: strip_strings_placeholder(s) for f,s in srcs.items()}
    defs = collect_defs(files)
    all_fails = []
    for f in files:
        name = os.path.basename(f)
        all_fails += check_brackets(name, srcs[f], masked_all[f])
        all_fails += check_imports(name, srcs[f], masked_all[f])
        all_fails += check_self_assign(name, srcs[f], masked_all[f])
        all_fails += check_dup(name, srcs[f], masked_all[f])
        all_fails += check_calls(name, srcs[f], masked_all[f], ph_all[f], defs, set(files))
        all_fails += check_undefined_refs(name, srcs[f], masked_all[f], defs, set(files))
    if all_fails:
        print(f"FAIL 共 {len(all_fails)} 项:"); [print(x) for x in all_fails]; sys.exit(1)
    print(f"PASS 全部核查通过（{len(files)} 文件：括号/import/参数个数/自赋值/重复定义）")

if __name__ == '__main__':
    main()
