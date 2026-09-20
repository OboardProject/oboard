# Offline backup verification

Run the separately built `backup-verify` executable with an explicit local archive, target version and an existing absolute temporary parent directory with mode `0700`:

```sh
backup-verify -archive /private/input/synthetic.obk \
  -target-version 1.2.3 -temp-parent /private/verification \
  < /private/input/password
```

The password file must have mode `0600`; a pipe is also accepted. Do not put passwords in command arguments. No production configuration or credentials are discovered automatically.

The JSON report distinguishes passed, failed, not-run and unsupported checks. Source database validation happens before migrations: an existing SQLite database with the foundational Controller settings table is required. A readable empty database is not a valid Controller backup. `verified` means only that the supported offline checks passed, not successful disaster recovery. Configuration rebuilding, external services and node takeover are not validated.

This command does not start Controller workers or invoke the restore commit path. It does **not** itself create an OS network namespace or filesystem sandbox. System-level network-denial/filesystem isolation has not been verified by the unit tests; use an operator-provided restricted environment with no production mounts or network access for that additional boundary.

The ten-minute context bounds context-aware database work, not the entire process. Password input, archive extraction/decryption and store initialization do not all observe that deadline. Use an external process supervisor when a hard wall-clock timeout is required.

Normal success and failure paths remove the command-owned `oboard-backup-verify-*` temporary directory. After a crash or forced termination, inspect and remove only directories belonging to that invocation under the dedicated temporary parent. They may contain decrypted data. Do not delete unrelated directories; cleanup does not promise secure erasure.
