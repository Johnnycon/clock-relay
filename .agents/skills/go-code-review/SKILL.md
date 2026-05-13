---
name: golang-reviewer
description: Reviews Go code for idiomatic style, code smells, anti-patterns, and modern best practices. Use when reviewing .go files, inspecting Go changes or PRs, auditing Go packages, or when the user asks to check Go code for quality, bugs, or style. Based on the Uber Go Style Guide with additions for modern Go (generics, context, security, HTTP, docs).
---

# Go Code Reviewer

Review Go code against the checklist below. Work through the categories in order. For each issue found, cite the file and line, quote the problematic snippet, explain *why* it's a problem, and show the idiomatic fix. Skip rules that don't apply — do not fabricate issues.

When the user asks for a review, favor a short, prioritized summary (correctness issues > idiom violations > style nits) over an exhaustive dump. If the file is large, ask whether they want everything or only the high-signal findings.

Testing rules (`*_test.go`) are intentionally out of scope for this skill.

---

## 1. Errors

**Type selection.** Pick the error style based on two questions: does the caller need to match on it, and is the message static or dynamic?

| Match? | Message | Use |
|--------|---------|-----|
| No | static | `errors.New("...")` at the call site |
| No | dynamic | `fmt.Errorf("...", args)` |
| Yes | static | top-level `var ErrX = errors.New("...")` |
| Yes | dynamic | custom `type XError struct { ... }` with `Error() string` |

**Wrapping.** Use `fmt.Errorf("...: %w", err)` to preserve the chain (`errors.Is`/`errors.As` walk it). Use `%v` only when you deliberately want to obscure the underlying error.

**Context phrasing in `fmt.Errorf` wrapping.** Don't prefix with `"failed to"` — it stacks into `failed to x: failed to y: failed to z: real error`. Write `fmt.Errorf("read config: %w", err)`, not `fmt.Errorf("failed to read config: %w", err)`. This rule applies to error chain wrapping only. Standalone log messages (`log.Printf("failed to enqueue job: %v", err)`) and user-facing error strings are not error chains and may use natural phrasing.

**Naming.** Exported sentinel vars use `Err` prefix (`ErrNotFound`). Unexported sentinels use `err` prefix (`errNotFound`). Custom error types end in `Error` (`NotFoundError`, `resolveError`).

**Error string style.** Lowercase, no trailing punctuation: `errors.New("parse config")`, not `errors.New("Parse config.")`. Errors get wrapped and concatenated (`x: y: parse config`), so capitals and periods read wrong mid-chain. Exceptions: starting with a proper noun or exported identifier (`HTTP timeout`, `JSON decode`).

**Return `error`, not a concrete error type,** from exported functions. `func F() *MyError` has a subtle footgun: a nil `*MyError` wrapped in the `error` interface becomes a *non-nil* interface value, so `if err != nil` fires even when there's no error. Return `error` and the problem disappears.

**Prefer `(value, ok)` or `(value, error)` to in-band sentinels.** `Lookup(key) int` returning `-1` for "not found" invites `Parse(Lookup(k))`-style bugs and forces every caller to remember the sentinel. `Lookup(key) (int, bool)` makes the caller handle the missing case at the type level.

**Handle once.** Don't log an error and then also return it — the caller will handle it too, producing duplicate log noise. Pick one: log-and-swallow (graceful degradation), wrap-and-return, or match-and-recover.

```go
// Bad
if err != nil {
    log.Printf("get user: %v", err) // caller will also log this
    return err
}

// Good
if err != nil {
    return fmt.Errorf("get user %q: %w", id, err)
}
```

**Type assertions.** Always use the comma-ok form. `x := v.(T)` panics on mismatch; `x, ok := v.(T)` does not.

**Match with `errors.Is` / `errors.As`, not `==`.** `if err == ErrNotFound` fails the moment anyone wraps `ErrNotFound` with `%w` further down the stack. `errors.Is(err, ErrNotFound)` walks the chain. Same for custom types: use `errors.As(err, &target)` (or `errors.AsType[T](err)` on Go 1.26+) instead of a raw type assertion.

**Don't panic in library or production code.** Return errors. Panic is reserved for truly unrecoverable situations (nil deref, invariant violations) and for program-initialization failures where continuing makes no sense (`template.Must`, `regexp.MustCompile` at package scope).

**`errors.Join` (Go 1.20+).** Prefer it over manual string concatenation when you need to return multiple errors together.

---

## 2. Concurrency and Goroutines

**Zero-value mutexes are valid.** `var mu sync.Mutex` is correct; `new(sync.Mutex)` is noise. Use a pointer receiver on methods that lock.

**Never embed `sync.Mutex`** (even on unexported types). It leaks `Lock`/`Unlock` into the public API. Use a named field (`mu sync.Mutex`) and keep it unexported.

**Don't copy types whose methods are on `*T`.** `sync.Mutex`, `sync.WaitGroup`, `bytes.Buffer`, `strings.Builder`, `atomic.*`, `sync.Once` — copying these breaks their invariants silently. `go vet` catches mutex copies but not all cases. If a struct contains one of these as a value field, the struct itself shouldn't be copied — return and pass it by pointer.

**Don't fire-and-forget goroutines.** Every `go func() { ... }()` needs (a) a predictable stopping condition or a cancellation signal, and (b) a way for the caller to wait for it to finish (`sync.WaitGroup`, `done chan struct{}`, `errgroup.Group`, or `context.Context`).

```go
// Bad: runs forever, no shutdown, no wait
go func() {
    for { flush(); time.Sleep(delay) }
}()

// Good: cancellable via ctx, caller can wait via errgroup
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error {
    ticker := time.NewTicker(delay)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C: flush()
        case <-ctx.Done(): return ctx.Err()
        }
    }
})
```

**No goroutines in `init()`.** Packages shouldn't spawn background work just because they were imported. Expose a constructor and a `Close`/`Shutdown` method instead.

**Channel buffer size should be 0 or 1** in almost all cases. Any larger buffer (`make(chan int, 64)`) is a smell — it hides backpressure. If you think you need a bigger buffer, document why and what happens when it fills.

**Channel direction in signatures.** When a function only sends or only receives, encode it in the type: `func producer(out chan<- int)`, `func consumer(in <-chan int)`. This is enforced by the compiler and documents intent.

**Atomics: use the stdlib typed API (Go 1.19+).** `atomic.Bool`, `atomic.Int64`, `atomic.Pointer[T]` are type-safe and should replace both `atomic.AddInt64(&raw, ...)`-style calls and the older `go.uber.org/atomic` package.

```go
// Bad (error-prone: nothing forces atomic access)
var running int32
atomic.StoreInt32(&running, 1)
if running == 1 { ... } // race

// Good
var running atomic.Bool
running.Store(true)
if running.Load() { ... }
```

**Atomics protect one value, not coordinated state.** If two variables must change together, use a mutex.

**Range loop variable capture.** In Go 1.22+ each iteration has its own copy, so `go func() { use(v) }()` inside `for _, v := range xs` is safe. In older codebases (pre-1.22 or `GOEXPERIMENT` off), require an explicit shadow: `v := v` before the goroutine.

**Prefer synchronous functions.** A synchronous function finishes its work before it returns; the caller can add concurrency with `go f()` if they want it. The reverse — stripping concurrency out of an asynchronous API — is painful or impossible. Async APIs also make goroutine lifetimes less obvious, which is where leaks come from. Export sync; let callers spawn.

---

## 3. Context (`context.Context`)

**First parameter, named `ctx`.** `func Do(ctx context.Context, ...) error`. Never store a `Context` in a struct field — pass it explicitly through the call chain.

**Don't pass `nil` as a Context.** Use `context.TODO()` if you genuinely don't have one yet; `context.Background()` at the root (e.g., in `main` or a request handler's top level).

**Respect cancellation.** Long-running loops and blocking operations must select on `<-ctx.Done()` and return `ctx.Err()`. Any function that takes a `ctx` but ignores it is suspect.

**`context.WithCancel`/`WithTimeout`/`WithDeadline` always pair with `defer cancel()`** — otherwise the context leaks until its parent is cancelled. `go vet` flags most cases.

**`context.Value` is for request-scoped data** (trace IDs, auth principals), not for passing optional arguments. If it's needed for the function to do its job, make it a parameter.

---

## 4. Interfaces and Types

**Accept interfaces, return concrete types.** Define interfaces on the consumer side, where they're used, not the producer side. A package that exports a struct with useful methods usually should not also export an interface describing those methods — the caller defines what they need.

**Don't take pointers to interfaces.** An interface is already a two-word value that can hold a pointer. `*io.Reader` is almost always wrong.

**Verify interface compliance at compile time** where the relationship is part of the API contract:

```go
var _ http.Handler = (*Handler)(nil)
```

The right-hand side is the zero value of the asserted type: `nil` for pointer/slice/map, empty struct for struct types.

**Receivers: consistency matters.** Within one type, either all methods have pointer receivers or all have value receivers — don't mix. Use pointer receivers when the method mutates, when the struct is large, or when it contains a `sync.Mutex` or similar non-copyable field.

**Don't embed types in exported structs.** Embedding leaks the inner type's methods and fields into the outer type's API, locks you into keeping it forever, and obscures godoc. Use a named field and write explicit delegating methods. Exceptions: embedding is fine when the intent is exactly "this type *is* an X-with-extras" (e.g., `countingWriteCloser { io.WriteCloser; count int }`).

**Generics (Go 1.18+).** Use type parameters when the same logic genuinely applies across types (e.g., `func Map[T, U any](xs []T, f func(T) U) []U`). Don't reach for generics when a single interface method would do. Don't parameterize over a single type that's always concrete.

**`any` over `interface{}` (Go 1.18+).** `any` is the built-in alias. New code should use it.

---

## 5. Structs and Initialization

**Use field names.** `User{FirstName: "a", LastName: "b"}`, not `User{"a", "b"}`. `go vet` enforces this for most packages.

**Omit zero-value fields** during initialization unless the zero value carries meaning in context:

```go
// Noisy
u := User{Name: "a", Middle: "", Admin: false}
// Clean
u := User{Name: "a"}
```

**Use `var` for the zero-valued struct.** `var u User`, not `u := User{}`. It matches the idiom for empty slices and makes the zero-value intent explicit.

**Prefer `&T{...}` to `new(T)`** for struct pointers. Consistent with value initialization and lets you set fields inline.

**Embedded fields go at the top of the field list,** separated from other fields by a blank line.

---

## 6. Slices and Maps

**`nil` is a valid slice.** Don't return `[]int{}` to mean "empty" — return `nil`. Check emptiness with `len(s) == 0`, never `s == nil`. The zero slice (`var xs []int`) is immediately usable with `append`.

**Maps must be initialized before writing.** `var m map[string]int` panics on write. Use `m := make(map[string]int)` or a literal. Use literals when the contents are fixed; `make` when you'll populate programmatically.

**Specify capacity when you know it.** Both for maps (as a hint) and slices (as a guarantee):

```go
users := make(map[string]*User, len(ids))  // reduces rehashing
out   := make([]Result, 0, len(in))        // zero allocations on append
```

**Copy slices and maps at boundaries** when you don't want callers mutating your internal state (or vice versa). Storing a caller-provided slice directly in a struct field means the caller can mutate your struct. Returning an internal map directly means callers can corrupt it and break your invariants. Copy in, or copy out, depending on direction.

```go
func (d *Driver) SetTrips(trips []Trip) {
    d.trips = make([]Trip, len(trips))
    copy(d.trips, trips)
}
```

**Don't append to a slice you don't own.** If `append` reallocates, you've silently diverged from the caller's view; if it doesn't, you've mutated their backing array.

---

## 7. Control Flow and Naming

**Handle errors/special cases first, return early.** Reduces nesting and makes the happy path flat:

```go
// Bad
for _, v := range xs {
    if v.OK {
        process(v)
    } else {
        log.Printf("skip: %v", v)
    }
}

// Good
for _, v := range xs {
    if !v.OK {
        log.Printf("skip: %v", v)
        continue
    }
    process(v)
}
```

**No unnecessary `else`.** If both branches assign, factor out the assignment:

```go
// Bad
var a int
if b { a = 100 } else { a = 10 }
// Good
a := 10
if b { a = 100 }
```

**Reduce variable scope.** Declare inside `if` when the value isn't needed after: `if err := f(); err != nil { ... }`. Declare constants inside the function that uses them, not at package scope, unless shared.

**Short declaration `:=` for explicit values, `var` for zero-values and nil slices.** Don't write `var s string = f()` — the type is redundant.

**Package names: lowercase, short, singular, no underscores, no `util`/`common`/`shared`/`lib`/`helpers`.** The package name becomes a prefix at every call site (`url.Parse`, not `urls.ParseURL`).

**Function names use MixedCaps / mixedCaps.** No underscores. (The test-function underscore exception is out of scope here.)

**Initialism casing is consistent.** `URL`, `ID`, `DB`, `HTTP`, `API`, `JSON`, `UUID`, `IP` stay the same case throughout an identifier: `userID` not `userId`, `URLPath` not `UrlPath`, `writeJSON` not `writeJson`, `HTTPServer` not `HttpServer`. For unexported names starting with an initialism, the initialism is lowercased: `urlPath`, not `uRLPath`. Mixed-case initialisms like `iOS`, `gRPC`, `DDoS` follow their prose spelling unless exportedness forces the first letter. `revive`/`staticcheck` flag violations.

**No `Get`/`get` prefix on getters.** `u.Name()` not `u.GetName()`. Use a verb when there's real work to do: `Fetch`, `Compute`, `Load`. "Get" is a gRPC/HTTP verb, not a Go method prefix.

**Don't repeat the package name in exported symbols.** `widget.New` not `widget.NewWidget`. `db.Load` not `db.LoadFromDB`. `http.Request` not `http.HTTPRequest`. The package name is already visible at the call site; repeating it is pure noise.

**Don't repeat the type in the variable name.** `users` not `userSlice` or `userList`. `count` not `numUsers` or `userCount` (when already in a user-related scope). `name` not `nameString`. The type is visible to the compiler and to any reader's IDE; a name like `userSlice` is saying something the code already says. Exception: when two forms of the same value are in scope together, use a qualifier (`nameRaw` and `name`, or `ageStr` and `age`).

**Receiver names are short and consistent.** One or two letters, usually abbreviation of the type: `func (s *Server)`, `func (u *User)`, `func (b *Buffer)`. Never `this`, `self`, or `me`. Use the *same* receiver name for every method on a type across every file. Use `_` (or omit the name) when the method doesn't reference the receiver.

**Don't shadow builtins.** `error`, `string`, `len`, `copy`, `new`, `append`, `make`, `true`, `false`, `nil` — using any of these as an identifier creates confusing code and latent bugs. `go vet` catches some cases.

**Avoid mutable globals.** Use dependency injection — pass them in, or put them on a struct. This also makes the code testable without monkey-patching package-level variables.

**Avoid `init()`.** Where unavoidable, it must be deterministic, side-effect-free, and do no I/O, no environment access, no network, no filesystem. Convert most `init()` work into a `NewX()` called from `main`.

---

## 8. Style Details

**Imports: stdlib first, then external deps, then internal packages, blank lines between groups.** Two groups (stdlib + everything else) or three groups (stdlib / external / internal) are both acceptable. `goimports` handles this automatically.

```go
import (
    "fmt"
    "os"

    "github.com/x/y"
    "go.uber.org/zap"

    "github.com/myorg/myapp/internal/config"
)
```

**Import aliases only when required** (package name differs from import path's last element, or two imports collide). Don't rename for cosmetics.

**Blank imports (`import _ "pkg"`) only in `package main` or test files.** These exist for registration side effects (image decoders, database drivers, tzdata). A library that imports for side effects drags that behavior onto every program that transitively imports it. Exception: `import _ "embed"` when using `//go:embed` directives.

**Dot imports (`import . "pkg"`) are banned.** They make it impossible to tell where identifiers come from without jumping to definitions.

**Group related declarations** with `const ( ... )` / `var ( ... )` / `type ( ... )` blocks. Don't group unrelated declarations together just because they're the same kind — group by meaning.

**Function ordering within a file:** types, then constructors (`NewX`), then exported methods on the type, then unexported methods, then free functions. Sort roughly in call order. This makes files easy to skim.

**Line length.** No hard limit. `gofmt` doesn't enforce one. If a line feels too long, prefer refactoring (extract a local, split a long conditional) over mechanical wrapping. ~100 columns is a reasonable soft ceiling if your team wants one, but readability beats any specific number — don't break a URL or a format string just to hit a column target.

**Enums: `iota` starting at `1`, unless zero has a deliberate meaning.** A zero default for an untyped variable should not silently resolve to a valid enum value:

```go
// Bad: Add == 0 == zero value of Operation
const ( Add Operation = iota; Sub; Mul )
// Good: zero is invalid, forces explicit choice
const ( Add Operation = iota + 1; Sub; Mul )
```

Consider adding a `String()` method for stringer-friendly logs, or use `go generate` with `stringer`.

**Use the `time` package for time.** `time.Time` for instants, `time.Duration` for durations. Never use `int`/`int64` to represent a duration in the signature — `func poll(delay time.Duration)`, not `func poll(delayMs int)`. When an external format can't carry a `Duration`, include the unit in the field name (`IntervalMillis`, `TimeoutSeconds`).

**Field tags on marshaled structs.** Any struct encoded via `encoding/json`, YAML, protobuf, BSON, etc. should have explicit tags. This decouples Go field names from wire format so renames aren't accidental breaking changes.

**Use raw string literals** (`` ` ` ``) for strings containing quotes/backslashes instead of escaping.

**Comment naked bool parameters.** `f(true /* recursive */, false /* dryRun */)`. Better: replace `bool` flag parameters with a typed enum so new states can be added later.

**`Printf`-style function names must end in `f`** (`Wrapf`, `Logf`), so `go vet` can check the format string. Format strings used outside `Printf` should be `const`.

---

## 9. Performance (hot-path only)

These matter only when measurements show they matter. Don't apply preemptively to non-hot code — clarity first.

- **`strconv` over `fmt`** for primitive ↔ string conversion. `strconv.Itoa(n)` is faster and allocates less than `fmt.Sprint(n)`.
- **Don't repeatedly convert the same string to `[]byte`.** Hoist `data := []byte("literal")` out of the loop.
- **Pre-size slices when appending in a loop.** `make([]T, 0, n)` avoids O(log n) reallocations.
- **Use `strings.Builder` for multi-step string concatenation,** not `+=`. For a bounded number of joins, `strings.Join` is fine and clearer.
- **Don't `defer` inside a hot loop** — each `defer` has a small cost, and they all stack up until the enclosing function returns, which can also mean resources accumulate far longer than expected. Close explicitly or wrap the loop body in a function.

---

## 10. Resource Management

**`defer` to clean up** locks, files, connections, HTTP response bodies, rows, transactions. Acquire, check for error, then `defer Close()`. This is the single most common bug-avoidance pattern in Go.

**HTTP response bodies must be closed** — and fully drained if you want connection reuse. `defer resp.Body.Close()` after the error check, never before.

**Check `Close` errors on writers** (files, gzip writers, network connections). A successful `Write` followed by a failed `Close` means data was lost; ignoring the `Close` error hides data loss.

**Don't `defer` inside a long-running loop** for per-iteration resources — they pile up until the surrounding function returns. Extract the loop body into a function so `defer` runs per iteration.

**`exit` only in `main`.** `os.Exit` and `log.Fatal*` skip all pending `defer`s, which means no cleanup. Other functions return errors; `main` is the single place that decides to exit. Ideally `main` has exactly one exit point, built around a `run() error` helper.

---

## 11. HTTP, Servers, and Clients

**Never use `http.DefaultClient` or `http.DefaultTransport` for outbound requests without a timeout.** `http.DefaultClient` has no timeout and will wait forever on a misbehaving peer. Construct a `&http.Client{Timeout: ...}` or set timeouts via `Transport`.

**`http.Server`: set `ReadTimeout`, `ReadHeaderTimeout`, `WriteTimeout`, `IdleTimeout`.** The zero values are unlimited, which is a DoS vector.

**Graceful shutdown.** Production servers wire `Server.Shutdown(ctx)` to a signal handler so in-flight requests finish.

**Always drain and close `resp.Body`** even on non-2xx responses, or the underlying connection won't be returned to the pool.

**Don't use `io/ioutil`** (deprecated since Go 1.16). Use `io` and `os` (`io.ReadAll`, `os.ReadFile`, `os.WriteFile`, `os.MkdirTemp`, `os.CreateTemp`).

---

## 12. Security

- **SQL: always use parameterized queries** (`db.Query("SELECT ... WHERE id = ?", id)`). Never format values into SQL strings with `fmt.Sprintf`.
- **Constant-time comparison for secrets.** Use `crypto/subtle.ConstantTimeCompare` for tokens, HMACs, password hashes, etc. — never `==` or `bytes.Equal`.
- **`math/rand` is not cryptographically secure.** For tokens, keys, session IDs, nonces, use `crypto/rand`.
- **Validate all external input** at the boundary (HTTP handlers, unmarshalers, CLI args). Assume nothing.
- **Don't log secrets, tokens, full request/response bodies, PII.** Structured loggers make it easy to accidentally dump a whole struct — review what goes in log fields.
- **`unsafe` requires justification.** Every use should have a comment explaining the invariant being maintained and why the safe alternative won't work.
- **TLS config:** don't set `InsecureSkipVerify: true` outside of tests. If you must, gate it on a clearly-named config flag and log loudly.

---

## 13. Documentation

- **Doc comments on non-obvious exported identifiers.** If the name and signature don't fully explain behavior, preconditions, or side effects, add a doc comment starting with the identifier's name (`// Parse reads ...`, not `// This function parses ...`). Self-documenting names like `func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error)` don't need a comment restating the obvious.
- **Package comment** on exactly one file in the package (usually `doc.go` or the primary file), starting `// Package foo ...`.
- **Document preconditions, postconditions, and ownership** of slices/maps/channels passed in or returned: is the caller allowed to mutate this? Who closes the channel?

---

## 14. Modern Go Quick Checks

Check `go.mod`'s `go` directive first — that's the minimum version, and anything older is a miss. Flag code that uses an outdated pattern when a cleaner modern equivalent is available at the project's target version.

**Always (no version gate):**
- `time.Since(t)` instead of `time.Now().Sub(t)`; `time.Until(d)` instead of `d.Sub(time.Now())`.
- `errors.Is(err, target)` / `errors.As(err, &target)` for matching wrapped errors — never `err == target` once wrapping is in play (Go 1.13+).

**Go 1.18+:**
- `any` instead of `interface{}`.
- `strings.Cut` / `bytes.Cut` replace the `Index` + slice dance: `k, v, ok := strings.Cut(pair, "=")`.

**Go 1.19+:**
- Typed atomics from `sync/atomic` — `atomic.Bool`, `atomic.Int64`, `atomic.Pointer[T]` — instead of raw `atomic.AddInt64(&x, ...)` or third-party wrappers.

**Go 1.20+:**
- `errors.Join` for returning multiple errors together.
- `strings.CutPrefix` / `strings.CutSuffix` (and `bytes` equivalents) replace `HasPrefix` + `TrimPrefix`: `if rest, ok := strings.CutPrefix(s, "pre:"); ok { ... }`.
- `strings.Clone` / `bytes.Clone` when a small substring is being retained from a large source — forces a copy so the underlying buffer can be GC'd. Matters for long-lived maps or structs that hold parsed pieces of a big input.
- `context.WithCancelCause(parent)` + `context.Cause(ctx)` to attach and later retrieve the reason for cancellation. Makes "why did this context cancel?" debuggable instead of just `context.Canceled`.

**Go 1.21+:**
- `min`, `max`, `clear` built-ins instead of hand-rolled helpers or reassignment loops.
- `slices` and `maps` packages — `slices.Contains`, `slices.Sort`, `slices.Index`, `slices.Clone`, `maps.Clone`, `maps.Copy`, `maps.DeleteFunc` — instead of hand-rolled loops.
- `sync.OnceFunc(fn)` / `sync.OnceValue(fn)` / `sync.OnceValues(fn)` replace manual `sync.Once` wrappers for lazy initialization.
- `context.AfterFunc(ctx, cleanup)` runs a function when `ctx` is cancelled; cleaner than a goroutine selecting on `<-ctx.Done()` just to call one function.

**Go 1.22+:**
- `cmp.Or(a, b, c, "default")` returns the first non-zero value — replaces chains of `if x == "" { x = y }`.
- `for i := range N` — integer range — is valid and cleaner than `for i := 0; i < N; i++` when you don't need the index for anything else.
- Drop range-loop-variable shadow workarounds (`v := v` before a goroutine); each iteration now has its own copy.
- **Enhanced `http.ServeMux` patterns:** `mux.HandleFunc("GET /users/{id}", h)` with method + path variables. `r.PathValue("id")` reads path params. For many services this removes the need for chi/gorilla/mux entirely.

**Go 1.23+:**
- Iterator forms: `slices.Collect(maps.Keys(m))`, `slices.Sorted(maps.Keys(m))`, and `for k := range maps.Keys(m)` replace append-in-a-loop patterns.
- `time.Tick` is safe to use freely now — the GC can recover unreferenced tickers, so `time.NewTicker` + `defer t.Stop()` is no longer mandatory when the ticker's lifetime matches its goroutine's.

**Go 1.24+:**
- **`omitzero` over `omitempty` in JSON struct tags.** This is a bug-prevention change, not a style one. `omitempty` silently does the wrong thing for `time.Time`, `time.Duration`, structs, and arrays — their zero values aren't "empty" to the JSON encoder, so the field is always emitted. `omitzero` checks actual zero value. Flag any `omitempty` on these types at Go 1.24+.
- Iterator split/fields: `strings.SplitSeq`, `strings.FieldsSeq`, `bytes.SplitSeq`, `bytes.FieldsSeq` — iterate without allocating the intermediate slice. Use them whenever the result is only consumed in a `for range`.

**Go 1.25+:**
- `wg.Go(fn)` on `sync.WaitGroup` replaces the `wg.Add(1)` + `go func() { defer wg.Done(); ... }()` pattern. Harder to get wrong. (This complements `errgroup.Group.Go` from `x/sync` — use errgroup when you need error propagation or context cancellation, stdlib `wg.Go` when you don't.)

**Go 1.26+:**
- `new(expr)` returns a pointer to any value with inferred type: `new(30)` → `*int`, `new("s")` → `*string`, `new(time.Minute)` → `*time.Duration`. Replaces the `x := 30; p := &x` dance common for optional-pointer struct fields.
- `errors.AsType[T](err)` returns `(T, bool)` — cleaner and type-safer than `var t T; errors.As(err, &t)`.

---

## 15. Tooling the Reviewer Should Recommend

If the codebase isn't already running these in CI, suggest:

- **`gofmt` / `goimports`** — formatting and import grouping, non-negotiable.
- **`go vet`** — stdlib static checks, shipped with Go.
- **`staticcheck`** — catches a large class of real bugs (ineffective assignments, unreachable code, misused APIs).
- **`golangci-lint`** — runs many linters fast. Minimum recommended set: `errcheck`, `govet`, `staticcheck`, `revive`, `ineffassign`, `unused`, `gosimple`.
- **`go test -race`** — any codebase with goroutines should run tests with the race detector in CI.
- **`govulncheck`** — scans for known CVEs in dependencies.

---

## Review Procedure

1. **Read `go.mod` first** to learn the Go version — it determines which modern-Go checks apply.
2. **Skim for structure:** how the package is laid out, what's exported, where the concurrency lives.
3. **Walk each file** top to bottom, applying sections 1–14 above. Prioritize in this order:
   1. Correctness (races, leaked goroutines, unchecked errors, context misuse, resource leaks, panics).
   2. API design (interface shape, receiver types, exported surface, naming).
   3. Idiom violations (initialization patterns, error wrapping, nil slices, defer).
   4. Style nits (imports, grouping, line length).
4. **Group findings by severity** in the output — don't bury a race condition under naming advice.
5. **Number every finding sequentially** (1, 2, 3, …) across the entire review so the user can respond to specific items by number.
6. **Quote the exact line**, explain the rule, show the fix. One issue per finding; don't pack unrelated concerns.
7. **Note what looks good** briefly when the code is well-written — reviewers who only report negatives are harder to trust.

