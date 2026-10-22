# marketplace testdata

Demo data only. `demo-market` is a Claude plugin marketplace used by the marketplace tests
(and reused by later skilltri tests):

- `writing`: local plugin (`./plugins/writing`), plugin.json name `writing-tools`, skills `outline`
  plus extra root `extra-skills` (proofread); `.mcp.json` with stdio `notes`, http `docs`, sse `legacy`
  (skipped with a warning).
- `remote-one`: GitHub source `demo/remote-one` (never fetched in tests).
- `pkg`: unsupported npm source.
