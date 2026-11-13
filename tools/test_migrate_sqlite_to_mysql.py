import sqlite3
import unittest
from decimal import Decimal

import migrate_sqlite_to_mysql as migration


class FakeCursor:
    def __init__(self, target):
        self.target = target
        self.last_result = None

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_value, traceback):
        return False

    def executemany(self, statement, rows):
        self.target.inserted.extend(rows)

    def execute(self, statement, params=None):
        self.last_result = None

    def fetchone(self):
        return self.last_result


class FakeTarget:
    def __init__(self):
        self.inserted = []
        self.commits = 0

    def cursor(self):
        return FakeCursor(self)

    def commit(self):
        self.commits += 1


class AccessTokenQuotaMigrationTests(unittest.TestCase):
    def test_quota_validation_rejects_lossy_values(self):
        cases = (
            (Decimal("0"), True),
            (Decimal("5.123456"), True),
            (Decimal("999999999999.999999"), True),
            (Decimal("1.1111111111111111E+24"), False),
            (Decimal("-2"), False),
            (Decimal("-0.000001"), False),
            (Decimal("-1000000000000"), False),
            (Decimal("Infinity"), False),
            (Decimal("0.00000001"), False),
            ("broken", False),
            (None, True),
        )
        for source, expected in cases:
            with self.subTest(source=source):
                self.assertEqual(migration.access_token_quota_is_representable(source), expected)

    def test_copy_table_rejects_overflowing_access_token_quota_without_modifying_source(self):
        source = sqlite3.connect(":memory:")
        self.addCleanup(source.close)
        source.execute(
            "CREATE TABLE access_tokens (id INTEGER PRIMARY KEY, total_quota_limit DECIMAL(18,6))"
        )
        source.executemany(
            "INSERT INTO access_tokens (id, total_quota_limit) VALUES (?, ?)",
            ((1, 1.1111111111111111e24), (2, 5.25), (3, -2)),
        )
        target = FakeTarget()

        before = source.execute("SELECT *, typeof(total_quota_limit) FROM access_tokens ORDER BY id").fetchall()
        with self.assertRaisesRegex(RuntimeError, "2 个值不能无损迁移"):
            migration.validate_access_token_quotas(source, ["access_tokens"])
        with self.assertRaisesRegex(RuntimeError, "不能无损迁移"):
            migration.copy_table(source, target, "access_tokens", "stage_access_tokens", 10)
        self.assertEqual(target.inserted, [])
        self.assertEqual(target.commits, 0)
        self.assertEqual(source.execute("SELECT *, typeof(total_quota_limit) FROM access_tokens ORDER BY id").fetchall(), before)

    def test_copy_table_preserves_valid_and_legacy_null_quotas(self):
        source = sqlite3.connect(":memory:")
        self.addCleanup(source.close)
        source.execute("CREATE TABLE access_tokens (id INTEGER PRIMARY KEY, total_quota_limit DECIMAL(18,6))")
        source.executemany("INSERT INTO access_tokens VALUES (?, ?)", [(1, 0), (2, 5.25), (3, None)])
        target = FakeTarget()
        migration.validate_access_token_quotas(source, ["access_tokens"])
        self.assertEqual(migration.copy_table(source, target, "access_tokens", "stage_access_tokens", 10), 3)
        self.assertEqual(target.inserted, [(1, Decimal(0)), (2, Decimal("5.25")), (3, None)])

    def test_other_columns_and_tables_are_not_normalized(self):
        source = sqlite3.connect(":memory:")
        self.addCleanup(source.close)
        source.execute("CREATE TABLE other_table (id INTEGER PRIMARY KEY, amount DECIMAL(18,6))")
        source.execute("INSERT INTO other_table (id, amount) VALUES (1, 1.1111111111111111e24)")
        target = FakeTarget()

        migration.copy_table(source, target, "other_table", "stage_other_table", 10)

        self.assertEqual(target.inserted, [(1, Decimal("1.1111111111111111E+24"))])

    def test_unscaled_numeric_columns_use_safe_mysql_type(self):
        self.assertEqual(
            migration.mysql_type({"name": "enabled", "type": "numeric", "pk": 0}, set()),
            "DOUBLE",
        )

    def test_negative_wallet_and_billing_balances_are_preserved(self):
        cases = (
            ("users", ("balance",), (-0.86,)),
            ("billing_records", ("balance_before", "balance_after"), (-0.76, -0.86)),
        )
        for table, columns, balances in cases:
            with self.subTest(table=table):
                source = sqlite3.connect(":memory:")
                self.addCleanup(source.close)
                fields = ", ".join(f"{name} DECIMAL(18,6)" for name in columns)
                source.execute(f"CREATE TABLE {table} (id INTEGER PRIMARY KEY, {fields})")
                placeholders = ", ".join("?" for _ in range(len(columns) + 1))
                source.execute(f"INSERT INTO {table} VALUES ({placeholders})", (1, *balances))
                target = FakeTarget()

                migration.copy_table(source, target, table, f"stage_{table}", 10)

                expected = (1, *(Decimal(str(value)) for value in balances))
                self.assertEqual(target.inserted, [expected])

    def test_bounded_sqlite_varchar_keeps_its_declared_length(self):
        # varchar(n) 在 MySQL/MariaDB 上本来就合法，照抄即可。
        #
        # 这条用例以前断言 varchar(512) → LONGTEXT（名字叫 "unbounded ... widened"，
        # 但 varchar(512) 明明是有界的）。那个断言把**错误映射固化成期望值**，
        # 于是真实缺陷被测试保护了起来：放大成 LONGTEXT 会让
        # sqlite_default_sql() 拒绝该列的默认值，直接把整个迁移闸掉。
        #
        # 实测（MariaDB 11.4.13）：
        #   varchar(512)/1024/4096 NOT NULL DEFAULT ''  建表成功且默认值生效
        #   varchar(16383)                              因 65535 行宽上限失败
        # 真实 schema 最宽是 varchar(1024)，离上限很远。
        for declared in ("varchar(512)", "varchar(1024)", "varchar(255)", "char(64)"):
            with self.subTest(declared=declared):
                self.assertEqual(
                    migration.mysql_type({"name": "question_text", "type": declared, "pk": 0}, set()),
                    declared.upper(),
                )

    def test_unbounded_sqlite_varchar_is_widened_for_mysql(self):
        # 只有裸 `varchar`（无长度）才需要放大：SQLite 允许，MySQL 必须有长度。
        self.assertEqual(
            migration.mysql_type({"name": "question_text", "type": "varchar", "pk": 0}, set()),
            "LONGTEXT",
        )
        self.assertEqual(
            migration.mysql_type({"name": "question_text", "type": "text", "pk": 0}, set()),
            "LONGTEXT",
        )


if __name__ == "__main__":
    unittest.main()
