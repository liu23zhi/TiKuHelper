"""coverage-report.py 的契约测试（不跑 go test，只用固定 fixture）。

这些测试钉住的是「口径」本身，不是覆盖率数字：

  1. profile 解析遇到脏行必须报错，不能静默少算几行（分母悄悄变小 = 门禁失效）；
  2. 汇总必须**按语句加权**，不是对文件百分比取算术平均；
  3. 进产物 / 未进产物必须分组，未进产物的包不能进分母；
  4. 「进产物但 profile 里没有」必须显式列出（测试二进制异常退出 ≠ 0%）；
  5. 默认 report-only：有基线下降也返回 0；--strict 才返回 1；
  6. 输入不可用返回 2，与「覆盖率不达标」的 1 区分开。
"""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location('coverage_report', Path(__file__).with_name('coverage-report.py'))
report = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(report)

MODULE = 'github.com/itihey/tikuAdapter'
PROFILE_HEADER = 'mode: atomic'

# 两个包，故意让「语句加权」与「文件平均」给出不同答案：
#   big   : 90/100 条覆盖（两个块：90 条命中、10 条未命中）
#   small : 1/1   条覆盖
# 语句加权 = 91/101 ≈ 90.10%；文件平均 = (90.0 + 100.0)/2 = 95.0%
# 两者差近 5 个百分点，所以断言「按语句加权」时这个 fixture 能真的分辨出来。
PROFILE = '\n'.join([
    PROFILE_HEADER,
    f'{MODULE}/internal/big/a.go:1.1,2.2 90 7',
    f'{MODULE}/internal/big/a.go:3.1,4.2 10 0',
    f'{MODULE}/internal/small/b.go:1.1,2.2 1 1',
    f'{MODULE}/internal/nocov/c.go:1.1,2.2 10 0',
    f'{MODULE}/cmd/tool/d.go:1.1,2.2 5 0',
    '',
])

SHIPPED = (f'{MODULE}/internal/big\n{MODULE}/internal/small\n{MODULE}/internal/nocov\n'
           f'{MODULE}/cmd/adapter-service|notests\n')


def write(directory, name, text):
    path = Path(directory) / name
    path.write_text(text, encoding='utf-8')
    return path


class ProfileParsing(unittest.TestCase):
    def test_parses_statement_counts_and_skips_mode_header(self):
        rows = report.parse_profile(PROFILE)
        self.assertEqual(len(rows), 5)
        self.assertEqual(rows[0]['package'], f'{MODULE}/internal/big')
        self.assertEqual(rows[0]['stmts'], 90)
        # 计数 > 0 表示整块语句都被执行过，因此 covered == stmts
        self.assertEqual(rows[0]['covered'], 90)

    def test_zero_count_is_not_covered(self):
        rows = report.parse_profile(PROFILE)
        nocov = [row for row in rows if row['package'].endswith('nocov')][0]
        self.assertEqual((nocov['stmts'], nocov['covered']), (10, 0))

    def test_malformed_line_fails_closed(self):
        # 少解析一行就会让分母变小，所以脏行必须报错而不是跳过。
        broken = '\n'.join([PROFILE_HEADER, f'{MODULE}/internal/big/a.go:1.1,2.2 100 90', 'garbage-line', ''])
        with self.assertRaises(report.CoverageInputError):
            report.parse_profile(broken)

    def test_empty_profile_is_an_input_error(self):
        for text in ['', PROFILE_HEADER, PROFILE_HEADER + '\n']:
            with self.subTest(text=text), self.assertRaises(report.CoverageInputError):
                report.parse_profile(text)


class Aggregation(unittest.TestCase):
    def test_totals_are_statement_weighted_not_file_average(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped = {f'{MODULE}/internal/big', f'{MODULE}/internal/small'}
        result = report.build_report(packages, shipped, set(), {}, 1.0)
        # 91/101，而不是文件平均的 95.0
        self.assertAlmostEqual(result['gatedTotals']['percent'], 100.0 * 91 / 101, places=6)
        self.assertEqual(result['gatedTotals']['stmts'], 101)

    def test_unshipped_packages_are_excluded_from_gated_totals(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped = {f'{MODULE}/internal/big'}
        result = report.build_report(packages, shipped, set(), {}, 1.0)
        self.assertEqual([e['package'] for e in result['gated']], ['internal/big'])
        self.assertEqual(sorted(e['package'] for e in result['other']), ['cmd/tool', 'internal/nocov', 'internal/small'])
        # 全仓总计仍然包含它们，只是不进小计
        self.assertEqual(result['allTotals']['stmts'], 116)

    def test_shipped_package_missing_from_profile_is_reported_not_treated_as_zero(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        # internal/registry 进了产物但 profile 里没有 → 必须被单独列出
        shipped = {f'{MODULE}/internal/big', f'{MODULE}/internal/registry'}
        result = report.build_report(packages, shipped, set(), {}, 1.0)
        self.assertEqual(result['missingUnknown'], ['internal/registry'])
        # 且不能被当成 0% 混进小计（混进去会把 90.0% 拉到 45%）
        self.assertNotIn('internal/registry', [e['package'] for e in result['gated']])
        self.assertAlmostEqual(result['gatedTotals']['percent'], 90.0, places=6)

    def test_no_test_file_packages_are_separated_from_crash(self):
        # 有 |notests 标记的包「本来就没有覆盖率数据」，是常态；
        # 没有标记却缺席的包是「测试二进制异常退出」，是故障。两者不能混。
        shipped, no_tests = report.load_shipped_text(SHIPPED)
        self.assertIn(f'{MODULE}/cmd/adapter-service', no_tests)
        packages = report.aggregate(report.parse_profile(PROFILE))
        result = report.build_report(packages, shipped, no_tests, {}, 1.0)
        self.assertIn('cmd/adapter-service', result['missingTests'])
        self.assertEqual(result['missingCrash'], [])

    def test_crash_detection_requires_evidence_from_test_log(self):
        # 只有 go test 明确报 FAIL 才归为 crash；没有证据就是 unknown（见 TestLogClassification）。
        shipped, no_tests = report.load_shipped_text(
            f'{MODULE}/internal/big\n{MODULE}/internal/registry\n')
        self.assertEqual(no_tests, set())
        result = report.build_report(report.aggregate(report.parse_profile(PROFILE)),
                                     shipped, no_tests, {}, 1.0)
        self.assertEqual(result['missingCrash'], [])
        self.assertEqual(result['missingUnknown'], ['internal/registry'])

    def test_baseline_delta_and_drop_detection(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped = {f'{MODULE}/internal/big'}
        baseline = {'packages': {'internal/big': 95.0}}
        result = report.build_report(packages, shipped, set(), baseline, 1.0)
        entry = result['gated'][0]
        self.assertAlmostEqual(entry['baseline'], 95.0)
        self.assertAlmostEqual(entry['delta'], 90.0 - 95.0, places=6)
        self.assertEqual([d['package'] for d in result['drops']], ['internal/big'])

    def test_small_drift_inside_tolerance_is_not_a_drop(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped = {f'{MODULE}/internal/big'}
        result = report.build_report(packages, shipped, set(), {'packages': {'internal/big': 90.5}}, 1.0)
        self.assertEqual(result['drops'], [])

    def test_empty_shipped_set_is_an_input_error(self):
        with self.assertRaises(report.CoverageInputError):
            report.load_shipped_text('\n\n')


class TestLogClassification(unittest.TestCase):
    """profile 里「缺席」有四种原因，只有一种算故障。"""

    LOG = '\n'.join([
        f'ok  \t{MODULE}/internal/big\t0.5s\tcoverage: 72.3% of statements',
        f'ok  \t{MODULE}/pkg/model\t0.1s\tcoverage: [no statements]',
        f'?\t{MODULE}/configs\t[no test files]',
        f'FAIL\t{MODULE}/internal/registry\t434.216s',
        '--- FAIL: TestSomething (0.01s)',
        'FAIL',
        '',
    ])

    def test_classifies_each_package_status(self):
        results = report.parse_test_results(self.LOG)
        self.assertEqual(results[f'{MODULE}/internal/big'], 'covered')
        self.assertEqual(results[f'{MODULE}/pkg/model'], 'no-statements')
        self.assertEqual(results[f'{MODULE}/configs'], 'no-test-files')
        self.assertEqual(results[f'{MODULE}/internal/registry'], 'failed')

    def test_ignores_third_party_and_noise_lines(self):
        results = report.parse_test_results(self.LOG)
        self.assertEqual(set(results), {
            f'{MODULE}/internal/big', f'{MODULE}/pkg/model',
            f'{MODULE}/configs', f'{MODULE}/internal/registry',
        })

    def test_missing_package_with_no_evidence_is_unknown_not_no_tests(self):
        # 日志被截断时不能替它下"没有测试"的结论，也不能说它崩了。
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped = {f'{MODULE}/internal/big', f'{MODULE}/internal/registry'}
        result = report.build_report(packages, shipped, set(), {}, 1.0, {})
        self.assertEqual(result['missingUnknown'], ['internal/registry'])
        self.assertEqual(result['missingTests'], [])
        self.assertEqual(result['missingCrash'], [])

    def test_failed_package_is_crash_and_no_statements_is_not(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped = {f'{MODULE}/internal/big', f'{MODULE}/internal/registry', f'{MODULE}/pkg/model'}
        results = report.parse_test_results(self.LOG)
        result = report.build_report(packages, shipped, set(), {}, 1.0, results)
        self.assertEqual(result['missingCrash'], ['internal/registry'])
        self.assertEqual(result['missingNoStatements'], ['pkg/model'])

    def test_go_list_notests_marker_is_used_when_log_has_no_row(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped = {f'{MODULE}/internal/big', f'{MODULE}/configs'}
        result = report.build_report(packages, shipped, {f'{MODULE}/configs'}, {}, 1.0, {})
        self.assertEqual(result['missingTests'], ['configs'])
        self.assertEqual(result['missingUnknown'], [])


class Renderers(unittest.TestCase):
    def setUp(self):
        packages = report.aggregate(report.parse_profile(PROFILE))
        # 额外塞一个「有测试但 profile 缺席」的包，用来验证 WARN 段
        shipped = {f'{MODULE}/internal/big', f'{MODULE}/internal/registry'}
        self.result = report.build_report(packages, shipped, set(), {}, 1.0)

    def test_console_renders_totals_and_missing_warning(self):
        text = report.render_console(self.result)
        self.assertIn('进产物小计', text)
        self.assertIn('WARN', text)
        self.assertIn('internal/registry', text)

    def test_markdown_has_table_and_scope_note(self):
        text = report.render_markdown(self.result)
        self.assertIn('| 包 | 语句 |', text)
        self.assertIn('`internal/big`', text)
        self.assertIn('进产物小计', text)
        self.assertIn('WARN', text)

    def test_no_test_packages_render_without_warn(self):
        # 没有测试文件的包不该产生 WARN —— 否则 WARN 天天响就没人看
        packages = report.aggregate(report.parse_profile(PROFILE))
        shipped, no_tests = report.load_shipped_text(SHIPPED)
        result = report.build_report(packages, shipped, no_tests, {}, 1.0)
        text = report.render_console(result)
        self.assertNotIn('WARN', text)
        self.assertIn('没有测试文件', text)


class ExitCodes(unittest.TestCase):
    """默认 report-only；--strict 才阻断；输入不可用是 2，不是 1。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = self.tmp.name
        self.profile = write(self.dir, 'coverage.out', PROFILE)
        self.shipped = write(self.dir, 'shipped.txt', SHIPPED)
        self.baseline = write(self.dir, 'baseline.json',
                              json.dumps({'packages': {'internal/big': 99.0}}))

    def run_tool(self, *extra):
        argv = [str(self.profile), '--shipped', str(self.shipped), '--baseline', str(self.baseline), *extra]
        return report.main(argv)

    def test_default_is_report_only_even_when_baseline_drops(self):
        self.assertEqual(self.run_tool(), 0)

    def test_strict_exits_one_on_drop(self):
        self.assertEqual(self.run_tool('--strict'), 1)

    def test_strict_without_drop_exits_zero(self):
        write(self.dir, 'baseline.json', json.dumps({'packages': {'internal/big': 10.0}}))
        self.assertEqual(self.run_tool('--strict'), 0)

    def test_missing_profile_is_exit_two_not_one(self):
        self.profile.unlink()
        self.assertEqual(self.run_tool(), 2)

    def test_summary_appends_markdown(self):
        summary = Path(self.dir) / 'summary.md'
        summary.write_text('# 已有内容\n', encoding='utf-8')
        self.assertEqual(self.run_tool('--summary', str(summary)), 0)
        text = summary.read_text(encoding='utf-8')
        self.assertTrue(text.startswith('# 已有内容\n'))  # 追加，不覆盖
        self.assertIn('## Go 覆盖率', text)


class ShippedListFromFile(unittest.TestCase):
    def test_ignores_non_module_and_blank_lines(self):
        with tempfile.TemporaryDirectory() as directory:
            path = write(directory, 'shipped.txt', f'\n{MODULE}/internal/big\n\nfmt\nos\n')
            shipped, no_tests = report.load_shipped_text(path.read_text(encoding='utf-8'))
            self.assertEqual(shipped, {f'{MODULE}/internal/big'})
            self.assertEqual(no_tests, set())

    def test_notests_suffix_is_parsed_and_not_leaked_into_path(self):
        shipped, no_tests = report.load_shipped_text(
            f'{MODULE}/internal/big\n{MODULE}/pkg/errors|notests\n')
        # 后缀必须被剥掉，否则包名会带 |notests 永远匹配不上 profile
        self.assertEqual(shipped, {f'{MODULE}/internal/big', f'{MODULE}/pkg/errors'})
        self.assertEqual(no_tests, {f'{MODULE}/pkg/errors'})


if __name__ == '__main__':
    unittest.main()
