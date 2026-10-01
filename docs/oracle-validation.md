# Oracle upload and execution validation — 2026-09-30

The complete Rust production runtime is locally validated, with static amd64 and
Arm64 release bundles uploaded to **private** Oracle Object Storage in the home
region `us-chicago-1`. Bucket: `discord-bots-rust-releases-20260930`. The four bundles total
18.92 MiB, within the documented Standard storage free allowance.
Every object was downloaded again and its SHA-256 matched the local archive.
This upload verifies transfer integrity, **not native cloud execution**.

This bot's objects and digests are in [the release manifest](../deploy/oci/releases-20260930.json).
No credentials, environment file, live database, Go reference or compiler is in
the bundles. The independent image/Compose configuration is unchanged between
local Docker and a destination Linux VM.

## Native VM test is blocked by provider availability

Authentication initially failed because a commercial tenancy used the Government
Cloud Chicago endpoint. A commercial region override succeeded; the CLI default
was backed up and corrected to its actual home region. An unqualified CLI region
request now succeeds. Credentials were not replaced.

Inventory found no existing instances or boot/block disks, one root compartment,
three availability domains and no reported monthly usage. We attempted only
documented free shapes and a 50 GB balanced boot disk, without relying on trial
credits, changing quotas, upgrading the account or selecting paid alternatives.

- A1: 1 OCPU/2 GB failed with `Out of host capacity` in all three domains.
- A1: reducing to 1 GB and explicitly trying all nine domain/fault-domain
  combinations returned the same error.
- AMD E2.1.Micro: its eligible domain has quota, but the shape is absent from
  the available-shape list. Explicit launches with Ubuntu 24.04 and 22.04 both
  returned `404 NotAuthorizedOrNotFound`; both images were available and
  compatible in image listings.

A subsequent read-only capacity-report check independently confirmed
`OUT_OF_HOST_CAPACITY` for A1 at both 1 OCPU/1 GB and 1 OCPU/2 GB in all three
domains. Fault domain was omitted, so the report includes all fault domains.
For E2.1.Micro, all three domains returned `HARDWARE_NOT_SUPPORTED`.
The caller's group has an unconditional `manage all-resources in tenancy`
policy. The AMD launch error therefore points to unsupported hardware for this
placement, rather than a missing general Compute permission. A positive quota
does not establish hardware support or available physical capacity.

The zero-cost path is to retry a single small A1 host in the home region when
capacity becomes available, allowing Oracle to choose its fault domain. Both
bots share that host and retain separate containers and persistent databases.
Do not fall back automatically to A2, E5, Standard3 or other paid shapes.
Oracle documents waiting/retrying or another availability domain as remedies;
all domains were already checked. A Pay As You Go upgrade preserves Always Free
allowances but enables chargeable usage and does not establish that an A1 slot
will become available. No upgrade was performed. Resolving the missing AMD
hardware offering would require clarification from Oracle; changing bot code
or broadening IAM permissions cannot create that hardware.

No VM or boot/block disk was created. The temporary network for failed attempts
was removed after verifying it was empty and belonged to this task. Only the
private release bucket remains. Existing production bots and state were not
cut over. Native Oracle CPU/RSS, cloud-init installation and live upstream
connectivity remain unverified until a free compute host becomes available.

## Download the tested runtime bundle

Use the authenticated CLI in the home region; choose the destination's architecture.
The following is for A1 (Arm64); replace `arm64` with `amd64` for an AMD host.
The manifest contains the outer archive hash, and the bundle has its own checksums.

```sh
namespace=$(oci os ns get --query data --raw-output)
oci --region us-chicago-1 os object get --namespace-name "$namespace" \
  --bucket-name discord-bots-rust-releases-20260930 \
  --name rust-20260930/rfd/linux-arm64.tar.gz --file rfd-linux-arm64.tar.gz
# Verify the SHA-256 against releases-20260930.json before extraction.
tar -xzf rfd-linux-arm64.tar.gz
cd rfd-bot
./manage.sh load
```

[Deployment and state migration](oracle-deployment.md) describes Docker setup,
consistent snapshots, ownership, producer/watchdog shutdown and rollback.
`bootstrap-ubuntu.sh` is cloud-init data for a new Ubuntu 24.04 host; it contains
no credentials or bot activation. [Runtime measurements](runtime-optimization.md)
record five-run comparisons using actual production musl fixture builds.
Both Arm64 binaries execute full Gateway/processor/SQLite fixtures under local
QEMU, and both containers pass 128 MiB resource/permissions checks. Emulation
results are not native Oracle performance measurements.

Oracle's [Always Free policy](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)
permits the selected footprint, but capacity shortages and idle-instance
reclamation prevent a guarantee of uninterrupted free hosting.
