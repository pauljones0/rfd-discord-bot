"""Local setup fixtures only: never access Discord or an installed env file."""

import argparse
import contextlib
import getpass
import io
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch

import configure_discord as setup


APP = "123456789012345678"
OTHER_APP = "234567890123456789"
KEY = "abcdef0123456789" * 4
TOKEN = "synthetic.test_token.0123456789"


class ConfigureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / ".env"

    def run_setup(self, answers, token="", app_id=None, public_key=None, tty=True):
        args = argparse.Namespace(env_file=self.path, app_id=app_id, public_key=public_key)
        output = io.StringIO()
        with patch("sys.stdin.isatty", return_value=tty), \
                patch("builtins.input", side_effect=answers), \
                patch("getpass.getpass", return_value=token), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            setup.configure(args)
        self.assertNotIn(TOKEN, output.getvalue())
        return output.getvalue()

    def test_labeled_values_and_invalid_input(self):
        for value in (APP, "Application ID: " + APP, "DISCORD_APP_ID='" + APP + "'", "AppID " + APP):
            self.assertEqual(setup.parse_public(value, "app_id"), APP)
        self.assertEqual(setup.parse_public("Public Key = " + KEY.upper(), "public_key"), KEY)
        for value in ("Guild ID: " + APP, "0", str(2**64), APP + "\nLOG_LEVEL=DEBUG", "$(whoami)"):
            with self.assertRaises(ValueError):
                setup.parse_public(value, "app_id")
        with self.assertRaises(ValueError):
            setup.parse_public("Public Key: " + KEY[:-1], "public_key")

    def test_new_file_from_template_and_separate_line_labels(self):
        self.run_setup(["Application ID", APP, "Public Key", KEY], TOKEN)
        content = self.path.read_text()
        self.assertIn("DISCORD_APP_ID=" + APP, content)
        self.assertIn("DISCORD_PUBLIC_KEY=" + KEY, content)
        self.assertIn("DISCORD_BOT_TOKEN=" + TOKEN, content)
        self.assertIn("RFD_POLL_INTERVAL=3m", content)
        self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o600)

    def test_existing_config_preserved_and_duplicate_keys_removed(self):
        unrelated = '# my schedule\r\nRFD_POLL_INTERVAL=7m\r\nCUSTOM="keep $literal"\r\n'
        self.path.write_bytes((unrelated + f"DISCORD_APP_ID={OTHER_APP}\r\nexport DISCORD_APP_ID='{APP}'\r\n"
                               + f'DISCORD_BOT_TOKEN="{TOKEN}" # keep this token\r\n'
                               + f"DISCORD_PUBLIC_KEY={KEY}\r\nDISCORD_GUILD_ID={OTHER_APP}").encode())
        self.path.chmod(0o644)
        self.run_setup(["", ""])
        content = self.path.read_bytes().decode()
        self.assertTrue(content.startswith(unrelated))
        self.assertEqual(content.count("DISCORD_APP_ID="), 1)
        self.assertIn("DISCORD_BOT_TOKEN=" + TOKEN, content)
        self.assertIn("DISCORD_PUBLIC_KEY=" + KEY, content)
        self.assertIn("DISCORD_GUILD_ID=" + OTHER_APP, content)
        self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o600)

    def test_changed_identity_clears_old_credentials(self):
        self.path.write_text(f"DISCORD_APP_ID={OTHER_APP}\nDISCORD_PUBLIC_KEY={KEY}\nDISCORD_BOT_TOKEN={TOKEN}\n")
        output = self.run_setup([APP, ""])
        content = self.path.read_text()
        self.assertNotIn(TOKEN, content)
        self.assertNotIn(KEY, content)
        self.assertIn("DISCORD_BOT_TOKEN=\n", content)
        self.assertIn("Bot token still needed", output)

    def test_missing_token_can_be_added_later_with_public_flags(self):
        output = self.run_setup([], app_id=APP, public_key=KEY)
        self.assertIn("Bot token still needed", output)
        self.run_setup(["", ""], TOKEN)
        self.assertIn("DISCORD_BOT_TOKEN=" + TOKEN, self.path.read_text())

    def test_kept_credentials_are_normalized(self):
        self.path.write_text(f"DISCORD_APP_ID={APP}\nDISCORD_PUBLIC_KEY={KEY.upper()}\nDISCORD_BOT_TOKEN='Bot {TOKEN}'\n")
        self.run_setup(["", ""])
        content = self.path.read_text()
        self.assertIn("DISCORD_PUBLIC_KEY=" + KEY, content)
        self.assertIn("DISCORD_BOT_TOKEN=" + TOKEN, content)

    def test_cancel_and_nonterminal_do_not_write(self):
        original = b"# unchanged\n"
        self.path.write_bytes(original)
        with self.assertRaises(EOFError):
            self.run_setup([EOFError()])
        self.assertEqual(self.path.read_bytes(), original)
        with self.assertRaisesRegex(ValueError, "interactive terminal"):
            self.run_setup([], tty=False)
        self.assertEqual(self.path.read_bytes(), original)

    def test_token_cannot_inject_env_and_errors_do_not_echo_it(self):
        for value in (KEY, TOKEN + "\nDISCORD_GUILD_ID=" + APP, "$(touch /tmp/oops)", TOKEN + "#comment"):
            with self.assertRaises(ValueError) as error:
                setup.parse_token(value)
            self.assertNotIn(value, str(error.exception))
        self.assertEqual(setup.parse_token("Bot " + TOKEN), TOKEN)

    def test_symlinks_and_concurrent_edits_are_not_overwritten(self):
        destination = self.path.parent / "another.env"
        destination.write_text("keep\n")
        self.path.symlink_to(destination)
        with self.assertRaisesRegex(ValueError, "symlink"):
            setup.save_env(self.path, "overwrite\n", b"keep\n")
        self.assertEqual(destination.read_text(), "keep\n")
        self.path.unlink()
        self.path.write_text("new edits\n")
        with self.assertRaisesRegex(ValueError, "changed during setup"):
            setup.save_env(self.path, "overwrite\n", b"original\n")
        self.assertEqual(self.path.read_text(), "new edits\n")

    def test_failed_replace_keeps_original_and_removes_secret_tempfile(self):
        original = b"# original\n"
        self.path.write_bytes(original)
        with patch("os.replace", side_effect=OSError("fixture failure")), self.assertRaises(OSError):
            setup.save_env(self.path, TOKEN, original)
        self.assertEqual(self.path.read_bytes(), original)
        self.assertEqual(list(self.path.parent.iterdir()), [self.path])

    def test_getpass_does_not_fall_back_to_echoing_token(self):
        with patch("getpass.getpass", side_effect=getpass.GetPassWarning("fixture")), \
                patch("sys.stdin.isatty", return_value=True), \
                patch("sys.argv", ["configure_discord.py", "--env-file", str(self.path), "--app-id", APP, "--public-key", KEY]), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(setup.main(), 1)
        self.assertFalse(self.path.exists())


if __name__ == "__main__":
    unittest.main()
