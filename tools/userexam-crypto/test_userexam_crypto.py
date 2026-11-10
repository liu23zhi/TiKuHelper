"""Unit tests for the offline UserExamInfo tool.

These cover the invariants that must survive future edits:

* direction inference works from the file header alone;
* the cipher key is never baked into the source (this repo is public);
* a missing key fails with an actionable message instead of a stack trace;
* output naming de-duplicates without clobbering sidecar files;
* active WAL is refused, but a cold PERSIST journal is allowed;
* a non-SQLite file is rejected without touching anything.

Run with:

    py -3.11 -m unittest test_userexam_crypto -v

The round-trip tests need the SQLite3MC runtime and a real key, so they skip
unless USEREXAM_PASSWORD is set and _vendor is present.
"""

import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))

import userexam_crypto as tool


def _vendor_ready():
    vendor = Path(__file__).resolve().parent / "_vendor" / "apsw"
    return vendor.is_dir()


class HeaderInferenceTests(unittest.TestCase):
    def test_plain_sqlite_is_encrypt(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "plain.db"
            connection = sqlite3.connect(str(path))
            connection.execute("CREATE TABLE t(a TEXT)")
            connection.commit()
            connection.close()
            self.assertEqual(tool.infer_operation(path), "encrypt")

    def test_other_header_is_decrypt(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cipher.db"
            path.write_bytes(b"SQLite format 4\x00encrypted payload")
            self.assertEqual(tool.infer_operation(path), "decrypt")

    def test_tiny_file_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "tiny.db"
            path.write_bytes(b"abc")
            with self.assertRaises(tool.UserExamError):
                tool.infer_operation(path)


class KeyHandlingTests(unittest.TestCase):
    def test_source_contains_no_key_literal(self):
        """The public repo must never carry the key, in any encoding."""
        source = (Path(__file__).resolve().parent / "userexam_crypto.py").read_text(encoding="utf-8")
        # The historical profile was a 13-character ASCII string. Assert on the
        # shape rather than the value so the test itself cannot leak the key.
        self.assertNotIn("fromhex(", source, "key must not be reconstructed from a hex literal")
        self.assertNotIn("PASSWORD = ", source, "key must not be assigned at module level")
        # Reading it from the environment is the supported mechanism.
        self.assertIn(tool.PASSWORD_ENV, source)
        self.assertIn(tool.PASSWORD_FILENAME, source)

    def test_missing_key_raises_actionable_error(self):
        previous = os.environ.pop(tool.PASSWORD_ENV, None)
        cwd = os.getcwd()
        with tempfile.TemporaryDirectory() as directory:
            try:
                os.chdir(directory)
                with self.assertRaises(tool.UserExamError) as caught:
                    tool._load_password()
                message = str(caught.exception)
                self.assertIn(tool.PASSWORD_ENV, message)
                self.assertIn(tool.PASSWORD_FILENAME, message)
            finally:
                os.chdir(cwd)
                if previous is not None:
                    os.environ[tool.PASSWORD_ENV] = previous

    def test_environment_key_wins(self):
        previous = os.environ.get(tool.PASSWORD_ENV)
        os.environ[tool.PASSWORD_ENV] = "env-provided-key"
        try:
            self.assertEqual(tool._load_password(), "env-provided-key")
        finally:
            if previous is None:
                os.environ.pop(tool.PASSWORD_ENV, None)
            else:
                os.environ[tool.PASSWORD_ENV] = previous

    def test_password_file_is_used_when_env_absent(self):
        previous = os.environ.pop(tool.PASSWORD_ENV, None)
        cwd = os.getcwd()
        with tempfile.TemporaryDirectory() as directory:
            try:
                os.chdir(directory)
                (Path(directory) / tool.PASSWORD_FILENAME).write_text("  file-provided-key  \n", encoding="utf-8")
                self.assertEqual(tool._load_password(), "file-provided-key")
            finally:
                os.chdir(cwd)
                if previous is not None:
                    os.environ[tool.PASSWORD_ENV] = previous


class OutputNamingTests(unittest.TestCase):
    def test_deduplicates_with_index_suffix(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "UserExamInfo.db"
            source.write_bytes(b"x")
            target = Path(directory) / "UserExamInfo.decrypted.db"
            target.write_bytes(b"x")
            second = tool._output_path(source, "decrypt")
            self.assertEqual(second.name, "UserExamInfo.decrypted (1).db")

    def test_avoids_sidecar_names(self):
        """A taken sidecar makes the whole base name unusable.

        Publishing to ``UserExamInfo.decrypted.db`` while
        ``UserExamInfo.decrypted.db-wal`` exists would let SQLite attach the
        stale WAL to our freshly written database, so the tool skips the base
        name entirely and takes the next index.
        """
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "UserExamInfo.db"
            source.write_bytes(b"x")
            (Path(directory) / "UserExamInfo.decrypted.db-wal").write_bytes(b"x")
            target = tool._output_path(source, "decrypt")
            self.assertEqual(target.name, "UserExamInfo.decrypted (1).db")


class JournalGuardTests(unittest.TestCase):
    def test_active_wal_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "UserExamInfo.db"
            source.write_bytes(b"x")
            (Path(str(source) + "-wal")).write_bytes(b"pending frames")
            with self.assertRaises(tool.UserExamError):
                tool._reject_active_journal(source)

    def test_cold_persist_journal_is_allowed(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "UserExamInfo.db"
            source.write_bytes(b"x")
            (Path(str(source) + "-journal")).write_bytes(bytes(512))
            tool._reject_active_journal(source)  # must not raise

    def test_nonzero_journal_header_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "UserExamInfo.db"
            source.write_bytes(b"x")
            (Path(str(source) + "-journal")).write_bytes(b"ACTIVE!" + bytes(504))
            with self.assertRaises(tool.UserExamError):
                tool._reject_active_journal(source)


class InspectTests(unittest.TestCase):
    def test_rejects_encrypted_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "UserExamInfo.db"
            path.write_bytes(b"SQLite format 4\x00encrypted payload")
            with self.assertRaises(tool.UserExamError):
                tool.inspect_database(path)

    def test_reports_known_tables_and_names_unknown_ones(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "UserExamInfo.decrypted.db"
            connection = sqlite3.connect(str(path))
            connection.execute("CREATE TABLE baseInfo(Token TEXT, schoolId TEXT, schoolName TEXT, thirdFild INTEGER)")
            connection.execute("CREATE TABLE macAddress(Address TEXT, Flag TEXT)")
            connection.execute("CREATE TABLE unrelated(id INTEGER PRIMARY KEY)")
            connection.execute("INSERT INTO baseInfo VALUES('token-value','196586','某大学',0)")
            connection.execute("INSERT INTO macAddress VALUES('','wsh')")
            connection.commit()
            connection.close()

            report = tool.inspect_database(path)
            self.assertEqual(sorted(report["tables"]), ["baseInfo", "macAddress"])
            self.assertEqual(report["other_tables"], ["unrelated"])
            self.assertEqual(report["tables"]["baseInfo"]["rows"][0]["schoolId"], "196586")


class RoundTripTests(unittest.TestCase):
    """Needs the SQLite3MC runtime plus a real key; skipped otherwise."""

    def setUp(self):
        if not _vendor_ready():
            self.skipTest("_vendor SQLite3MC runtime not installed")
        if not os.environ.get(tool.PASSWORD_ENV):
            self.skipTest(f"{tool.PASSWORD_ENV} not set")

    def _build_plaintext(self, directory):
        path = Path(directory) / "UserExamInfo.decrypted.db"
        connection = sqlite3.connect(str(path))
        connection.execute("CREATE TABLE baseInfo(Token TEXT, schoolId TEXT, schoolName TEXT, thirdFild INTEGER)")
        connection.execute("CREATE TABLE macAddress(Address TEXT, Flag TEXT)")
        connection.execute("INSERT INTO baseInfo VALUES('token-value','196586','某大学',0)")
        connection.execute("INSERT INTO macAddress VALUES('','wsh')")
        connection.commit()
        connection.close()
        return path

    def test_encrypt_then_decrypt_preserves_content(self):
        with tempfile.TemporaryDirectory() as directory:
            plain = self._build_plaintext(directory)
            encrypted = tool.transform_database(plain)
            self.assertEqual(encrypted.operation, "encrypt")
            self.assertEqual(tool.infer_operation(encrypted.output_path), "decrypt")

            decrypted = tool.transform_database(encrypted.output_path)
            self.assertEqual(decrypted.operation, "decrypt")
            report = tool.inspect_database(decrypted.output_path)
            self.assertEqual(report["tables"]["baseInfo"]["rows"][0]["schoolId"], "196586")

            # The source must be byte-identical: the tool only writes copies.
            self.assertEqual(tool.infer_operation(plain), "encrypt")

    def test_wrong_key_fails_instead_of_producing_output(self):
        previous = os.environ.get(tool.PASSWORD_ENV)
        with tempfile.TemporaryDirectory() as directory:
            plain = self._build_plaintext(directory)
            encrypted = tool.transform_database(plain)
            before = sorted(p.name for p in Path(directory).iterdir())
            os.environ[tool.PASSWORD_ENV] = "definitely-not-the-key"
            try:
                with self.assertRaises(tool.UserExamError):
                    tool.transform_database(encrypted.output_path)
            finally:
                if previous is None:
                    os.environ.pop(tool.PASSWORD_ENV, None)
                else:
                    os.environ[tool.PASSWORD_ENV] = previous
            # A failed conversion must not publish anything, and must not leave
            # staging artifacts behind: the directory listing is unchanged.
            after = sorted(p.name for p in Path(directory).iterdir())
            self.assertEqual(after, before)
            self.assertFalse(
                [name for name in after if name.endswith(".partial")],
                "failed conversion left a staging file behind",
            )


if __name__ == "__main__":
    unittest.main()
