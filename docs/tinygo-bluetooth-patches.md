# tinygo.org/x/bluetooth divergences (and how to upstream them)

This repo used to ship a vendored fork of `tinygo.org/x/bluetooth@v0.15.0`
under `local_modules/bluetooth/`, wired in via a `replace` directive in
[`go.mod`](../go.mod). The fork modified exactly one file —
`adapter_darwin.go` — with four small changes that we needed for the Baarsa
app's clib build.

We removed the fork by reimplementing the darwin path of our `Adapter`
directly on top of `github.com/tinygo-org/cbgo` (the same Objective-C↔Go
bridge tinygo uses internally). The Linux/Windows path still uses upstream
tinygo unchanged.

- darwin: [`desk/adapter_darwin.go`](../desk/adapter_darwin.go) — direct
  cbgo, includes all four customisations below.
- non-darwin: [`desk/adapter_other.go`](../desk/adapter_other.go) — upstream
  tinygo, no patches needed.

This document records the four divergences so they can still be sent
upstream as a PR. Once merged, the daemon could in principle drop the
direct-cbgo path and go back to plain tinygo on darwin too.

## Source diff

Against `tinygo.org/x/bluetooth@v0.15.0/adapter_darwin.go`:

```diff
@@ -33,8 +33,8 @@
 var DefaultAdapter = &Adapter{
-    cm:         cbgo.NewCentralManager(nil),
-    pm:         cbgo.NewPeripheralManager(nil),
+    cm:         cbgo.NewCentralManager(&cbgo.ManagerOpts{RestoreIdentifier: "tinygo.ble.central"}),
+    pm:         cbgo.NewPeripheralManager(&cbgo.ManagerOpts{RestoreIdentifier: "tinygo.ble.peripheral"}),
     connectMap: sync.Map{},

@@ -45,6 +45,11 @@
 func (a *Adapter) Enable() error {
+    // Already enabled: delegates set and adapter powered on — nothing to do.
+    if a.cmd != nil && a.cm.State() == cbgo.ManagerStatePoweredOn {
+        return nil
+    }
+
     if a.poweredChan != nil {

@@ -59,6 +64,7 @@
     case <-time.NewTimer(10 * time.Second).C:
+        a.poweredChan = nil
         return errors.New("timeout enabling CentralManager")

@@ -67,8 +73,8 @@
     for len(a.poweredChan) > 0 { <-a.poweredChan }
+    a.poweredChan = nil

-    // wait until powered?
     a.pmd = &peripheralManagerDelegate{a: a}
```

## The four changes

### 1. `RestoreIdentifier` for the central + peripheral managers

```go
cm: cbgo.NewCentralManager(&cbgo.ManagerOpts{RestoreIdentifier: "tinygo.ble.central"}),
pm: cbgo.NewPeripheralManager(&cbgo.ManagerOpts{RestoreIdentifier: "tinygo.ble.peripheral"}),
```

Apple's `CBCentralManagerOptionRestoreIdentifierKey` opts an app into BLE
state restoration: if the OS terminates the app (foreground app crash, iOS
background eviction, etc.) and a connected peripheral subsequently sends a
notification or disconnects, CoreBluetooth relaunches the app and replays
the events via `centralManager(_:willRestoreState:)`. Without the key, those
events are lost.

The CLI daemon does not care — it's a long-lived foreground process and
doesn't implement the restoration delegate either way. The clib build
linked into the Baarsa app does care: Baarsa wants the BLE state to survive
process restarts.

Why upstream can't accept it as-is: hardcoding a single identifier is wrong.
Two apps sharing the library would share the identifier, and CoreBluetooth's
behaviour for collisions is undefined. The identifier must be caller-chosen.

**Upstream PR shape:** add a configurable hook. Cleanest options:

```go
// Option A — additional package-level setter, called before Enable():
func SetCentralManagerRestoreIdentifier(id string)
func SetPeripheralManagerRestoreIdentifier(id string)

// Option B — adapter-level option struct (DefaultAdapter can stay; new
// adapters created via NewAdapter(opts) accept restoration identifiers):
type AdapterOptions struct {
    CentralRestoreIdentifier   string
    PeripheralRestoreIdentifier string
}
func NewAdapter(opts AdapterOptions) *Adapter
```

Option A is the smaller PR, fewer breaking changes, and matches how other
package-level adapter knobs already work.

### 2. Idempotent `Enable()`

```go
if a.cmd != nil && a.cm.State() == cbgo.ManagerStatePoweredOn {
    return nil
}
```

Upstream's `Enable()`:

```go
if a.poweredChan != nil {
    return errors.New("already calling Enable function")
}
a.poweredChan = make(chan error, 1)
```

If you call `Enable()` once, it succeeds and `a.poweredChan` is set to a
non-nil drained channel (see bug #3 below). The next `Enable()` call hits
the guard and returns the misleading "already calling Enable" error.

Even ignoring bug #3, two legitimate use cases hit this:
- A library that does `Scan` from one entry point and `Connect` from
  another, each calling `Enable()` defensively.
- The Baarsa clib bindings entered the library through multiple C entry
  points; each called `Enable()`.

**Upstream PR shape:** add the early-return shown above. It is safe because
`cm.State()` is the source of truth — once it returns `PoweredOn`, the
adapter is usable. The existing `poweredChan != nil` guard can stay as a
race guard against two concurrent first-time `Enable()` calls.

### 3. `poweredChan = nil` after timeout

```go
case <-time.NewTimer(10 * time.Second).C:
    a.poweredChan = nil               // ← added
    return errors.New("timeout enabling CentralManager")
```

If powering on times out, upstream leaves `a.poweredChan` non-nil. Every
subsequent `Enable()` call then hits the "already calling Enable" guard and
returns immediately, even though no enable is actually in progress. The
adapter is permanently wedged.

Setting the channel to `nil` on the failure path lets the caller retry.

**Upstream PR shape:** one-line addition on the timeout branch. Trivial.

### 4. `poweredChan = nil` after success

```go
for len(a.poweredChan) > 0 {
    <-a.poweredChan
}
a.poweredChan = nil   // ← added
```

Same shape as #3, on the success path. Upstream drains the channel but
leaves the reference live. Combined with bug #2, this is what produces the
"already calling Enable" returns on the second `Enable()` call.

With the idempotent early-return from #2, this becomes a hygiene
improvement rather than a correctness fix — but it's still the right thing
to do.

**Upstream PR shape:** one-line addition. Trivial.

## Suggested upstream PR

A single commit titled something like:

> `bluetooth: darwin: configurable RestoreIdentifier, idempotent Enable, channel cleanup`

…with the four hunks above, plus tests:

- `TestEnableIdempotent` — call `Enable()` twice, second call returns
  `nil`.
- `TestEnableRecoversFromTimeout` — simulate a timeout, then a successful
  `Enable()` (this needs a hook to inject a fake `cm.State` source; may not
  be worth the testing scaffolding for a one-line fix).
- `TestRestoreIdentifierFromOptions` — set the package-level identifier,
  inspect the manager options.

The `RestoreIdentifier` part is the only API addition; the other three are
bug fixes. They naturally split into two PRs if maintainers prefer
finer-grained review:

1. PR1 (bug fixes, no API change): #2, #3, #4 — small, easy review.
2. PR2 (feature): #1 — exposes a setter or AdapterOptions field.

## Current divergence cost

Until upstream lands these, this repo carries:

- `desk/adapter_darwin.go` — ~550 lines, the darwin-specific implementation
  using cbgo directly. Behaviourally equivalent to upstream tinygo plus the
  four customisations.
- The same `cbgo.ManagerOpts.RestoreIdentifier` setting, hardcoded as
  `"yaasa.ble.central"`.
- A direct require on `github.com/tinygo-org/cbgo` in `go.mod` (previously
  indirect via tinygo).

If both PRs land we can replace `desk/adapter_darwin.go` with a
thirty-line wrapper that calls the upstream setter once, drops the build
tag split, and lets `desk/adapter_other.go` be the one cross-platform
file again.
