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
    def test_quota_normalization_repairs_invalid_values(self):
        cases = (
            (Decimal("0"), Decimal("0"), False),
            (Decimal("5.123456"), Decimal("5.123456"), False),
            (Decimal("999999999999.999999"), Decimal("999999999999.999999"), False),
            (Decimal("1.1111111111111111E+24"), Decimal(0), True),
            (Decimal("-2"), Decimal(0), True),
            (Decimal("-0.000001"), Decimal(0), True),
            (Decimal("-1000000000000"), Decimal(0), True),
            (Decimal("Infinity"), Decimal(0), True),
            (None, None, False),
        )
        for source, expected, corrected in cases:
            with self.subTest(source=source):
                value, changed = migration.normalize_access_token_quota_limit(source)
                self.assertEqual(value, expected)
                self.assertEqual(changed, corrected)

    def test_copy_table_normalizes_overflowing_access_token_quota(self):
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

        copied = migration.copy_table(source, target, "access_tokens", "stage_access_tokens", 10)

        self.assertEqual(copied, 3)
        self.assertEqual(target.inserted, [(1, Decimal(0)), (2, Decimal("5.25")), (3, Decimal(0))])
        self.assertEqual(target.commits, 1)

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

    def test_unbounded_sqlite_varchar_is_widened_for_mysql(self):
        self.assertEqual(
            migration.mysql_type({"name": "question_text", "type": "varchar(512)", "pk": 0}, set()),
            "LONGTEXT",
        )


if __name__ == "__main__":
    unittest.main()
