#!/usr/bin/env bash
# Optional Linux cross build using an installed official Zig toolchain (tested 0.16.0).
# Zig is a build-only C/linker driver; no Zig code/runtime is used by the bots.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd -- "$root"
export BOT_ZIG
BOT_ZIG=$(command -v "${ZIG:-zig}") || { echo 'Install official Zig or set ZIG=/path/to/zig.' >&2;exit 1; }
export LIBSQLITE3_FLAGS="-DHAVE_FDATASYNC=1"
export CARGO_BUILD_JOBS=${BOT_BUILD_JOBS:-2}
rustup target add aarch64-unknown-linux-musl
task_tools="$root/target/arm64-tools"
mkdir -p -- "$task_tools"
cat > "$task_tools/cc" <<'PYCC'
#!/usr/bin/env python3
import os,sys
# cc-rs appends a Rust triple that Zig does not accept. Zig supplies its own CRT.
args=[arg for arg in sys.argv[1:] if not arg.startswith('--target=') and not ('/self-contained/crt' in arg and arg.endswith('.o'))]
zig=os.environ['BOT_ZIG']
os.execv(zig,[zig,'cc','-target','aarch64-linux-musl']+args)
PYCC
cat > "$task_tools/ar" <<'PYAR'
#!/usr/bin/env python3
import os,sys
zig=os.environ['BOT_ZIG'];os.execv(zig,[zig,'ar']+sys.argv[1:])
PYAR
chmod 755 "$task_tools/cc" "$task_tools/ar"
export CC_aarch64_unknown_linux_musl="$task_tools/cc"
export AR_aarch64_unknown_linux_musl="$task_tools/ar"
export CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER="$task_tools/cc"
cargo build --locked --release --target aarch64-unknown-linux-musl --bin rfd-bot --example runtime-fixture
readelf -h target/aarch64-unknown-linux-musl/release/rfd-bot | grep -q AArch64
[[ $(readelf -d target/aarch64-unknown-linux-musl/release/rfd-bot | grep -c '(NEEDED)' || true) == 0 && $(readelf -l target/aarch64-unknown-linux-musl/release/rfd-bot | grep -c INTERP || true) == 0 ]]
echo 'Static Arm64 build ready. Run fixture execution checks before packaging.'
