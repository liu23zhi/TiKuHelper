import datetime as dt
import sqlite3
import unittest
from decimal import Decimal

import mysql_compare_schema as compare
import mysql_fit_varchar as fit
import mysql_migration_common as common
import sqlite_preflight as preflight
import verify_sqlite_mysql_migration as verify


class DeclaredTypeTests(unittest.TestCase):
    def test_length_and_scale_parsing(self):
        cases = (
            ("varchar(512)", 512, None),
            ("VARCHAR(255) ", 255, None),
            ("char(64)", 64, None),
            ("decimal(18,6)", None, 6),
            ("DECIMAL(18,8)", None, 8),
            ("numeric", None, None),
            ("longtext", None, None),
            (None, None, None),
        )
        for declared, length, scale in cases:
            with self.subTest(declared=declared):
                self.assertEqual(common.declared_length(declared), length)
                self.assertEqual(common.declared_scale(declared), scale)

    def test_big_text_detection(self):
        self.assertTrue(common.is_big_text_type("text"))
        self.assertTrue(common.is_big_text_type("longtext"))
        self.assertFalse(common.is_big_text_type("varchar(255)"))

    def test_identifier_quoting_refuses_nul(self):
        self.assertEqual(common.quote_mysql("key"), "`key`")
        self.assertEqual(common.quote_mysql("a`b"), "`a``b`")
        with self.assertRaises(ValueError):
            common.quote_mysql("a\x00b")


class SqliteDatetimeParsingTests(unittest.TestCase):
    def test_nanoseconds_and_offsets_keep_wall_clock(self):
        parsed = common.parse_sqlite_datetime("2026-10-31 22:32:17.786635322+08:00")
        self.assertEqual(parsed, dt.datetime(2026, 10, 31, 22, 32, 17, 786635))
        utc_wall = common.parse_sqlite_datetime("2026-10-05 11:09:13.262373736+00:00")
        self.assertEqual(utc_wall, dt.datetime(2026, 10, 5, 11, 9, 13, 262373))

    def test_short_fraction_and_invalid_input(self):
        self.assertEqual(
            common.parse_sqlite_datetime("2026-10-04 03:57:18.75+00:00"),
            dt.datetime(2026, 10, 4, 3, 57, 18, 750000),
        )
        self.assertEqual(
            common.parse_sqlite_datetime("2026-10-04 03:57:18"),
            dt.datetime(2026, 10, 4, 3, 57, 18, 0),
        )
        self.assertIsNone(common.parse_sqlite_datetime("not a datetime"))
        self.assertIsNone(common.parse_sqlite_datetime(1234))


SAMPLE_DUMP_BEFORE = """CREATE TABLE `billing_records` (
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `question_text` longtext DEFAULT NULL,
  `amount` decimal(18,8) NOT NULL DEFAULT 0.00000000,
  PRIMARY KEY (`id`),
  KEY `idx_billing_records_question_text` (`question_text`(191))
) ENGINE=InnoDB AUTO_INCREMENT=26959 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
"""

SAMPLE_DUMP_AFTER_SAME = """CREATE TABLE `billing_records` (
  `amount` decimal(18,8) NOT NULL DEFAULT 0.00000000,
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `question_text` longtext DEFAULT NULL,
  KEY `idx_billing_records_question_text` (`question_text`(191)),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
"""

SAMPLE_DUMP_AFTER_NARROWED = """CREATE TABLE `billing_records` (
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `question_text` varchar(512) DEFAULT NULL,
  `amount` decimal(18,8) NOT NULL DEFAULT 0.00000000,
  PRIMARY KEY (`id`),
  KEY `idx_billing_records_question_text` (`question_text`(191))
) ENGINE=InnoDB AUTO_INCREMENT=5 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
"""


class SchemaCompareTests(unittest.TestCase):
    def test_definition_split_ignores_nested_commas(self):
        body = (
            "`id` bigint(20) NOT NULL AUTO_INCREMENT,\n"
            "`amount` decimal(18,8) NOT NULL DEFAULT 0.00000000,\n"
            "PRIMARY KEY (`id`),\n"
            "KEY `idx_a` (`id`, `amount`)"
        )
        parts = compare.split_definition_list(body)
        self.assertEqual(len(parts), 4)
        self.assertEqual(parts[1], "`amount` decimal(18,8) NOT NULL DEFAULT 0.00000000")
        self.assertEqual(parts[3], "KEY `idx_a` (`id`, `amount`)")

    def test_definition_classification(self):
        self.assertEqual(compare.classify_definition("`id` bigint(20) NOT NULL"), "column")
        self.assertEqual(compare.classify_definition("PRIMARY KEY (`id`)"), "key")
        self.assertEqual(compare.classify_definition("UNIQUE KEY `u` (`a`)"), "key")
        self.assertEqual(compare.classify_definition("CONSTRAINT `c` CHECK (1)"), "other")

    def test_identical_structure_ignores_order_and_auto_increment(self):
        before = compare.parse_schema_dump(SAMPLE_DUMP_BEFORE)
        after = compare.parse_schema_dump(SAMPLE_DUMP_AFTER_SAME)
        self.assertEqual(compare.diff_schema_dumps(before, after), [])

    def test_column_type_change_is_reported(self):
        before = compare.parse_schema_dump(SAMPLE_DUMP_BEFORE)
        after = compare.parse_schema_dump(SAMPLE_DUMP_AFTER_NARROWED)
        differences = compare.diff_schema_dumps(before, after)
        self.assertEqual(len(differences), 1)
        self.assertEqual(differences[0]["kind"], "column_changed")
        self.assertIn("question_text", differences[0]["detail"])

    def test_missing_and_added_tables_are_reported(self):
        before = compare.parse_schema_dump(SAMPLE_DUMP_BEFORE)
        self.assertEqual(
            sorted(item["kind"] for item in compare.diff_schema_dumps(before, {})), ["table_dropped"]
        )
        self.assertEqual(
            sorted(item["kind"] for item in compare.diff_schema_dumps({}, before)), ["table_added"]
        )

    def test_unparsable_dump_is_rejected(self):
        with self.assertRaises(ValueError):
            compare.parse_schema_dump("not a dump")


class CanonicalValueTests(unittest.TestCase):
    def test_datetime_is_truncated_to_microseconds(self):
        value = verify.canonical_value("2026-10-31 22:32:17.786635322+08:00", "datetime")
        self.assertEqual(value, "2026-10-31 22:32:17.786635")
        from_mysql = verify.canonical_value(dt.datetime(2026, 10, 31, 22, 32, 17, 786635), "datetime(3)")
        self.assertEqual(from_mysql, "2026-10-31 22:32:17.786635")

    def test_decimal_uses_declared_scale(self):
        self.assertEqual(verify.canonical_value(0.1 + 0.2, "decimal(18,6)"), "0.300000")
        self.assertEqual(verify.canonical_value(Decimal("49"), "decimal(18,8)"), "49.00000000")

    def test_none_boolean_and_bytes(self):
        self.assertEqual(verify.canonical_value(None, "text"), "NULL")
        self.assertEqual(verify.canonical_value(1.0, "numeric"), "1")
        self.assertEqual(verify.canonical_value(0.5, "numeric"), "0.5")
        self.assertEqual(
            verify.canonical_value(memoryview(b"abc"), "blob"),
            "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
        )

    def test_row_digest_is_order_sensitive(self):
        self.assertNotEqual(verify.row_digest(["a", "b"]), verify.row_digest(["b", "a"]))
        self.assertEqual(verify.row_digest(["a"]), verify.row_digest(["a"]))


class DatetimePrecisionTests(unittest.TestCase):
    COLUMN = {"name": "created_at", "type": "datetime", "pk": 0}

    def test_canonical_truncates_to_target_precision(self):
        value = "2026-10-04 03:52:31.433701963+00:00"
        self.assertEqual(verify.canonical_value(value, "datetime", 3), "2026-10-04 03:52:31.433")
        self.assertEqual(verify.canonical_value(value, "datetime", 6), "2026-10-04 03:52:31.433701")
        # 精度 0（logs.create_time 就是 datetime 无小数位）不能留一个小数点
        self.assertEqual(verify.canonical_value(value, "datetime", 0), "2026-10-04 03:52:31")

    def test_values_equal_allows_one_tick_only(self):
        self.assertTrue(verify.values_equal(self.COLUMN, "2026-10-04 03:52:31.433", "2026-10-04 03:52:31.434", 3))
        self.assertFalse(verify.values_equal(self.COLUMN, "2026-10-04 03:52:31.433", "2026-10-04 03:52:31.435", 3))
        self.assertFalse(verify.values_equal(self.COLUMN, "2026-10-04 03:52:31.433", "2026-10-04 03:52:32.433", 3))

    def test_column_precision_prefers_override(self):
        self.assertEqual(verify.column_precision(self.COLUMN, {"created_at": 3}, None), 3)
        self.assertEqual(verify.column_precision(self.COLUMN, {"created_at": 3}, 6), 6)
        self.assertEqual(verify.column_precision(self.COLUMN, {}, None), 6)
        self.assertIsNone(verify.column_precision({"name": "note", "type": "varchar(5)", "pk": 0}, {}, None))


class SamplingAndSpecTests(unittest.TestCase):
    def test_sample_stride(self):
        self.assertEqual(verify.sample_stride(10, 5000), 1)
        self.assertEqual(verify.sample_stride(146725, 5000), 29)
        self.assertEqual(verify.sample_stride(5, 0), 1)

    def test_truncation_specs(self):
        parsed = verify.parse_truncation_specs(["billing_records.question_text=512"])
        self.assertEqual(parsed[("billing_records", "question_text")], 512)
        with self.assertRaises(SystemExit):
            verify.parse_truncation_specs(["billing_records=512"])


class FitVarcharTests(unittest.TestCase):
    def test_limit_guard(self):
        self.assertIsNone(fit.normalize_limit(None))
        self.assertEqual(fit.normalize_limit(512), 512)
        with self.assertRaises(ValueError):
            fit.normalize_limit(0)

    def test_modify_type_only_for_bounded_columns(self):
        self.assertEqual(fit.build_modify_type("varchar(512)", 512), "varchar(512)")
        self.assertEqual(fit.build_modify_type("char(64)", 64), "char(64)")
        self.assertIsNone(fit.build_modify_type("longtext", 512))


class PreflightQueryTests(unittest.TestCase):
    def test_text_length_and_not_null_checks(self):
        connection = sqlite3.connect(":memory:")
        self.addCleanup(connection.close)
        connection.execute("CREATE TABLE sample (id INTEGER PRIMARY KEY, note varchar(5), flag numeric NOT NULL)")
        connection.executemany(
            "INSERT INTO sample (id, note, flag) VALUES (?, ?, ?)",
            ((1, "abcdefgh", 1), (2, "x", 0), (3, None, 2)),
        )
        columns = preflight.describe_table(connection, "sample")
        lengths = preflight.max_text_lengths(connection, "sample", columns)
        self.assertEqual(lengths["note"], 8)
        self.assertEqual(preflight.notnull_violations(connection, "sample", columns), {})
        outliers = preflight.numeric_outliers(connection, "sample", columns)
        self.assertEqual(outliers, {"flag": 1})

    def test_table_listing_skips_internal_tables(self):
        connection = sqlite3.connect(":memory:")
        self.addCleanup(connection.close)
        connection.execute("CREATE TABLE tiku (id INTEGER PRIMARY KEY AUTOINCREMENT)")
        connection.execute("INSERT INTO tiku DEFAULT VALUES")
        # AUTOINCREMENT 会自动建出 sqlite_sequence，迁移脚本与预检都必须跳过它。
        internal = [
            row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE name = 'sqlite_sequence'")
        ]
        self.assertEqual(internal, ["sqlite_sequence"])
        self.assertEqual(preflight.list_tables(connection), ["tiku"])


if __name__ == "__main__":
    unittest.main()
