# Niks3 pin delivery (NixOS pilot)

This fork adds a single-channel niks3 fetcher, inspired by nlewo/comin's
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
The isolated VM test uses HTTP with public fixture keys only.

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
