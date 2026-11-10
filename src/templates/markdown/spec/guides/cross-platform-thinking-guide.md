# Cross-Platform Thinking Checklist

- Which bytes does the compiler embed: the index blob or the working copy?
- Do embedded executable resources require LF even when Go source accepts CRLF?
- Are symlink and permission tests actually running on the target platform?
- Can root capabilities make an unreadable-fixture test skip or falsely pass?
- Does the audit interpreter have its required parser, or can an existing
  deployment dependency be used without installation or bytecode writes?
- Have all temporary, home, application-data, telemetry and cache outputs been
  moved under the currently approved roots before running tools?
- Have tool-specific binary outputs such as GOBIN been overridden too, rather
  than assuming that changing GOPATH redirects go install?
- Does the conclusion distinguish compilation, target execution, isolated
  integration, service health and real platform interaction?

See [isolated regression contracts](../backend/container-regression.md).
