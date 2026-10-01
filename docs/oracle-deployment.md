# Oracle deployment and moving the Rust bot

This repository is independent. Its runtime is `rfd-bot` and its persistent volume
is `rfd-data`, containing `rfd.sqlite`. Keep one active process per Discord identity.
The exact same image, environment file and SQLite snapshot can move to another
Linux VM without a hosted database or a cloud SDK in the bot.

## Free resources

Use the tenancy home region and its actual remaining Always Free allowance.
Current [Oracle documentation](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)
provides 1,500 A1 OCPU-hours and 9,000 GB-hours per month (2 OCPUs/12 GB total),
or up to two E2.1.Micro AMD instances. The combined boot/block disk allowance is
200 GB; reserve one 50 GB boot disk only if that space remains available.
A single A1 VM with 1 OCPU and 2 GB is ample for both containers. Choose an
Always Free eligible Ubuntu/Oracle Linux image. Inventory existing resources
first; stopped instances can still retain billable disks. Use ordinary internet
egress, outbound DNS/HTTPS and SSH for administration. No inbound bot port,
load balancer, NAT gateway, paid database or cloud AI service is needed.

Oracle can run out of capacity or reclaim idle instances. A budget alert does
not enforce a spending stop. Do not use trial credits as a permanent free plan
or manufacture load to defeat reclamation. Keep restorable backups off the VM.
AWS time-limited credits and GCP external IPv4 costs do not meet the same
ongoing $0 requirement; see the provider discussion in the hosting runbook.

## Package, transfer and install

Install Docker Engine and Compose on the VM using the distribution's supported
packages. If using Ubuntu, `sudo apt-get update && sudo apt-get install -y
docker.io docker-compose-v2` provides both. Run Docker through sudo or an
administrator-approved Docker group. Enable its service at boot. Avoid compiling
on an E2 micro; build on a machine with adequate RAM.

For an A1 host, build on an Arm64 machine or a buildx builder with Arm emulation:

```sh
./deploy/oci/package.sh /private/release/rfd-bot linux/arm64
scp -r /private/release/rfd-bot ubuntu@HOST:~/rfd-bot
ssh ubuntu@HOST
cd ~/rfd-bot
./manage.sh load
```

For an AMD micro use `linux/amd64`. The bundle contains the static image, Compose
file, example environment and management helper, with SHA-256 checksums. It
contains no bot credentials, database, build tools or Go reference code. The
helper checks host architecture before loading. Keep its default Compose project
`rfd-cloud` or set `BOT_PROJECT` consistently for every command.

Transfer this bot's existing `.env` separately over SSH, or create it from
`env.example` for a new independent application. Set mode 0600. For a migration,
restore state into the empty volume before starting; do not run a blank new
producer with the old token. `./manage.sh check` validates config/storage offline.
Registration is separate and should only update intended application/scopes.
After stopping the previous producer and its watchdogs, `./manage.sh start`
activates the service, and `./manage.sh health` checks local storage health.
Check Gateway health and poll logs as well; a storage healthcheck alone cannot
prove upstream availability. Docker restarts exited processes, not merely
unhealthy running containers.

## State, retention and rollback

Use `rfd-bot backup --destination /data/NEW.sqlite` for a consistent snapshot,
including committed WAL pages. It publishes mode 0600 and refuses overwrite.
`./manage.sh backup` creates a uniquely named snapshot in the data volume;
copy it over SSH to an existing machine, verify it, and remove the temporary
volume copy. Do not rely solely on a snapshot stored on the VM being reclaimed.
Rotate external backups yourself; the helper does not silently delete them.

Stop both watchdog.timer and watchdog.service before stopping an old supervised
producer. Stop the old bot gracefully; then snapshot and transfer its state.
Keep original environment, image and backup for rollback. Never copy just the
main SQLite file while WAL may contain committed records. Never overwrite an
already populated destination. Restore with ownership 65532:65532, mode 0600,
and the filename `rfd.sqlite` in `rfd-cloud_rfd-data`. The normal Compose runtime
is non-root and read-only outside that volume. Legacy root-owned binds need an
explicit ownership migration or the historical root override.

Each bot uses a single native SQLite connection on local disk, WAL/FULL commits,
a 2 MiB cache target, 256-page checkpoints and a 1 MiB retained-WAL target.
Long external read transactions can pin WAL growth; keep them short. RFD retains
receipts by its default 2,000-row cap, not an age TTL that would repost visible
old threads. Crux retains company identity without TTL, keeps completed delivery
and change history for 90 days/10,000 rows, and never expires pending delivery.
At 10,000 pending rows it rejects new snapshots transactionally. These are row
limits, not hard disk byte quotas. Subscriptions and record sizes still matter.

During rollback, stop the new producer first. Its backup may contain new
receipts/outbox acknowledgements absent from the old snapshot. Preserve those
before returning to the previous image; otherwise alerts can be duplicated.

For a tested cross-compiled static Rust binary, `deploy/oci/package-binary.sh
NEW_DIRECTORY STATIC_BINARY linux/arm64` creates the identical runtime bundle
without executing a compiler inside an Arm Docker container. It checks ELF
architecture, loader and shared-library dependencies. Execution testing remains
required: user-mode QEMU is a local compatibility check, not a native performance
measurement or proof of cloud networking. Native target builds use the normal
Dockerfile. No Zig or emulator belongs in the runtime image.

## Restore commands on a rootful Linux Docker host

Stop the old producer and its watchdogs first. Transfer a verified, consistent
SQLite backup separately from the runtime bundle. These commands restore to an
**empty** destination volume; use the same `BOT_PROJECT` for later management.
Do not run them over a running service or a populated destination.

```sh
project=${BOT_PROJECT:-rfd-cloud}
docker volume create "${project}_rfd-data"
volume_path=$(docker volume inspect --format '{{.Mountpoint}}' "${project}_rfd-data")
# Refuse to overwrite any populated destination.
[ -z "$(sudo find "$volume_path" -mindepth 1 -maxdepth 1 -print -quit)" ] || exit 1
sudo install -o 65532 -g 65532 -m 0600 /private/verified-snapshot.sqlite "$volume_path/rfd.sqlite"
BOT_PROJECT="$project" ./manage.sh check
```

For Linux cross builds without binfmt, install official Zig 0.16.0 on the build
host and run `ZIG=/path/to/zig ./deploy/oci/cross-build-arm64.sh`. It builds a
static Arm64 binary and fixture runner, uses native SQLite's Linux `fdatasync`
path, and leaves WAL/FULL durability enabled. Validate the fixture runner with
QEMU or native Arm before `package-binary.sh`. Native development builds on
Linux can use `LIBSQLITE3_FLAGS=-DHAVE_FDATASYNC=1 cargo build --locked --release`;
the production Dockerfile sets this Linux-only flag explicitly.

Actual upload and free-host launch results are recorded in [Oracle validation](oracle-validation.md).
