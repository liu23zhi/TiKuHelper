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

import hashlib
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

    def test_frozen_build_finds_password_next_to_the_exe(self):
        """password.txt beside the exe must win over _MEIPASS and the cwd.

        In a one-file build ``_MEIPASS`` is a throw-away extraction directory,
        so searching it first (the pre-fix order) made the documented
        "drop password.txt next to the exe" deployment silently fail. This test
        simulates a frozen run: it is the only place that behaviour is checked.
        """
        previous = os.environ.pop(tool.PASSWORD_ENV, None)
        cwd = os.getcwd()
        with tempfile.TemporaryDirectory() as exe_dir, tempfile.TemporaryDirectory() as meipass:
            try:
                exe_path = Path(exe_dir) / "userexam-crypto.exe"
                exe_path.write_bytes(b"MZ")
                (Path(exe_dir) / tool.PASSWORD_FILENAME).write_text("beside-exe-key", encoding="utf-8")
                # A decoy inside the bundle must NOT win: it is regenerated on
                # every run and would make the shipped file authoritative.
                (Path(meipass) / tool.PASSWORD_FILENAME).write_text("bundled-key", encoding="utf-8")
                os.chdir(meipass)

                saved_frozen = getattr(sys, "frozen", None)
                saved_meipass = getattr(sys, "_MEIPASS", None)
                saved_executable = sys.executable
                sys.frozen = True
                sys._MEIPASS = meipass
                sys.executable = str(exe_path)
                try:
                    self.assertEqual(tool._load_password(), "beside-exe-key")
                    self.assertEqual(tool._password_search_dirs()[0], Path(exe_dir))
                finally:
                    sys.executable = saved_executable
                    for name, value in (("frozen", saved_frozen), ("_MEIPASS", saved_meipass)):
                        if value is None:
                            if hasattr(sys, name):
                                delattr(sys, name)
                        else:
                            setattr(sys, name, value)
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
    """真实加解密往返：这是本工具唯一的核心能力，必须真跑而不是静默 skip。

    自检用**任意**口令即可：`transform_database` 在 encrypt 与 decrypt 两条路径上
    都调用同一个 `_load_password()`（见 userexam_crypto.py 的 156/318/330 行），
    所以往返不需要「客户端那个真实口令」。真实口令只在解密**外部既有**样本时才需要，
    那是另一条用例（需要 _vendor + 真实口令，缺任一条才 skip）。

    `USEREXAM_REQUIRE_CRYPTO=1` 时缺 _vendor 会直接失败而不是 skip —— CI 用它
    把「环境没装运行库」从 warning 变成 red。
    """

    SELFTEST_KEY = "userexam-unit-selftest-key"

    def setUp(self):
        self._saved_key = os.environ.get(tool.PASSWORD_ENV)
        os.environ[tool.PASSWORD_ENV] = self.SELFTEST_KEY
        if not _vendor_ready():
            if os.environ.get("USEREXAM_REQUIRE_CRYPTO") == "1":
                self.fail(
                    "_vendor 缺失但 USEREXAM_REQUIRE_CRYPTO=1：加解密往返必须真跑。"
                    "先执行 py -3.11 -m pip install --target .\\_vendor apsw-sqlite3mc==3.54.0.0"
                )
            self.skipTest("_vendor SQLite3MC runtime not installed")

    def tearDown(self):
        if self._saved_key is None:
            os.environ.pop(tool.PASSWORD_ENV, None)
        else:
            os.environ[tool.PASSWORD_ENV] = self._saved_key

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

    def test_source_file_is_left_byte_identical(self):
        """源文件必须逐字节不变 —— 这是 README 明确承诺的「源文件全程只读」。

        用 sha256 比对而不是「文件还在」：后者对「被原地 rekey 过」照样通过。
        """
        with tempfile.TemporaryDirectory() as directory:
            plain = self._build_plaintext(directory)
            before = hashlib.sha256(plain.read_bytes()).hexdigest()

            encrypted = tool.transform_database(plain)

            self.assertEqual(hashlib.sha256(plain.read_bytes()).hexdigest(), before)
            self.assertEqual(plain.read_bytes()[:16], tool.SQLITE_HEADER)
            self.assertNotEqual(encrypted.output_path, plain)
            self.assertNotEqual(hashlib.sha256(encrypted.output_path.read_bytes()).hexdigest(), before)

    def test_existing_output_is_not_overwritten(self):
        """目标已存在时必须另取名字，不能覆盖。

        只测 `_output_path()` 的取名是不够的：真正危险的是 `transform_database`
        落盘那一步（os.rename 在 Windows 上会直接覆盖）。这里先放一个同名占位文件，
        确认它的内容在转换后原封不动。
        """
        with tempfile.TemporaryDirectory() as directory:
            plain = self._build_plaintext(directory)
            occupied = Path(directory) / "UserExamInfo.decrypted.encrypted.db"
            sentinel = b"pre-existing output must survive"
            occupied.write_bytes(sentinel)

            result = tool.transform_database(plain)

            self.assertNotEqual(result.output_path, occupied)
            self.assertEqual(occupied.read_bytes(), sentinel, "转换覆盖了已存在的输出文件")
            self.assertEqual(result.output_path.name, "UserExamInfo.decrypted.encrypted (1).db")
            self.assertTrue(result.output_path.is_file())


if __name__ == "__main__":
    unittest.main()
