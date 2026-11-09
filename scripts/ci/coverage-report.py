"""Go 覆盖率统计与上报（默认不阻断）。

口径说明（很重要，别当成"全仓覆盖率"）：

  A. **只对真正进产物的包负责。**
     本仓库的覆盖率统计基于 `go test -coverprofile`。它会给出 `./...` 下所有包，
     但其中 `cmd/gorm-gen`、`scripts/ci/s04-fixture` 这类开发期工具根本不链接进
     `cmd/adapter-service`，把它们算进分母等于用一个不会上线的分母给自己打折。
     因此本脚本用 `go list -deps ./cmd/adapter-service` 求出"真的进产物的包集合"，
     分「进产物」与「未进产物」两组分别汇总。

     这与 PLUS 侧 `scripts/ci/check-coverage.mjs` 的口径一致：那边只统计真正进
     构建产物的 `.ts`（`build.mjs` 的 resolve.extensions 把 `.ts` 排在 `.js` 前），
     不统计跑在 dist 里、Node 加载器看不到的 `src/projects/**`；这边同理，
     只统计真正链接进二进制的包。

  B. **语句加权，不是文件平均。**
     覆盖率是"被覆盖语句数 / 总语句数"。对文件覆盖率取算术平均会让一个 5 条语句的
     小文件和一个 5000 条语句的大文件同权，得出的数字没有工程含义。

  C. **默认 report-only，不设阈值阻断。**
     TIKU 当前有已知基线红（见 PR 正文），本仓库的 Go 测试还有若干"单条几百秒"的
     用例。阈值一上来就盯总数会天天红，红久了就没人看。所以本轮只统计 + 上报 +
     与基线比漂移，`--strict` 留给基线稳定之后再用。

  D. **包在 profile 里消失必须显式列出，而且要按证据分类。**
     `go test -coverprofile` 对**测试二进制异常退出**的包不会写出覆盖率（不是 0%，
     是整包消失）。若静默跳过，分母会自己变小、门禁越来越容易过，而看起来一切正常。
     但"缺席"有四种原因，只有一种算故障：
       `[no test files]`  常态 —— 本来就没有测试
       `[no statements]`  常态 —— 包内只有类型/常量
       `FAIL` 且无覆盖率行  **故障** —— 测试二进制异常退出
       日志里查不到该包     unknown —— 需人工确认（可能是日志被截断）
     不区分就会把"本来没有覆盖率"的包当成故障，WARN 天天响，最后没人看。

用法：

  python3 scripts/ci/coverage-report.py coverage.out
  python3 scripts/ci/coverage-report.py coverage.out --test-log coverage-test.log \\
      --baseline scripts/ci/coverage-baseline.json --summary "$GITHUB_STEP_SUMMARY"

退出码：

  0  统计完成（含覆盖率下降；report-only 语义）
  1  仅 `--strict` 且检出超阈值下降 / 低于阈值时
  2  输入不可用（profile 缺失、解析为空、shipped 集合为空）—— 这是基础设施错误，
     不是覆盖率不达标，必须与"覆盖率低"区分开。
"""

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

# coverage profile 行格式：
#   github.com/x/y/file.go:15.92,37.60 5 24
# 字段：文件:起始行.起始列,结束行.结束列 语句数 命中次数
PROFILE_ROW = re.compile(r'^(?P<file>.+?):(?P<sl>\d+)\.(?P<sc>\d+),(?P<el>\d+)\.(?P<ec>\d+) (?P<stmts>\d+) (?P<count>\d+)$')

# go test 输出里的包结果行：
#   ok   <pkg>  0.1s  coverage: 72.3% of statements
#   ok   <pkg>  0.1s  coverage: [no statements]      ← 包内只有类型/常量，永远没有覆盖率
#   ?    <pkg>  [no test files]                      ← 根本没有测试
TEST_RESULT_ROW = re.compile(r'^(?P<status>ok|FAIL|\?)\s+(?P<package>\S+)(?P<rest>.*)$')
NO_STATEMENTS = '[no statements]'
NO_TEST_FILES = '[no test files]'

SHIPPED_ENTRY = './cmd/adapter-service'
MODULE_PREFIX = 'github.com/itihey/tikuAdapter/'


class CoverageInputError(Exception):
    """输入不可用：profile 缺失 / 解析为空 / shipped 集合为空。"""


def parse_profile(text):
    """解析 coverage profile，返回 [(package, stmts, covered)] 与文件明细。

    跳过 `mode:` 头行；任何一行不符合 profile 语法即视为输入损坏（fail closed），
    因为"少解析几行"会让分母悄悄变小。
    """
    rows = []
    for lineno, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not line:
            continue
        if line.startswith('mode:'):
            continue
        match = PROFILE_ROW.match(line)
        if not match:
            raise CoverageInputError(f'coverage profile 第 {lineno} 行无法解析：{raw!r}')
        stmts = int(match.group('stmts'))
        count = int(match.group('count'))
        rows.append({
            'file': match.group('file'),
            'package': match.group('file').rsplit('/', 1)[0],
            'stmts': stmts,
            'covered': stmts if count > 0 else 0,
        })
    if not rows:
        raise CoverageInputError('coverage profile 解析结果为空（只有 mode 头行？）')
    return rows


def parse_test_results(text):
    """从 go test 输出里取出每个包的状态。

    返回 {import path: 'covered' | 'no-statements' | 'no-test-files' | 'failed'}。

    为什么需要它：profile 里**缺席**有四种完全不同的原因，只有一种算故障：
      - `[no test files]`   → 常态，本来就没有测试
      - `[no statements]`   → 常态，包内只有类型/常量，没有可覆盖语句
      - `FAIL` 且无覆盖率行 → **故障**：测试二进制异常退出（如 GOCOVERDIR 写不进、
                              os.Exit、panic 在写 profile 之前），不是 0%
      - `ok` 且有覆盖率行   → 正常
    不区分就会把「本来没有覆盖率」的包当成故障，WARN 天天响，最后没人看。
    """
    results = {}
    for raw in text.splitlines():
        match = TEST_RESULT_ROW.match(raw.strip())
        if not match:
            continue
        status = match.group('status')
        package = match.group('package')
        if not package.startswith(MODULE_PREFIX):
            continue
        rest = match.group('rest')
        if NO_TEST_FILES in rest:
            results[package] = 'no-test-files'
        elif NO_STATEMENTS in rest:
            results[package] = 'no-statements'
        elif status == 'ok' and 'coverage:' in rest:
            results[package] = 'covered'
        elif status == 'FAIL':
            results[package] = 'failed'
        # 其他（ok 但没写 coverage:）不归类，交给「profile 里有没有」判断
    return results


def aggregate(rows):
    """按包聚合语句数与已覆盖语句数。"""
    packages = {}
    for row in rows:
        bucket = packages.setdefault(row['package'], {'stmts': 0, 'covered': 0})
        bucket['stmts'] += row['stmts']
        bucket['covered'] += row['covered']
    return packages


def percent(covered, stmts):
    if stmts <= 0:
        return None
    return 100.0 * covered / stmts


def load_shipped_text(text):
    """从「每行一个 import path」的文本里取出本模块的包集合。

    每行可选后缀 `|notests`，表示该包**根本没有测试文件**（`go test` 会报
    `[no test files]`，因此永远不会有覆盖率数据）。这类包必须与「有测试但
    测试二进制异常退出」区分开：前者是常态，后者是故障，混在一起会让 WARN
    天天响、响到没人看。

    非本模块的行（标准库、第三方）与空行直接忽略；结果为空视为输入不可用 ——
    空集合会让所有包都落进「未进产物」，小计变成空，看起来像"没有可统计的包"。
    """
    shipped, no_tests = set(), set()
    for raw in text.splitlines():
        line = raw.strip()
        if not line or not line.startswith(MODULE_PREFIX):
            continue
        path, _, suffix = line.partition('|')
        path = path.strip()
        if not path.startswith(MODULE_PREFIX):
            continue
        shipped.add(path)
        if suffix.strip() == 'notests':
            no_tests.add(path)
    if not shipped:
        raise CoverageInputError('进产物的包集合为空：没有解析到任何本模块包')
    return shipped, no_tests


def load_shipped(path):
    """读取"进产物的包"集合与「无测试文件」子集。

    path 为 None 时现场执行 go list（CI 环境有 Go）；否则读文件，
    便于测试用固定 fixture 复现，不依赖本机 Go 版本。
    """
    if path is None:
        proc = subprocess.run(
            ['go', 'list', '-deps', '-f',
             '{{.ImportPath}}|{{if or .TestGoFiles .XTestGoFiles}}tests{{else}}notests{{end}}',
             SHIPPED_ENTRY],
            capture_output=True, text=True, check=False,
        )
        if proc.returncode != 0:
            raise CoverageInputError(f'go list 失败（rc={proc.returncode}）：{proc.stderr.strip()}')
        return load_shipped_text(proc.stdout)
    return load_shipped_text(Path(path).read_text(encoding='utf-8'))


def load_baseline(path):
    if not path:
        return {}
    baseline_path = Path(path)
    if not baseline_path.exists():
        return {}
    data = json.loads(baseline_path.read_text(encoding='utf-8'))
    return data if isinstance(data, dict) else {}


def short_name(package):
    return package[len(MODULE_PREFIX):] if package.startswith(MODULE_PREFIX) else package


def build_report(packages, shipped, no_tests, baseline, max_drop, test_results=None):
    """产出结构化报告：分「进产物」/「未进产物」两组，附基线漂移。"""
    test_results = test_results or {}
    baseline_packages = baseline.get('packages', {}) if isinstance(baseline, dict) else {}
    gated, other = [], []

    for package, bucket in sorted(packages.items()):
        name = short_name(package)
        pct = percent(bucket['covered'], bucket['stmts'])
        base = baseline_packages.get(name)
        entry = {
            'package': name,
            'stmts': bucket['stmts'],
            'covered': bucket['covered'],
            'percent': pct,
            'baseline': base,
            'delta': (pct - base) if (pct is not None and isinstance(base, (int, float))) else None,
            'shipped': package in shipped,
        }
        (gated if entry['shipped'] else other).append(entry)

    # 进产物但 profile 里完全没有的包。必须按证据区分，不能猜：
    #   FAIL 且无覆盖率行   → 故障：测试二进制异常退出，不是 0%
    #   [no statements]     → 常态（包内只有类型/常量）
    #   [no test files]     → 常态；go list 也确认没有测试文件时同样归此类
    #   完全没有证据        → unknown：可能是日志被截断，也可能是崩溃。
    #                         宁可标成"不知道"让人去看，也不能当成"本来就没有"。
    present = {short_name(p) for p in packages}
    missing_tests, missing_no_statements, missing_crash, missing_unknown = [], [], [], []
    for package in shipped:
        name = short_name(package)
        if name in present:
            continue
        status = test_results.get(package)
        if status == 'failed':
            missing_crash.append(name)
        elif status == 'no-statements':
            missing_no_statements.append(name)
        elif status == 'no-test-files' or package in no_tests:
            missing_tests.append(name)
        else:
            missing_unknown.append(name)
    missing_tests.sort()
    missing_no_statements.sort()
    missing_crash.sort()
    missing_unknown.sort()

    def totals(entries):
        stmts = sum(e['stmts'] for e in entries)
        covered = sum(e['covered'] for e in entries)
        return {'stmts': stmts, 'covered': covered, 'percent': percent(covered, stmts)}

    drops = [e for e in gated if e['delta'] is not None and e['delta'] < -max_drop]
    return {
        'gated': gated,
        'other': other,
        'missingTests': missing_tests,
        'missingNoStatements': missing_no_statements,
        'missingCrash': missing_crash,
        'missingUnknown': missing_unknown,
        'gatedTotals': totals(gated),
        'allTotals': totals(gated + other),
        'drops': drops,
    }


def render_console(report):
    lines = ['Go 覆盖率（口径：只统计真正进 cmd/adapter-service 的包，语句加权）', '']
    lines.append(f'  {"包":<40}{"语句":>7}{"已覆盖":>9}{"覆盖率":>9}{"基线":>9}{"变化":>9}')
    for entry in report['gated']:
        pct = '—' if entry['percent'] is None else f'{entry["percent"]:.1f}%'
        base = '—' if entry['baseline'] is None else f'{entry["baseline"]:.1f}%'
        delta = '' if entry['delta'] is None else f'{entry["delta"]:+.1f}pp'
        lines.append(f'  {entry["package"]:<40}{entry["stmts"]:>7}{entry["covered"]:>9}{pct:>9}{base:>9}{delta:>9}')

    gated = report['gatedTotals']
    allt = report['allTotals']
    lines.append('')
    lines.append(f'  进产物小计：{gated["covered"]} / {gated["stmts"]} 条语句'
                 f' = {gated["percent"]:.2f}%' if gated['percent'] is not None else '  进产物小计：无数据')
    if allt['percent'] is not None:
        lines.append(f'  全仓总计  ：{allt["covered"]} / {allt["stmts"]} 条语句 = {allt["percent"]:.2f}%')

    if report['other']:
        lines.append('')
        lines.append('  未进产物（不计入门禁，仅参考）：')
        for entry in report['other']:
            pct = '—' if entry['percent'] is None else f'{entry["percent"]:.1f}%'
            lines.append(f'    {entry["package"]:<38}{entry["stmts"]:>7} 条  {pct:>7}')

    if report['missingTests']:
        lines.append('')
        lines.append('  进产物但没有测试文件（常态，不计入分母）：')
        for name in report['missingTests']:
            lines.append(f'    {name}')

    if report['missingNoStatements']:
        lines.append('')
        lines.append('  进产物但包内没有可覆盖语句（常态，不计入分母）：')
        for name in report['missingNoStatements']:
            lines.append(f'    {name}')

    if report['missingCrash']:
        lines.append('')
        lines.append('  WARN 进产物、有测试、但 profile 里没有覆盖率数据（测试二进制异常退出，不是 0%）：')
        for name in report['missingCrash']:
            lines.append(f'    {name}')

    if report['missingUnknown']:
        lines.append('')
        lines.append('  WARN 进产物但 profile 里没有覆盖率数据，且日志里没有该包的结论（需人工确认，不是 0%）：')
        for name in report['missingUnknown']:
            lines.append(f'    {name}')

    if report['drops']:
        lines.append('')
        lines.append('  WARN 相对基线下降超过阈值（report-only，不阻断）：')
        for entry in report['drops']:
            lines.append(f'    {entry["package"]}: {entry["delta"]:+.1f}pp')
    return '\n'.join(lines)


def render_markdown(report):
    gated = report['gatedTotals']
    allt = report['allTotals']
    out = ['## Go 覆盖率', '']
    out.append('口径：只统计真正链接进 `cmd/adapter-service` 的包（`go list -deps`），'
               '按语句加权；与 PLUS 侧「只统计进构建产物的 `.ts`」对齐。')
    out.append('')
    out.append('| 包 | 语句 | 已覆盖 | 覆盖率 | 基线 | 变化 |')
    out.append('| --- | ---: | ---: | ---: | ---: | ---: |')
    for entry in report['gated']:
        pct = '—' if entry['percent'] is None else f'{entry["percent"]:.1f}%'
        base = '—' if entry['baseline'] is None else f'{entry["baseline"]:.1f}%'
        delta = '—' if entry['delta'] is None else f'{entry["delta"]:+.1f}pp'
        out.append(f'| `{entry["package"]}` | {entry["stmts"]} | {entry["covered"]} | {pct} | {base} | {delta} |')
    if gated['percent'] is not None:
        out.append(f'| **进产物小计** | **{gated["stmts"]}** | **{gated["covered"]}** | '
                   f'**{gated["percent"]:.2f}%** | | |')
    if allt['percent'] is not None:
        out.append(f'| 全仓总计（含未进产物的包） | {allt["stmts"]} | {allt["covered"]} | {allt["percent"]:.2f}% | | |')

    if report['other']:
        out.append('')
        out.append('未进产物、不计入门禁：' + '、'.join(f'`{e["package"]}`' for e in report['other']))

    if report['missingTests']:
        out.append('')
        out.append('进产物但没有测试文件（常态，不计入分母）：'
                   + '、'.join(f'`{name}`' for name in report['missingTests']))

    if report['missingNoStatements']:
        out.append('')
        out.append('进产物但包内没有可覆盖语句（常态，不计入分母）：'
                   + '、'.join(f'`{name}`' for name in report['missingNoStatements']))

    if report['missingCrash']:
        out.append('')
        out.append('> WARN 进产物、有测试、但 profile 里没有覆盖率数据（测试二进制异常退出，不是 0%）：'
                   + '、'.join(f'`{name}`' for name in report['missingCrash']))

    if report['missingUnknown']:
        out.append('')
        out.append('> WARN 进产物但 profile 里没有覆盖率数据，且日志里没有该包的结论（需人工确认，不是 0%）：'
                   + '、'.join(f'`{name}`' for name in report['missingUnknown']))

    if report['drops']:
        out.append('')
        out.append('> WARN 相对基线下降（report-only，不阻断）：'
                   + '、'.join(f'`{e["package"]}` {e["delta"]:+.1f}pp' for e in report['drops']))
    return '\n'.join(out)


def main(argv=None):
    parser = argparse.ArgumentParser(description='Go 覆盖率统计与上报（默认不阻断）')
    parser.add_argument('profile', help='go test -coverprofile 产出的 coverage.out')
    parser.add_argument('--shipped', default=None,
                        help='进产物的包清单文件（每行一个 import path）；缺省现场执行 go list')
    parser.add_argument('--baseline', default=None, help='基线 JSON，用于算漂移')
    parser.add_argument('--test-log', default=None,
                        help='go test 输出（用于区分 [no test files] / [no statements] / 异常退出）')
    parser.add_argument('--summary', default=None, help='把 markdown 追加写入该文件（CI 用 $GITHUB_STEP_SUMMARY）')
    parser.add_argument('--max-drop', type=float, default=1.0,
                        help='相对基线允许的下降百分点（默认 1.0）')
    parser.add_argument('--strict', action='store_true',
                        help='检出下降即退出 1；CI 默认不用（已知基线红）')
    args = parser.parse_args(argv)

    profile_path = Path(args.profile)

    try:
        if not profile_path.exists():
            raise CoverageInputError(f'找不到 coverage profile：{profile_path}')
        rows = parse_profile(profile_path.read_text(encoding='utf-8', errors='replace'))
        packages = aggregate(rows)
        shipped, no_tests = load_shipped(args.shipped)
        test_results = {}
        if args.test_log:
            test_log_path = Path(args.test_log)
            if test_log_path.exists():
                test_results = parse_test_results(
                    test_log_path.read_text(encoding='utf-8', errors='replace'))
            else:
                # 测试日志缺失不是致命错误：没有它也能统计，只是「profile 里缺席的包」
                # 只能归为 unknown 而不是精确分类。降级运行，不阻塞。
                print(f'注意：找不到 go test 输出 {test_log_path}，'
                      'profile 缺席的包将标记为「需人工确认」。', file=sys.stderr)
        baseline = load_baseline(args.baseline)
        report = build_report(packages, shipped, no_tests, baseline, args.max_drop, test_results)
    except CoverageInputError as exc:
        print(f'覆盖率统计失败：{exc}', file=sys.stderr)
        return 2

    print(render_console(report))
    if args.summary:
        # $GITHUB_STEP_SUMMARY 在本地跑时是空串，不能拿空路径去 open。
        with open(args.summary, 'a', encoding='utf-8') as handle:
            handle.write(render_markdown(report) + '\n')

    if args.strict and report['drops']:
        print('覆盖率门禁失败（--strict）：', file=sys.stderr)
        for entry in report['drops']:
            print(f'  {entry["package"]}: {entry["delta"]:+.1f}pp', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
