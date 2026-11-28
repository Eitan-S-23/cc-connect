# Windows managed restart and upgrade

- `/restart` and the post-install restart use the same host lifecycle.
- Bind one task/action to the current config through the live process ancestry;
  verify process creation times and task fingerprint before destructive actions.
- An independent scheduled-task controller must survive old Job Object cleanup.
  Never implement stop-and-start in the business process that is being stopped.
- Acknowledgment precedes shutdown. A committed shutdown precedes task stop.
  Actual old process exit and task state precede replacement startup.
- Download/checksum verification precedes downtime. Windows backups have unique
  transaction ownership; pending installations reject duplicates.
- Separate installed bytes, healthy running main/supervisor, and delivered
  notifications. A legacy supervisor or failed activation cannot report success.
- Occupied known histories may remain; permission errors must not be silently
  swallowed. Only checksum-matching owned files can be pruned. Keep rollback
  copies and a bounded history; fail explicitly if occupancy exhausts it.
- Windows process tests must run on Windows, including scheduled-task/Python
  launcher fixtures. Cross-compilation and RestartCh mocks are insufficient.
- Preserve Unix/standalone behavior and release-first deployment gates.
- See `docs/windows-restart-upgrade.md` for bootstrap maintenance and limitations.
