# Niks3 pin delivery (NixOS pilot)

This fork adds a main/testing niks3 fetcher, inspired by nlewo/comin's
`niks3` draft at `153f466f8b52d6add4f76426da401a31bfa33648`.
The implementation targets niks3 v1.11's actual S3 pin format: one plain-text
store output path. It does not require credentials for the management API.

```nix
services.comin = {
  enable = true;
  niks3.url = "https://cache.example/pins/device-channel";
  buildConfirmer.mode = "without";
  deployConfirmer.mode = "manual";
};
systemd.tmpfiles.rules = [
  "L+ /nix/var/nix/gcroots/comin-pending - - - - /var/lib/comin/gcroots/last-built-generation"
];
```

Configure `nix.settings.substituters` and `trusted-public-keys` for the cache.
Use HTTPS for the pin endpoint: a cache signature authenticates a store path,
but does not authenticate channel selection or prevent replay of an old pin.
The isolated VM test uses private S3 over HTTPS with public fixture keys only.

CI uploads a complete closure with an immutable release pin, then promotes
the channel with `niks3 pins create <channel> <store-path>`. Serialize channel
promotion in CI. Keep release pins for all supported rollback targets.

The device never evaluates a Nix expression in this mode. Comin's existing
preparation stages pass through pin metadata and realize the exact output
using `nix-store`. Local/remote builders are disabled for this command.
HTTP errors and malformed pins leave a ready proposal intact; an unchanged
pin retries failed downloads. A changed pin cancels the unfinished download.
The prepared system is rooted and its confirmation UUID/operation are saved
in `store.json`, so manual installation survives agent restart and cold boot
without a connection. Already attempted installations are not replayed.

Git remotes and `niks3` are mutually exclusive. The Nix package argument
`withGit = false` omits the runtime Git wrapper for pin-only deployments.
The RPC state exposes `source.niks3` and `fetcher.niks3_status`.

This does not provide automatic rollback after a failed activation, recovery
of an interrupted activation, or data migration rollback. The deployment
logic remains Comin's existing NixOS executor. Interactive desktop changes
from PR #184 and related branches are independent of this work.

## Private S3 pins

Use an S3 URL with a runtime AWS credentials file shared with Nix:

```nix
services.comin.niks3 = {
  url = "s3://fleet-cache/pins/main-device?endpoint=s3.example.org&scheme=https&region=us-east-1&profile=fleet-cache";
  testing_url = "s3://fleet-cache/pins/testing-device?endpoint=s3.example.org&scheme=https&region=us-east-1&profile=fleet-cache";
  aws_credentials_file = "/etc/fleet-cache.aws";
};
systemd.services.nix-daemon.environment.AWS_SHARED_CREDENTIALS_FILE = "/etc/fleet-cache.aws";
nix.settings.substituters = [
  "s3://fleet-cache?endpoint=s3.example.org&scheme=https&region=us-east-1&profile=fleet-cache"
];
```

Provision the credentials file separately (root, mode 0600), outside the Nix
store. It uses standard `[fleet-cache]`, `aws_access_key_id`, and
`aws_secret_access_key` fields. Only the configured file/profile is read; ambient
AWS credentials are never used. AWS SDK for Go v2 handles signing and S3 reads.
The file is read on every poll to support rotation. HTTPS is required.

Installers can use the same validated reader without a running Comin agent:

```sh
comin pin 's3://fleet-cache/pins/pilot-device?endpoint=s3.example.org&profile=fleet-cache' \
  --aws-credentials-file /run/credentials/cache.aws
```

The command prints a single validated output store path. Nix still must verify
the cache signature when downloading it. A pin selects a release; it does not
replace artifact signature verification.

## Main and testing

`url` is the main pin. With `testing_url`, Comin also reads that URL with
`-<main-store-hash>` appended to its path. A different testing output is preferred;
missing/equal testing selects main. Main is read again before accepting the pair
to detect publication during the poll. Errors other than a missing key preserve
the existing proposal. An older base's testing pin cannot shadow a newer main.

Main uses `operation` (default `switch`), testing uses `testing_operation`
(default `test`). An equal output moving from testing to main still gets a new
confirmation for `switch`. Withdrawn/replaced testing proposals become invalid;
offline restarts preserve the selected operation and UUID until newer pins arrive.

Git ancestry stays in the publisher. It must reject non-fast-forward main,
allow testing resets only above main, ignore testing equal to/below/divergent
from main, and rebind a still-descendant testing release when main advances.
Publish testing before main, serialize reconciliation, and resolve current refs
instead of trusting a finishing job's old commit. Withdrawing a test can overwrite
its pin with the main path, avoiding dependence on best-effort pin deletion.

This models successfully published Git heads, not live Git refs. A reconcile-only
CI job (also suitable for a schedule) must observe branch deletion/reset even
when no new build finishes. No Git or Nix evaluation is needed on devices.
