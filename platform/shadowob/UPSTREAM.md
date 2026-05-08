# Upstream Engine Fix Needed

## `core/engine.go` — Missing `sp.discard()` in EventResult else branch

**Location**: `processInteractiveEvents()`, EventResult handling, ~line 4067

**Problem**: The `else` branch sends the final reply without calling `sp.discard()`, leaving the preview message visible alongside the final result — user sees two messages.

All other branches (`isSilent`, `hasRichCard`, `toolCount && segmentStart`, `suppressDuplicate`) correctly call `sp.discard()`. Only the `else` fallthrough is missing it.

**Fix**:
```diff
 } else {
+    sp.discard()
     slog.Debug("EventResult: sending via p.Send ...")
```

**Impact**: Any platform that implements `PreviewStarter` but falls through to this branch. Shadow currently works around this at platform level (`previewMsgs` map in `platform/shadowob/shadowob.go`), which can be removed after the engine fix.
