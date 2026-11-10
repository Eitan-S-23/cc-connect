# Cross-Layer Thinking Checklist

- Is the callback actor verified independently of the session/card owner?
- Are adapter allowlists and engine admin/role gates both exercised?
- Does a failed operation leave retries, session history and persisted state intact?
- Are stale-card absolute paths preserved through rendering, callback parsing and
  directory application rather than resolved against mutable current state?
- Did tests cover the failure from the final application layer, not only decoding?
- Is automatic callback regression being kept distinct from real client clicks?

See [directory-list contracts](../backend/dir-list-callbacks.md) and
[required tests](../unit-test/dir-list.md).
