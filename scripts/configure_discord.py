#!/usr/bin/env python3
"""Interactively save Discord credentials without putting tokens in shell history."""

import argparse
import getpass
import os
from pathlib import Path
import re
import shlex
import sys
import tempfile
import warnings


ROOT = Path(__file__).resolve().parent.parent
ASSIGNMENT = re.compile(r"^[ \t]*(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=(.*)$")
LABELS = {
    "app_id": r"(?:application[ _-]*id|app[ _-]*id|discord_app_id)",
    "public_key": r"(?:public[ _-]*key|discord_public_key)",
}


def parse_public(value, field):
    """Accept a raw clipboard value or a known label followed by its value."""
    value = re.sub(r"^" + LABELS[field] + r"\s*[:=]?\s*", "", value.strip(), flags=re.I)
    value = value.strip().strip("\"'")
    if field == "app_id":
        if not re.fullmatch(r"[0-9]{17,20}", value) or not 0 < int(value) < 2**64:
            raise ValueError("Application ID must be a 17–20 digit Discord ID.")
    elif not re.fullmatch(r"[a-fA-F0-9]{64}", value):
        raise ValueError("Public key must contain exactly 64 hexadecimal characters.")
    return value.lower()


def parse_token(value):
    value = value.strip()
    if value.startswith("Bot "):
        value = value[4:]
    if re.fullmatch(r"[a-fA-F0-9]{64}", value):
        raise ValueError("That looks like a public key. Copy the token from the application's Bot page.")
    if not re.fullmatch(r"[A-Za-z0-9_.-]{20,}", value):
        raise ValueError("Token must be copied from the Bot page, without quotes or whitespace.")
    return value


def env_value(content, key):
    """Read only simple managed values; never source or evaluate the env file."""
    value = ""
    for line in content.splitlines():
        match = ASSIGNMENT.fullmatch(line)
        if match and match[1] == key:
            try:
                parts = shlex.split(match[2], comments=True)
            except ValueError:
                raise ValueError(f"Existing {key} must be a single-line value.") from None
            if len(parts) > 1:
                raise ValueError(f"Existing {key} must be a single value.")
            value = parts[0] if parts else ""
    return value


def merge_env(content, updates):
    """Preserve unrelated settings/comments and remove duplicate updated keys."""
    remaining = dict(updates)
    lines = []
    for line in content.splitlines(keepends=True):
        match = ASSIGNMENT.fullmatch(line.rstrip("\r\n"))
        key = match[1] if match else None
        if key in updates:
            if key in remaining:
                newline = "\r\n" if line.endswith("\r\n") else "\n"
                lines.append(f"{key}={remaining.pop(key)}{newline}")
        else:
            lines.append(line)
    result = "".join(lines)
    if remaining and result and not result.endswith("\n"):
        result += "\n"
    return result + "".join(f"{key}={value}\n" for key, value in remaining.items())


def read_env(path):
    if path.is_symlink():
        raise ValueError("Refusing to replace a symlink; choose the actual env file path.")
    try:
        return path.read_bytes()
    except FileNotFoundError:
        return None


def save_env(path, content, original):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if read_env(path) != original:
        raise ValueError("The env file changed during setup. Run setup again to keep those changes.")
    fd, name = tempfile.mkstemp(prefix=".discord-setup-", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="") as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def prompt_public(field, title, existing="", optional=False):
    hint = " [Enter to keep]" if existing else " [optional; Enter to skip]" if optional else ""
    while True:
        value = input(title + hint + ": ").strip()
        # Some clipboard selections put the label on the preceding line.
        if re.fullmatch(LABELS[field] + r"\s*[:=]?", value, flags=re.I):
            value = input("Value: ").strip()
        if not value and (existing or optional):
            return existing
        try:
            return parse_public(value, field)
        except ValueError as error:
            print(error, file=sys.stderr)


def configure(args):
    if not sys.stdin.isatty():
        raise ValueError("Run this command in an interactive terminal (use ssh -t for remote setup).")
    path = args.env_file.expanduser().absolute()
    original = read_env(path)
    content = (original if original is not None else (ROOT / ".env.example").read_bytes()).decode("utf-8")
    old_app = env_value(content, "DISCORD_APP_ID")
    old_key = env_value(content, "DISCORD_PUBLIC_KEY")
    old_token = env_value(content, "DISCORD_BOT_TOKEN")
    print(f"Saving standalone RFD settings to {path}")
    print("Use the dedicated RFD application. Paste values or labels such as Application ID: 123….")
    print("The public key is optional; Gateway commands use the bot token. Leave Interactions Endpoint URL empty.")
    app_id = parse_public(args.app_id, "app_id") if args.app_id else prompt_public("app_id", "Application ID", old_app)
    app_id = parse_public(app_id, "app_id")
    same_app = app_id == old_app
    if not same_app and (old_token or old_key):
        print("Application changed: the previous token and public key will be replaced or cleared.")
    public_key = parse_public(args.public_key, "public_key") if args.public_key else prompt_public(
        "public_key", "Public key", old_key if same_app else "", optional=True
    )
    token_hint = "Enter to keep existing" if same_app and old_token else "Enter to add later"
    while True:
        with warnings.catch_warnings():
            warnings.simplefilter("error", getpass.GetPassWarning)
            raw_token = getpass.getpass(f"Bot token (hidden; {token_hint}): ")
        if not raw_token.strip():
            token = old_token if same_app else ""
            break
        try:
            token = parse_token(raw_token)
            break
        except ValueError as error:
            print(error, file=sys.stderr)
    # Validate kept values as well, without printing the token on failures.
    if public_key:
        public_key = parse_public(public_key, "public_key")
    if token:
        token = parse_token(token)
    updated = merge_env(content, {
        "DISCORD_APP_ID": app_id,
        "DISCORD_PUBLIC_KEY": public_key,
        "DISCORD_BOT_TOKEN": token,
    })
    save_env(path, updated, original)
    print(f"Saved {path} with owner-only permissions (0600).")
    if not token:
        print("Bot token still needed: rerun this command when you have it.")
    print("No services started or restarted. Settings take effect at the next bot start.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, default=ROOT / ".env", help="Config file to update (default: repository .env)")
    parser.add_argument("--app-id", help="Application ID; otherwise prompted")
    parser.add_argument("--public-key", help="Optional public key; otherwise prompted")
    args = parser.parse_args()
    try:
        configure(args)
    except (EOFError, KeyboardInterrupt):
        print("\nCancelled; configuration was not saved.", file=sys.stderr)
        return 1
    except getpass.GetPassWarning:
        print("Cannot hide token input in this terminal; configuration was not saved.", file=sys.stderr)
        return 1
    except (OSError, ValueError) as error:
        print(f"Setup failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
