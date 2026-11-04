# Go performance, concurrency, and reliability audit

Date: 2026-11-02
Module: `norviq-api` (Go 1.27.3)
Scope: this repository. Findings and an implementation plan.
Status: see [Implementation status](#implementation-status-2026-11-04). The audit itself changed nothing.

Staff review (2026-11-02): the roadmap in section B is the original proposal. It is not the work to implement. The approved work is [Revised roadmap](#revised-roadmap). Anything marked REVISE, MEASURE FIRST, or REJECT stays unshipped until that section says otherwise.

This is a wire-compatible port of the Swift/Vapor API. Vapor names, Redis key formats, and the fail-open job lock are intentional parity. A change that alters those needs an explicit decision. `docs/MIGRATION.md` records the deviations that were already chosen.

## Implementation status (2026-11-04)

**Live state: production runs this work.** `7f13fdf` was promoted to production on 2026-11-04 (infra #542), replacing `b4d3015`. It was healthy after a 5-minute check: no restarts, no job lock warnings, and one error, `FMP request failed`, which the previous image also logged. The new shutdown drain first runs at the next deploy, and the Telegram log had no turns to report yet.

Revised roadmap items 1 to 10 are in the code. Item 2 needed one correction: a heartbeat database error marked the run `failed`, and the plan says it stays leased and reclaimable. `TestHeartbeatErrorCancelsAndLeavesTheRunReclaimable` failed before that fix.

Item 5 and item 10 use a longer wait than the plan's 15 seconds. HTTP gets 15 seconds, then every drain shares what is left until 24 seconds, because 20 + 15 seconds of the old budget overran the 30 second grace period. Nothing in this list claims a measured speedup.

Review of that work fixed three defects, each with a test that failed first: a tick no longer gets skipped when Postgres dropped the lock session (`TestTickRunsAfterTheBackendIsKilled`), a hung lease renewal now stops the simulation after 20 seconds instead of stalling the worker (`TestHungRenewalCancelsTheSimulation`), and `job_duration_seconds` has buckets up to 3600 seconds, so long ticks no longer all land in `+Inf` (`TestJobDurationsResolveLongTicks`).

**Outside the ten items, kept.** `d988c4a` also shipped two idempotency changes the roadmap does not list. The 503 on a Redis `GET` error has an APPROVE verdict in P0.2, so its absence from the roadmap reads as an omission. The replay-header filter has no verdict. It stays because removing it brings back a replay that labels a plain body gzip, and `TestReplayHeaderKeepsOnlyHandlerHeaders` covers it. Rollback for either is a revert.

**Accepted risk: one shared lock session.** Losing that connection releases every lock the process holds, and running ticks are not told. At one replica, only a rollout's surge pod could take a lock. Revisit this before raising the replica count.

### Validation results

| Check | Result | Evidence |
|---|---|---|
| `go test -race` for `joblock`, billing, scenarios | Pass, no data race | Whole module, disposable Postgres 16 and Redis |
| Production `SCENARIO_PLANNING_ENABLED` | `"true"` | Live deployment |
| Production `DATABASE_MAX_CONNECTIONS` | Unset, so 20 | Live deployment. `DATABASE_MAX_CONNECTIONS_PER_EVENT_LOOP` is a Vapor leftover that v2 ignores |
| Postgres `max_connections` | 100 | Live `SHOW`, Postgres 18.4. The instance is shared by production and staging |
| API replicas | 1, but `maxSurge: 1` | Live deployment |
| Provider call counter | Added: `market_quote_provider_calls_total{provider}` | Counts calls after every cache missed. No production reading yet |
| Telegram in-flight log | Added: `telegram_turn_started in_flight=N` | Info level, no chat or user id. No production reading yet |
| Clients sending a key in parallel | iOS no. Web possible. MCP not shown | See below |
| Staging `EXPLAIN` and row counts | Done, inconclusive alone | Every plan is under 0.2 ms, but staging holds 1 expense, 0 scenario runs and 0 targets. See below |
| Production row counts | Done, **P1.2 closed** | Approved by the owner on 2026-11-04 as an exception to "never production": read-only aggregates, no `EXPLAIN`. See below |

**Connection budget.** One pod is now 21 connections: 20 in the pool and the lock session. Two production pods during a rollout (42), staging (21) and about 10 others come to about 73 of 100.

**Export boot sweep stays out.** One replica is not one process. `maxSurge: 1` runs two pods during every rollout, so a sweep in the new pod would fail live exports in the old one.

**Parallel `Idempotency-Key`.** iOS guards the action with `isSubmitting` and reuses the key sequentially. Web forms carry a hidden key, and the pilot-follow comment expects a double submit, so two parallel requests are possible there. No trace shows how often. Per the review above, this does not justify the NX protocol. MCP sends one key per call and nothing shows it repeating a key concurrently.

**Keys the API ignores.** Only banks, brokers, expense import, pilots and receipt import mount the idempotency middleware. MCP sends keys to `/v1/expenses`, `/v1/transactions`, `/v1/targets`, `/v1/watchlist`, `/v1/research`, `/v1/goals` and `/v1/budget/*`, and web sends one to `/v1/terminal-positions`. None of those replays, so a retried write there can double-write, despite the comment in `norviq-mcp/internal/api/client.go`. Not fixed here: it is outside the approved list.

### Staging plans

Run read-only with a 15 second statement timeout, using `EXPLAIN (ANALYZE, BUFFERS, SERIALIZE)`. Staging has 31 users, 1 expense, 0 scenario runs, 0 scenario definitions, 0 targets, 27 stocks, 61 watchlist items and 0 news items. The slowest query took 0.133 ms. That is what a nearly empty table costs, so it does not show that these queries are cheap under load.

### Production row counts

The plan says "never production", so staging could not settle P1.2. The owner approved one exception on 2026-11-04: read-only aggregates with a 15 second statement timeout, printing no user data or ids.

Production has 38 users, 1 expense, 0 scenario runs, 0 scenario definitions, 0 targets, 23 stocks, 34 watchlist items and 0 news items. The largest single-user expense list is 184 bytes.

**P1.2 is closed with no change: no query rewrite, no index.** There is no cost to reduce today. Quote batching, the symbol indexes and the targets index have the same answer for now, since `targets` and `news_items` are empty. Re-run these counts before reopening any of them.

### Synthetic query plans

These are not measurements of Norviq's data. The row counts are numbers chosen to stress the queries, on an unloaded laptop with Postgres 16 (production runs 18). They test only whether the plan shape is dangerous at that size.

| Query | Synthetic rows | Plan | Execution |
|---|---|---|---|
| `ListExpensesRange`, heavy user, nil dates | 100,000 of 200,000 | Seq scan, half the table matches | 45 ms |
| `ListExpensesRange`, typical user | 500 | Index scan | 0.5 ms |
| `ListScenarioRuns`, heavy user | 2,000 of 6,000 | Seq scan and sort | 0.8 ms |
| `ListScenarioDefinitions`, heavy user | 500 of 4,500 | Bitmap index scan and sort | 0.1 ms |
| `UnresolvedTargets` | 10,000 of 100,000 | Seq scan | 5.5 ms |
| `StockOwnersForSymbols`, 30 symbols | 1,200 of 200,000 | Parallel seq scan | 29 ms |

The plans are cheap at these sizes, so none of them argues for an index. `EXPLAIN` on Postgres 16 does not detoast or send the rows, and that is where the cost went: reading the same rows with `COPY` produced 222 MB in 1.1 to 6.0 seconds for the heavy expense user, whose metadata was deliberately 2 KB per row, 39 MB in about 0.3 seconds for the heavy run list, and 80 KB for the typical user. A 222 MB response would threaten the pod's 450 MiB memory limit. No real user is near that today (see production row counts above).

## Adversarial review of the plan

This pass does not look for new optimizations. It asks whether each recommendation in section B is evidenced, whether it would help a realistic workload, and whether the design can make correctness, memory, or the database worse. No new profiles, race runs, or query plans were taken. Verdicts use the code already cited above plus a second read of the billing tax-pack path, the scenario cancel statement, and the feature flags named below.

Almost every original P0 item is a correctness fix. None of them has a measured latency or throughput gain. Approving one means the failure mode is visible in the source and the revised design is small. It does not mean the change will make the API faster.

### Verdicts

| Item | Verdict | Why, in one line |
|---|---|---|
| P0.1 Billing, one transaction, no new status | APPROVE | The skip-on-retry bug is in the source. One local transaction fixes it. |
| P0.1 `received`/`applied` status and reapply-on-conflict | REJECT | A unique violation already means the other transaction committed the entitlement. A second apply path can double-apply. |
| P0.2 Idempotency reservation, poll, and 409 | REJECT | The race is real. This recovery protocol is a new distributed system with a crash window the plan does not close. |
| P0.2 Redis `GET` error returns 503 before the handler | APPROVE | Matches the rate limiter. The handler has not run, so a 503 cannot hide a committed write. |
| P0.3 Export shutdown context and conditional ready update | APPROVE | The detached goroutine and the closed-pool race are in the source. |
| P0.3 Timer that fails stale `pending`, and a per-user cap | REJECT | The timer can fail a live export. The cap changes a product rule that was not the bug. |
| P0.4 One new Postgres session per overlapping tick | REJECT | Moves exhaustion from the pool of 20 onto server `max_connections`, which is unknown. Fail-open on that dial then runs unlocked ticks. |
| P0.4 One shared lock session, shutdown log, cancel check | APPROVE | Same deadlock removal, one extra session, mutex only around the lock statements. |
| P0.5 Scenario fence on owner and `state = 'running'` | APPROVE | Renew, complete, and fail update by id. The predicate is the whole fix. |
| P0.6 Attempt column and a one-retry state machine | REJECT | A schema and a new lifecycle for a crash window that a reorder covers. |
| P0.6 Mark the target before send; record earnings after a successful send | APPROVE | Stops the 300-second duplicate and the "failure counted as sent" path. No new column. |
| P1.1 Pool, job, and scenario metrics | APPROVE | Scrape-time stats. No request-path change. This is how the rejected speedups get judged later. |
| P1.2 Dashboard and scenario query rewrites | MEASURE FIRST | The shape is real. The cost is not. An `EXPLAIN` can end this item. |
| P1.3 Repeated Enable Banking continuation key | APPROVE | A repeated key is not progress. The numeric page cap is not approved. |
| P1.3 Numeric page cap | MEASURE FIRST | No observed page count. A low cap truncates a finite history. |
| P1.4 Plaid webhook returns 500 | REJECT | No error taxonomy. A permanent failure becomes Plaid's retry loop. |
| P1.4 Log the swallowed Plaid `Sync` error, and stop the job loop on `ctx` | APPROVE | The log is the whole safe change. The `ctx` check is part of the shutdown work. |
| P1.5 Process context for achievement hooks | APPROVE | `Evaluate` already rolls back on cancel. The flag defaults off. The wiring is still the right shutdown behavior. |
| P2.1 Batched quotes and `singleflight` on `Quote` | MEASURE FIRST | Cache TTL is 20 seconds. A batch call fails a whole chunk. No miss counter exists. |
| P2.2 `IdleTimeout` of 120 seconds | APPROVE | Closes idle keep-alives. Does not apply to an in-flight SSE response. |
| P2.2 `ReadTimeout` | MEASURE FIRST | It covers the body. A slow upload dies, and no latency histogram picked the number. |
| P2.2 `WriteTimeout` | REJECT | Cuts SSE. The plan already said to leave it unset. |
| P2.3 Group fan-out uses the process context | APPROVE | Same shutdown race as achievements, smaller scope. |
| P2.3 Telegram cap that returns 200 and drops the update | REJECT | Drops a user message. No flood has been observed. A 429 makes Telegram retry. |
| P2.4 Split the achievement transaction | MEASURE FIRST | Duration is unknown. The split can double-award XP. The current lock is per user, on purpose. |
| P3.1 Symbol indexes | MEASURE FIRST | No plan, no row count. The index is a guess. |
| P3.2 Reclaim of `pending` notification deliveries | REJECT | `pending` means a send may be in flight. Flipping it to `failed` makes `claim` send again. |
| P3.3 Partial index on untriggered targets | MEASURE FIRST | The tick's cost is the quote calls, not a proven sequential scan. |

### P0.1 Billing transaction — APPROVE the narrow form

The problem is demonstrated. `InsertBillingEvent` autocommits (`webhook.go:259-264`, `billing.sql:7-9`), and a later `BillingEventExists` returns success (`webhook.go:238-240`). That is a lost entitlement update, not a hypothesis. It does not make the webhook faster. The transaction is a handful of local writes. `GrantTaxPack` and `RevokeTaxPack` use `s.q` only (`taxpacks.go:54-78`). There is no HTTP on that path. Passing `svc.WithDB(tx)` into the whole of `processEvent`, including `processTaxPackEvent`, keeps the tax pack inside the same transaction. Calling the original service would leave the pack on the pool and recreate the bug.

The plan's second design, a `received` versus `applied` column plus a reapply when the unique index conflicts, is the risky one. Under PostgreSQL, the loser of two inserts waits for the winner and then gets a unique violation. If the winner's transaction included the entitlement, the violation means the work is done. Reapplying it can grant, revoke, or move a subscription twice. Do not add the column.

Unknown event types and unknown users still return 200 after the insert. Those returns stay inside the transaction so the "we deliberately did nothing" decision commits with the event row.

Deadlock risk is low if this transaction is the only one that writes `billing_events` and then subscriptions. Two deliveries of the same event block on the unique index. They do not lock each other in the opposite order. Do not add statements that lock a user's entitlement row before the event insert.

Tests that are required: a failure of `upsertEntitlement` rolls the event back, and the redelivery applies it once. Two overlapping deliveries leave one event row and one entitlement write. An unknown `app_user_id` commits the event and does not grant Pro. Rollback is reverting the commit. There is no migration.

### P0.2 Idempotency protocol — REJECT; the 503-before-handler slice is APPROVE

The check-then-act race is in `ratelimit.go:110-134`. Two requests that both miss the key both run `next`. That does not require a production trace. What is not demonstrated is that any client sends the same key on two connections at once.

The proposed protocol does not close the failure it names. A handler can commit and then return an error. Deleting the in-progress key, which the plan does on failure, lets the retry commit again. Keeping the key for 24 hours blocks a legitimate retry of a failed request. A short lease lets a slow bank handler be entered twice when the lease expires mid-request. Polling Redis from the peer holds an HTTP connection and a goroutine per waiter. Returning 409 is a new status on routes whose clients were written against Vapor's replay behavior. The plan then adds "Postgres idempotency for banking and brokers" without a design. That is a second project.

Redis `SET` failing after a successful handler must not become a 503. The client would retry a write that succeeded. The only safe 503 is a Redis error before `next` is called. The rate limiter on these same routes already fails closed (`ratelimit.go:55-59`), so a total Redis outage already rejects the request. The new 503 covers the narrower case where the rate-limit script worked and the idempotency `GET` did not.

The parallel race stays. It is not approved to "fix" it with this protocol. Validation, not code: check whether the iOS client sends `Idempotency-Key` and whether it retries in parallel. Until that is known, do not invent a 409 contract.

### P0.3 Exports — APPROVE shutdown only

`generate(context.Background())` and a `WaitGroup` that `serve` never waits on are in `feature.go:266-276` and `main.go:216-218`. A deploy can close the pool under that goroutine. That is demonstrated.

The stale-pending timer is not safe. A large export can still be running when the budget expires. The cleaner then marks it `failed` while `MarkExportReady` is about to run. The user sees a failed export whose file exists, or a ready export the cleaner already decided was dead. The per-user single-flight rule is a product change. The bug report did not show two exports corrupting each other.

Approved shape:

- Store the process context and pass it to `generate`. `exportRows` already takes `ctx`.
- On shutdown, cancel that context and wait up to the same 15 seconds as the jobs, and log if the wait expires.
- `MarkExportReady` updates `WHERE id = $1 AND status = 'pending'` so a late writer cannot overwrite `failed`.
- Do not add a per-user cap.
- Do not add a timer. A boot-time sweep that fails every `pending` row is safe only while one API process owns the table. The repo documents one replica for realtime (`docs/MIGRATION.md:52-58`) and does not prove one replica for this process. Leave the sweep out until that is confirmed. Crashed rows may stay `pending`. That is the current behavior, and it is better than failing a live export on a second replica.

This does not reduce the memory of holding a full export. It stops the goroutine from outliving the pool.

### P0.4 Lock connections — REJECT one connection per tick; APPROVE one shared session

The deadlock shape is demonstrated: `Acquire` holds a pool connection across `body`, and `body` uses the pool (`joblock.go:23-44`). `Acquire` blocks until the process context ends. It does not fail open on a full pool. Fail-open runs when the dial or the lock query returns an error.

Whether 20 ticks overlap is not demonstrated. Several jobs never take the lock unless a flag is on, and those flags default off: achievements (`achievementhooks/hooks.go:73`), digest (`digest/config.go:55`), pilots (`pilots/feature.go:41`), market brief (`marketbrief/feature.go:44`), daily board posts (`boards/daily_job.go:653`), scenario planning (`scenarios/feature.go:115`). Rebalancing alerts and goal planning default on. A stall is possible and has not been observed. Shipping N extra server connections to prevent an unobserved stall can hit `max_connections` instead. If that dial fails and the code fail-opens, every tick runs with no lock while the database is up. That is a duplicate-send incident created by the fix.

PostgreSQL session advisory locks can stack on one session. One `pgx.Conn` for the process can hold every lock this process owns. `pgx.Conn` is not safe for concurrent use, so a mutex covers only `pg_try_advisory_lock` and `pg_advisory_unlock`. The mutex must be released before `body`. Holding it across `body` serializes every job and deadlocks if `body` ever needs the same mutex.

Approved behavior when the lock session cannot be dialed:

- If the pool itself cannot answer a ping, keep today's fail-open. The tick's own queries will fail. This is the Vapor outage case.
- If the pool is up and the lock session is not, skip the tick and log. Do not run it unlocked.

That second branch is a deliberate narrowing of fail-open. Record it in `docs/MIGRATION.md` when the code changes. It is not a license to delete fail-open.

Also approved, and independent: `Jobs.Wait` logs the job names still inside `Run` at 15 seconds. `RunAsLeader` returns before `body` when `ctx` is already canceled. The banking connection loop returns when `ctx` is done, between connections, not inside a provider call. Do not raise `DATABASE_MAX_CONNECTIONS`.

Tests: two bodies against a pool of two both finish while sharing one lock session. A race test covers concurrent lock and unlock. A canceled context does not enter `body`. A live pool and a dead lock session skip the tick. Rollback is returning `RunAsLeader` to `pool.Acquire`. No schema change.

Memory and GC are unchanged. The extra session is one connection, not a cache.

### P0.5 Scenario fence — APPROVE with a tighter predicate

`RenewScenarioRunLease`, `CompleteScenarioRun`, and `FailScenarioRun` update by primary key (`runs.sql:99-114`). The heartbeat returns without canceling the simulation when its read fails (`worker.go:274-276`). The fail path checks the owner in Go and then writes with no owner predicate (`worker.go:165-172`). That is a real lost update when the flag is on. The flag defaults off, so this is not today's production path unless the environment says otherwise. The fix is still small enough to land before anyone turns the flag on. It does not make simulations faster.

Adding only `lease_owner` is not enough. `CompleteScenarioRun` sets `state = 'completed'`. `CancelScenarioRun` sets `state = 'cancelled'` and does not clear `lease_owner` (`runs.sql:75-77`). A complete that matches the owner and ignores state overwrites a user cancel. The predicate is `WHERE id = $1 AND lease_owner = $2 AND state = 'running'`.

A zero-row renew cancels the simulation and does not schedule achievements. A zero-row complete or fail returns. A heartbeat read error cancels the simulation. The run stays leased until expiry and another worker can claim it. That aborts a healthy run during a database blip. The alternative is the current bug: the worker keeps going with no fence. Take the abort.

Do not wrap the tick in `RunAsLeader`. The claim is already the concurrency control.

Tests: the owner loses the lease, then complete affects zero rows and schedules nothing. A cancel between the Go check and the update is not overwritten. A heartbeat read error cancels. Rollback reverts the three statements.

### P0.6 Target and earnings — APPROVE a reorder; REJECT the state machine

The target poller sends, then marks (`jobs.go:192-197`). A failed mark leaves `alert_triggered_at` null, and the next poll, default 300 seconds, sends again. The earnings poller inserts the delivery row before APNs (`jobs.go:405-421`), so a failed send is never retried. Both orders are visible in the source. How often the mark fails is unmeasured. The duplicate, when the mark fails, repeats on every tick until the mark works. That is worse than losing one push in a crash.

The plan's attempt column, `failed` state, and single retry are a migration and a new lifecycle. They are not required to stop either bug.

Approved:

- In the existing `markTriggered` transaction, set `alert_triggered_at` before `Send`. Inbox publish stays first and stays deduped. A crash after the mark and before the send loses that push. It does not send every five minutes.
- Insert the earnings delivery row only when `Delivered > 0`. A failed send is retried on the next poll. A crash after a successful send and before the insert can send one extra push. That window is one crash, not a permanent skip.

Do not batch quotes in this change. A batch error would skip every target. Tests: a failed mark is unreachable after the reorder because the mark commits first; assert a second poll does not send. `Delivered == 0` leaves no earnings row and the next poll can send. Rollback reverts the order. Rows already marked stay marked.

### P1.1 Metrics — APPROVE

`SetScenarioQueueDepth` is never called from the worker. Pool stats are not exported. This does not speed anything up. It is the measurement the rest of this review keeps demanding. Read `pgxpool.Stat()` when Prometheus scrapes, not on each request. Label job duration by job name only. Thirty-eight jobs is a bounded label set. Do not label by SQL text or user id. Prometheus stays off until an operator enables it. Rollback is reverting the metrics code.

### P1.2 Query rewrites — MEASURE FIRST

`ListExpensesRange` with nil dates and the unbounded scenario lists are real SQL. No row count and no plan exists. A user with a short history will not notice a rewrite, and a rewrite that drops columns or shortens the window can change `budgetStreak` (`dashboard/service.go:151-168`) and the scenario list JSON. That is a wire change sold as a performance fix.

Validation only, on a staging copy, never production: `EXPLAIN (ANALYZE, BUFFERS)` and a count for one heavy user and one typical user. If buffer reads are small, stop. Do not add an index. The existing `(user_id, occurred_on)` and `(user_id, created_at)` indexes already match the predicates.

### P1.3 Enable Banking — APPROVE the repeated-key guard only

The loop ends only on an empty continuation key (`enablebanking.go:195-224`). A provider that repeats a key grows `all` until shutdown and holds the leader lock. That bound is missing in the source. Whether any bank repeats a key is unmeasured. Treating a repeat as an error is still the right guard: a continuation key that does not change is not a next page. Return the error and discard the partial slice. Do not invent a cursor. Do not add a numeric cap until a real sync reports its page count. A cap chosen now can cut a long, finite history.

The Plaid webhook currently ignores `Sync` errors. This guard still stops the loop. The webhook change is the log in the next item, not a 500.

### P1.4 Plaid status codes — REJECT; the log is APPROVE

`_, _ = p.Sync` then 200 is demonstrated (`webhook.go:199`). Returning 500 makes Plaid retry. The plan says "classify errors" and does not list a single permanent or transient code. A wrong class turns a deterministic bug into a retry storm against Plaid and the database. The 6 hour poller remains the backstop. Log the connection id and the error. Leave the status at 200 until an error map is written down and reviewed on its own.

### P1.5 Achievement context — APPROVE

`Register` stores `context.Background()` (`achievements/feature.go:70-74`). `Schedule` is a no-op while `ACHIEVEMENTS_ENABLED` is off (`hooks.go:16`, `:73`). When the flag is on, a shutdown race can call `Evaluate` after `pool.Close`. Pass the process context into `SetEvaluator`. Cancel it on shutdown. Wait for the hooks only until the 15 second deadline, then log and continue. `Evaluate` already defers `Rollback`. Do not add a worker pool. Do not split the transaction here.

### P2.1 Quote batching — MEASURE FIRST

`Quote` already checks Redis and `quote_cache` with a 20 second TTL (`service.go:168-188`). A stampede exists only on a miss. `StockQuotes` returns `CryptoQuoteResponse`, not `Quote`. Pointing the target poller at it changes fallback and stale-cache behavior in the same change as P0.6. A batch failure fails every symbol in the chunk. Today's code skips one symbol (`jobs.go:162-164`).

`singleflight` on `Quote` shares one caller's context unless the implementation is careful. A cancel from the first waiter cancels the rest. That is a new cancellation bug.

Validation only: a counter of provider HTTP calls inside `fetchQuote`. No behavior change. Revisit batching only if that counter shows misses dominating a tick.

### P2.2 Timeouts — APPROVE `IdleTimeout` only

`ReadTimeout` and `IdleTimeout` are both zero (`main.go:189-193`). With both zero, keep-alive connections have no idle limit. `IdleTimeout` is the wait for the next request. It does not abort a handler that is already writing an SSE stream. Set it to 120 seconds. Clients reconnect. That is ordinary HTTP. It is not a measured speedup.

`ReadTimeout` includes reading the body. The server allows a 10 MB body, and some routes lift that limit. A number taken from a header timeout will cut slow uploads. Leave `ReadTimeout` unset until a histogram exists. Leave `WriteTimeout` unset. Setting `ReadTimeout` without `IdleTimeout` would also make `IdleTimeout` inherit `ReadTimeout`. Do not set one as a side effect of the other.

### P2.3 Fan-out — APPROVE the group context; REJECT the Telegram drop

Group `detach` uses `context.Background()` and production `Wait` is nil (`groups/notifier.go:72-80`). Use the process context and cancel it on shutdown. A missed push during shutdown is the accepted cost. There is no evidence of a member-list pile-up, so do not add a semaphore.

Telegram turns are capped at 360 seconds and drained on shutdown (`telegram_bridge.go:91-105`, `:154-160`). They are not capped while the process is up. A cap that returns 200 and drops the update loses a message with no retry. A non-200 makes Telegram deliver it again and stacks more turns. No flood is in evidence. Log the in-flight count if a metric is cheap. Do not drop updates.

### P2.4 Achievement transaction split — MEASURE FIRST

`Evaluate` holds `FOR UPDATE` across `measure`, including price history (`service.go:71-105`, `metrics.go:379-389`). The lock is one user's evaluation row. Other users are not blocked. The comment at `service.go:60-62` says the backfill flag is read and cleared under that lock on purpose. Splitting `measure` out of the transaction without a second read of the flag can unlock twice or skip a backfill. Duration is unknown. Do not split it. A duration log is optional and only useful while the flag is on.

### P3 — no implementation

Symbol indexes, a targets partial index, and a notification reaper are not approved.

The notification reaper is the one to refuse outright. `claim` treats `pending` as owned (`inbox.go:211-214`) because a send is in progress. Aging that row into `failed` makes the next `claim` send again. Digest and market brief are at-most-once on purpose. Leave them.

### What this review does to performance expectations

The approved list does not claim a throughput or latency gain. The lock-session change removes a stall that may be rare. The Enable Banking guard stops an unbounded allocation if a key repeats. The metrics exist so a later change can be rejected with numbers. Query rewrites, quote batching, indexes, and `ReadTimeout` stay off the list until the validation section produces a plan or a counter.

## Revised roadmap

Implement only these items, in this order. Each one has a rollback that is a code revert. None of them adds an index, a queue, a worker pool, or a Redis pub/sub bus.

1. **Billing, one transaction.** `WithDB(tx)` for the event insert and every entitlement write, including tax packs. Unique violation returns 200 and does not reapply. Tests: rollback on entitlement failure, one winner under two deliveries, unknown user commits no grant. No migration.

2. **Scenario fence.** `WHERE id AND lease_owner AND state = 'running'` on renew, complete, and fail. Zero rows cancels the sim and skips achievements. Heartbeat read error cancels. Tests on the disposable database with `-race` if `TEST_DATABASE_URL` is set. Do not add a job lock.

3. **Target mark before send. Earnings row after `Delivered > 0`.** No new column. Tests for a second poll and for a failed earnings send. Do not batch quotes.

4. **One shared advisory-lock session.** Mutex around lock and unlock only. Skip the tick when the pool is healthy and the lock session is not. Keep fail-open when the pool cannot ping. Log it as a Vapor deviation when the code lands. `Jobs.Wait` logs names at 15 seconds. Banking's loop returns on `ctx.Done()` between connections. Tests: pool of two, concurrent leaders, canceled context, lock session down.

5. **Export shutdown.** Process context, 15 second wait with a log, `MarkExportReady` conditional on `pending`. No timer, no per-user cap, no boot sweep until the deployment is confirmed to be one process.

6. **Metrics, scrape time only.** Pool stats, job-duration histogram by job name, `SetScenarioQueueDepth` from the worker. No per-request `Stat()` call.

7. **Enable Banking repeated key.** Return an error. Do not persist a partial page. No numeric cap.

8. **Plaid log.** Log connection id and sync error. Keep HTTP 200.

9. **`IdleTimeout` 120 seconds** on the API server. Do not set `ReadTimeout` or `WriteTimeout`.

10. **Process context** for achievement `SetEvaluator` and group `detach`. Cancel on shutdown. Wait only until the 15 second deadline.

### Validation work, not implementation

Do these before any item marked MEASURE FIRST is redesigned. They are allowed to be read-only.

- Staging `EXPLAIN (ANALYZE, BUFFERS)` and row counts for `ListExpensesRange` with nil dates, `ListScenarioRuns`, `ListScenarioDefinitions`, `UnresolvedTargets`, and the news `symbol = ANY` lookups. Stop when the plan is cheap.
- Read the production value of `SCENARIO_PLANNING_ENABLED`, `DATABASE_MAX_CONNECTIONS`, Postgres `max_connections`, and the API replica count. The lock-session change is still approved without those numbers because it adds one session, not N. The export boot sweep is not.
- `go test -race` for `joblock`, the billing webhook, and the scenario worker, against the disposable template database. Not against production.
- A counter of provider calls inside `fetchQuote`, and a log of Telegram in-flight turns. No change to either call path.
- A check of whether clients send `Idempotency-Key` on parallel requests. No NX protocol until that check, and not the protocol in P0.2 even then, without a new design review.

### Still rejected after this review

One Postgres connection per job tick. Idempotency polling, 409, and a Postgres idempotency project. Export timers and per-user caps. Notification pending reaper. Plaid HTTP 500. Telegram drop. `WriteTimeout`. `sync.Pool`. Raising the pool size as the lock fix. Quote batching, `singleflight` on `Quote`, query rewrites, and new indexes, until the validation work says the cost is real.

## How this audit was done

Read paths: process entry (`cmd/api/main.go`), config and the pgx DSN, the job registry, advisory locks, HTTP middleware, the billing webhook, idempotency, data export, watchlist pollers, banking sync and webhooks, news ingestion, the scenario worker, market-data quotes, the inbox, achievement evaluation, goal planning, the realtime bus, and the baseline migration indexes those queries use.

Static analysis from the earlier pass of this same audit: `go vet ./...` exited 0, and `staticcheck` 2026.2.2 exited 0. Neither was repeated while this file was written. `go test`, `go test -race`, benchmarks, pprof, and `EXPLAIN` were not run. `TEST_DATABASE_URL` was unset, and Postgres on `127.0.0.1:55499` was not accepting connections. Docker and production were left alone. There is no `func Benchmark` in the tree.

A clean `go vet` and a clean `staticcheck` do not show that the process is race-free. The race detector was not run. The absence of a data-race report below means the shared state that was read is guarded in the source, not that a race run passed.

Every speed, row-count, and sequential-scan claim below is a hypothesis until a profile or a query plan exists. File and line references were read in this audit. No measurement in this document was invented.

## Architecture

One Echo v5 process (`cmd/api`) serves HTTP. Persistence is PostgreSQL through pgx v5.12 and sqlc, with goose migrations. Redis (go-redis v9) is optional at boot: a missing `REDIS_URL` logs a warning and continues (`cmd/api/main.go:132-142`). Production rate limiting then fails closed (`internal/middleware/ratelimit.go:47-59`).

`internal/features/features.go:259-321` registers 38 background jobs. `StartJobs` starts only the names in `V2_JOBS`, or every job when that variable is `all`. The default is none. `docs/MIGRATION.md:44-49` says production and staging set `V2_JOBS=all`.

Leader election is `joblock.RunAsLeader`: `pg_try_advisory_lock(hashtext(name))` on a pool connection held for the whole tick (`internal/platform/joblock/joblock.go:22-45`). There are 29 production call sites. The scenario worker deliberately skips it and claims rows with `FOR UPDATE SKIP LOCKED` (`internal/features/scenarios/worker.go:41-45`, `db/queries/scenarios/runs.sql:79-94`).

The pool DSN sets `sslmode` and `pool_max_conns` only (`internal/config/config.go:67-70`). `DATABASE_MAX_CONNECTIONS` defaults to 20. pgxpool's own defaults remain: max lifetime 1 hour, idle time 30 minutes, health check 1 minute (`vendor/github.com/jackc/pgx/v5/pgxpool/pool.go:19-24`). The application DSN sets no `statement_timeout` and no `idle_in_transaction_session_timeout`.

Shutdown (`cmd/api/main.go:208-218`): the process context cancels, HTTP `Shutdown` has 20 seconds, realtime streams are finished from `RegisterOnShutdown`, `assistant.DrainTelegramTurns()` waits, then `Jobs.Wait` gives up after 15 seconds with no log (`internal/features/features.go:409-416`). `defer pool.Close()` runs as `serve` returns.

The HTTP server sets `ReadHeaderTimeout: 10s` and leaves `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` at zero (`cmd/api/main.go:189-193`). Write timeout is omitted on purpose for SSE. The pprof listener documents the same choice for `/profile` and `/trace` (`internal/platform/pprof/pprof.go:37-38`). Middleware (`internal/server/server.go:83-105`): optional Prometheus, CORS, security headers, gzip from 1024 bytes, request log, a 10 MB body limit, recover.

Realtime is an in-process bus. `internal/platform/realtime/bus.go:1-8` and `docs/MIGRATION.md:52-58` say it is correct with one API replica. Nothing is published to Redis. Health treats a missing Redis as degraded in production so readiness does not take that single replica down (`internal/server/health_core.go:35-44`). The replica count actually running in production is not in this repository.

Market data goes to FMP, Finnhub, and IBKR. `Quote` checks Redis (`market:quote:<provider>:<symbol>`, TTL default 20 seconds in `internal/platform/marketdata/config.go:77`), then `quote_cache`, then the provider (`internal/platform/marketdata/service.go:161-208`). `singleflight` covers FMP end-of-day, basic financials, and crypto markets. `Quote` and `QuoteBatch` do not use it. `FMP.StockQuotes` is a real comma-joined `/stable/quote` call (`internal/platform/marketdata/fmp.go:535-546`). `QuoteBatch` still calls `Quote` ten at a time (`service.go:281-314`).

Prometheus text output is off unless `PROMETHEUS_ENABLED` is set (`internal/config/config.go:48`). The registry has HTTP counters and a latency histogram, business counters, and `scenario_queue_depth`. The scenario worker never calls `SetScenarioQueueDepth` (`internal/platform/metrics/metrics.go:105-106` is only referenced from `metrics_test.go`). The worker comment at `internal/features/scenarios/worker.go:131-133` says v2 has no metrics registry. The registry exists. The gauge stays at 0. There are no pgx pool stats and no per-job duration histogram. pprof is opt-in on a loopback listener (`docs/pprof.md`). Block and mutex profiles are off. No profile was captured.

### Workload, as far as the repo documents it

- One API process is the supported realtime shape. Multi-replica HTTP works for request data that lives in Postgres and Redis. It does not work for SSE.
- Jobs are interval pollers, not a message broker. There is no queue, no consumer ack, and no dead-letter topic. Delivery semantics are whatever each job writes to Postgres.
- Provider calls are mostly one HTTP request per symbol, with a few batched endpoints that the hot callers do not use.
- Per-user data volume, quote QPS, webhook retry rates, and Postgres `max_connections` are not in the repo.

### What is already sound

- The scenario claim is the right shape for taking work: `FOR UPDATE SKIP LOCKED`, a 60 second lease, a 20 second heartbeat, concurrency clamped to 1..8. A crash leaves the lease, and another worker can reclaim it. Do not wrap that tick in an advisory lock. The renew, complete, and fail statements are not fenced by `lease_owner`. That gap is F21. The worker is a no-op unless `SCENARIO_PLANNING_ENABLED` is set (`internal/features/scenarios/worker.go:70`, default false in `internal/features/scenarios/feature.go:115`).
- The rate-limit script is one Redis round trip and fails closed in production.
- Inbox insert uses the unique `(event_id, channel)` conflict as the concurrency guard (`db/queries/notifications/notifications.sql:68-75`, `internal/platform/inbox/inbox.go:228-234`).
- News ingestion is capped. Each tick refreshes at most `THESIS_WATCH_SYMBOLS_PER_SYNC` symbols, default 30 (`internal/features/news/jobs.go:84-93`). It is not an unbounded provider loop.
- Billing HMAC verification, when `REVENUECAT_HMAC_SECRET` is set, checks a 300 second window in constant time (`internal/features/billing/webhook.go:154-172`).

---

## C. Executive summary

### 1. The ten highest-priority findings

1. **F01** A RevenueCat event is stored as processed before the entitlement write commits. A retry then returns 200 and skips the entitlement.
2. **F02** Idempotency is check-then-act. Two concurrent requests with the same key both run the handler. A Redis read error also runs it.
3. **F03** Data export generation uses `context.Background()`, is not waited on at shutdown, and a crashed `pending` row is never resumed or failed.
4. **F04** Each leader tick holds one pool connection for the whole body, while the body takes more connections from the same pool of 20.
5. **F05** `Jobs.Wait` returns after 15 seconds with no log, then the pool closes under any tick still inside `RunAsLeader`.
6. **F06** A price-target push is marked sent only after APNs succeeds. A crash or a failed mark sends the push again on the next poll.
7. **F09** Enable Banking transaction paging stops only when the continuation key is empty. A repeating key holds the bank-sync lock until process shutdown.
8. **F14** `GET /v1/dashboard/insights` loads every expense row for the user, including `receipt_metadata`, with no limit.
9. **F15** Scenario definition and run lists return every row for the user, including `configuration` and `result` jsonb, with no limit.
10. **F08** A Plaid sync webhook discards `Sync` errors and answers 200. The backstop waits a full interval, default 6 hours, before its first tick.

F07 (earnings recorded before the push) is the mirror of F06 and belongs in the same change. F10–F13 and F16–F20 are real and sit behind these ten.

### 2. The five most promising performance improvements

These are code-shape improvements. None has a measured before/after number.

1. Stop holding a pooled connection across job bodies that take more connections (F04). This is a stall risk, not a micro-optimization.
2. Aggregate or bound the dashboard expense read and the scenario list reads (F14, F15), after a row count and a plan.
3. Price target alerts and goal holdings with the existing `FMP.StockQuotes` batch, after a symbol count (F06's loop, F16, F18).
4. Cap Enable Banking pages and release the advisory-lock connection while HTTP is in flight (F09, F04).
5. Export pgx pool wait time, job tick duration, and a live `scenario_queue_depth` (F20) so the next pass can reject the rest of this list with data.

### 3. The most dangerous concurrency issues

- **F01** is a lost update across two autocommit statements, made permanent by the idempotency of the event row.
- **F02** is a check-then-act race on money-adjacent routes (banks, brokers, receipts, expense import, pilots).
- **F04** is a pool deadlock shape: lock holders wait for a second connection while every connection is a lock holder. Overlap is capped by flags. Scenario planning, and other jobs that return before `RunAsLeader`, do not hold a connection while their flag is off. Twenty simultaneous lock-holders is an upper bound, not the steady state.
- **F05** closes the pool under goroutines that still think they hold a session lock.
- **F21** lets a scenario worker whose lease expired still `UPDATE` the run by id. Last writer wins, and both workers schedule achievements. The job does not start unless `SCENARIO_PLANNING_ENABLED` is on.
- **F03, F10, F11** start goroutines on `context.Background()` that survive HTTP shutdown and can outlive `pool.Close`.

No copied mutex, double channel close, or unlocked map write was found on the paths that were read. Achievement hook maps sit under `hooks.mu`. That is a source reading, not a race-detector result.

### 4. The most important database bottlenecks

The proven request-path issue is payload, not a missing index.

- `ListExpensesRange` (`db/queries/expenses/expenses.sql:106-110`) is `SELECT * FROM expenses WHERE user_id = $1` when both timestamps are nil. `GET /v1/dashboard/insights` passes nil, nil (`internal/features/dashboard/feature.go:87-88`). The row includes `receipt_metadata jsonb` (`00001_baseline.sql:787-804`). Indexes `(user_id, occurred_on)` and `(user_id, occurred_on, category_id)` already match a user predicate (`00001_baseline.sql:4912-4914`).
- `ListScenarioRuns` and `ListScenarioDefinitions` (`db/queries/scenarios/runs.sql:4-8` and `:46-49`) are `SELECT * ... WHERE user_id = $1 ORDER BY created_at DESC` with no `LIMIT`. `scenario_runs.result` and `scenario_definitions.configuration` are jsonb (`00001_baseline.sql:2726`, `:2743`). Index `(user_id, created_at)` matches the list (`00001_baseline.sql:5038`). The handler is `internal/features/scenarios/runs.go:473`.

`StockOwnersForSymbols` and `WatchlistOwnersForSymbols` (`db/queries/news/news.sql:39-46`) filter `symbol = ANY($1)` while every stocks and watchlist index leads with `user_id` (`00001_baseline.sql:5060-5070`, `:5124-5126`). Whether that is a sequential scan that matters is unknown. No `EXPLAIN` was run. No index is recommended until a plan and a row count exist.

`transactions` has no `user_id` column. Do not add an index on one.

### 5. Risks of the proposed changes

- A billing transaction that inserts the event and applies the entitlement together changes when RevenueCat sees 200. A bug in the new transaction can turn today's silent skip into a retry storm, or the reverse.
- Idempotency that reserves a key before the handler changes the Vapor cache format unless the finished-body encoding stays `status\nheaders\n\nbody` (`internal/middleware/ratelimit.go:81-84`, `:162-167`).
- A separate advisory-lock connection raises the number of Postgres sessions by one per overlapping tick. `max_connections` on the server is unknown.
- Returning a non-200 from the Plaid webhook makes Plaid retry. A permanent sync failure then retries until Plaid gives up.
- Bounding dashboard and scenario responses changes the JSON clients receive if the cap is below today's full list. That is a wire change.
- Lengthening `Jobs.Wait` keeps the process in shutdown longer and can trip the orchestrator kill timeout. The timeout value is not in this repo.

### 6. Improvements that should stay undone

See [Leave unchanged](#leave-unchanged). Short version: do not remove job-lock fail-open, do not add Redis pub/sub for realtime, do not set a global `WriteTimeout`, do not add `sync.Pool` or rewrite structs as pointers, do not add indexes or retries from this document alone, and do not raise `DATABASE_MAX_CONNECTIONS` as the fix for F04.

### 7. Missing evidence

- Production and staging replica count, Postgres `max_connections`, `statement_timeout`, and current `pool_max_conns`.
- pgx pool acquire wait, acquired connections, and canceled acquires. The process does not export them.
- pprof CPU, heap, goroutine, and mutex profiles from a staging process with jobs enabled.
- `EXPLAIN (ANALYZE, BUFFERS)` on a staging copy, never on production, for the dashboard expense read, both scenario lists, `UnresolvedTargets`, and the news owner lookups. Include row counts.
- A query for `billing_events` rows whose later entitlement write did not land. The table has `processed_at` set at insert time, so "row exists" does not mean "entitlement applied".
- Whether clients send the same `Idempotency-Key` on parallel connections.
- Quote-cache hit rate and FMP 429 rate.
- Achievement evaluation duration and catalog size while the `FOR UPDATE` transaction is open.
- `go test -race ./...` against the disposable template database in the Makefile. Not run here.

---

## A. Findings

Severity is the damage when the path runs, not how often it has been seen. Confidence is how sure the reading of the code is. Impact that depends on volume is called a hypothesis.

| ID | Severity | Category | Location | Confidence |
|---|---|---|---|---|
| F01 | Critical | Reliability | `internal/features/billing/webhook.go:236-324` | High |
| F02 | High | Reliability | `internal/middleware/ratelimit.go:102-134` | High |
| F03 | High | Reliability | `internal/features/dataexport/feature.go:266-276` | High |
| F04 | High | Concurrency | `internal/platform/joblock/joblock.go:22-45` | High on the mechanism, Medium on frequency |
| F05 | High | Concurrency | `internal/features/features.go:409-416` | High |
| F06 | High | Reliability | `internal/features/watchlist/jobs.go:160-207` | High |
| F07 | Medium | Reliability | `internal/features/watchlist/jobs.go:393-421` | High |
| F08 | Medium | Reliability | `internal/features/banking/webhook.go:193-209` | High |
| F09 | High | Reliability | `internal/features/banking/enablebanking.go:195-224` | High on the missing bound, Medium on frequency |
| F10 | Medium | Concurrency | `internal/features/achievements/feature.go:40-74` | High |
| F11 | Medium | Concurrency | `internal/features/groups/notifier.go:72-80` | High |
| F12 | Medium | Concurrency | `internal/features/assistant/telegram_bridge.go:147-171` | High |
| F13 | Medium | Reliability | `cmd/api/main.go:189-193` | High |
| F14 | Medium | Database | `db/queries/expenses/expenses.sql:106-110` | High on shape, Low on cost |
| F15 | Medium | Database | `db/queries/scenarios/runs.sql:4-8`, `:46-49` | High on shape, Low on cost |
| F16 | Low | Performance | `internal/platform/marketdata/service.go:281-314` | High on shape, Low on cost |
| F17 | Medium | Database | `internal/features/achievements/service.go:71-163` | High on shape, Low on duration |
| F18 | Low | Performance | `internal/features/goalplanning/service.go:245-261` | High on shape, Low on cost |
| F19 | Low | Database | `db/queries/news/news.sql:39-46` | Medium. Plan not run |
| F20 | Medium | Reliability | `internal/platform/metrics/metrics.go:105-106`, `scenarios/worker.go:131-133` | High |
| F21 | High when the flag is on | Concurrency | `db/queries/scenarios/runs.sql:99-114` | High |

### F01 — Billing event is committed before the entitlement

- **Category:** Reliability
- **Evidence:** `processEvent` calls `BillingEventExists` and returns nil when the row exists (`webhook.go:238-240`). `InsertBillingEvent` then runs on `svc.Queries()`, which is the pool handle (`entitlements.go:121-122`), so the insert autocommits. The subscription and entitlement writes happen after that (`webhook.go:282-324`). The insert has no `ON CONFLICT` (`db/queries/billing/billing.sql:7-9`). Uniqueness is `uq:billing_events.provider_event_id` (`db/migrations/00001_baseline.sql:4412`).
- **Problem:** If the process dies, or `upsertSubscription` / `processTaxPackEvent` / `upsertEntitlement` returns, the event row remains. RevenueCat's retry hits `exists` and the handler returns 200 (`webhook.go:204-207`) with no second attempt at the entitlement. Two in-flight deliveries can interleave the same way: the loser of the insert gets a unique violation and returns 500, which is retryable, but a later attempt that observes the row returns 200 while the winner is still in `upsert` or has already failed.
- **Impact:** A paid user's level, tax pack, or cancellation can stay at the previous value for good. Correctness. Frequency is unmeasured.
- **Recommendation:** One transaction that inserts the event and applies the entitlement, and acknowledge only after commit. `Service.WithDB` already exists for this (`entitlements.go:99-106`). On unique violation, proceed only when a committed "applied" state exists. A `received` versus `applied` status is the alternative if the entitlement write must stay outside the insert for a reason this code does not show.
- **Risk:** A long transaction around provider-free local writes is small. Including any HTTP call inside it would hold row locks across the network. The current `upsert*` path must be re-read for statements that cannot share a transaction. Rollback is reverting to today's autocommit, which restores the skip-on-retry bug.
- **Validation:** A test that inserts the event, fails the entitlement write, redelivers the same `provider_event_id`, and asserts the entitlement changed. A second test with two overlapping calls asserts one applied row and one entitlement write.

Related, not the same bug: when `REVENUECAT_HMAC_SECRET` is empty, the Authorization shared secret is accepted with no time window (`webhook.go:175-186`). A stolen header works until the secret rotates. The event id still dedupes a single payload. Fix the transaction first. Rotating or requiring the HMAC secret is a separate config decision.

### F02 — Idempotency reads, then runs, then writes

- **Category:** Reliability
- **Evidence:** `Idempotency` (`ratelimit.go:85-137`) GETs the key and, unless `err == nil` and the body parses, calls `next`. A Redis error and a missing key take the same branch (`:110-120`). `SET` errors are ignored (`:133`). The key is `prefix:authHash:idemKey` with no method or path (`:109`). Mounted on expense import (`expenseimport/feature.go:72`), brokers (`brokers/feature.go:166`), receipts (`receipts/feature.go:65`), pilots (`pilots/feature.go:68`), and banking (`banking/feature.go:160`). Production with Redis nil returns 503 (`:102-105`). Production with Redis up and a `GET` error does not.
- **Problem:** Two requests that both miss the key both execute the handler. A blip on `GET` executes it too. A failed `SET` means the next retry executes it again. Rate limiting in the same file fails closed on Redis errors (`:55-59`). Idempotency does the opposite. 4xx and 5xx are not stored, so a committed side effect that returned 500 runs again. That last part matches "replay the first 2xx/3xx" and is a product choice. The race and the fail-open `GET` are not.
- **Impact:** Duplicate bank, broker, receipt, pilot, or import writes when a client retries in parallel or Redis reads fail. Whether clients do that is unknown.
- **Recommendation:** `SET` the key to an in-progress marker with `NX` and the 24 hour TTL before `next`. The holder runs the handler. A peer waits, with a bound, or receives 409. Store the finished body only after success, in the existing Vapor encoding. On Redis errors in production, return 503 for a request that sent `Idempotency-Key`, matching the rate limiter. Keep the key format so a retry can still hit a Vapor entry during any leftover overlap.
- **Risk:** A process crash after `NX` and before the finished body leaves the key in progress until TTL. Peers must be able to take over after a short lease, or the user is stuck for 24 hours. That lease is the whole design. A 409 on the peer is simpler and can surprise a client that expects the first response replayed.
- **Validation:** Two parallel requests, same Authorization and key, one handler execution. Redis `GET` error in production returns 503 and does not call the handler. A successful response is replayed byte-for-byte, including the Vapor framing.

### F03 — Data export work is detached from shutdown and from recovery

- **Category:** Reliability
- **Evidence:** `create` inserts `status=pending`, then `go func` calls `generate(context.Background(), ...)` (`feature.go:258-276`). `handler.wg` is never waited in `serve`. Shutdown waits for Telegram turns and jobs (`cmd/api/main.go:216-218`), then closes the pool. `DeleteExpired` deletes expired ready rows only (`dataexport/job.go:62-66`). The create route allows 60 requests per minute per user (`feature.go:88`).
- **Problem:** Client cancel and process cancel do not stop generation. A deploy closes the pool under the goroutine. The row stays `pending`. Nothing sweeps it. Each request holds the full export in memory (`feature.go:287-306`) with no process-wide cap.
- **Impact:** Exports that never become ready or failed. Memory growth with a 60/minute burst. A pool error in a goroutine after `Close`.
- **Recommendation:** Pass a process-scoped context, and `Wait` the group in shutdown before `pool.Close`, with the same logged deadline as F05. Add a recovery pass: `pending` older than the generation budget becomes `failed`, or is claimed and rerun once. Keep the per-user rate limit. Add a process-wide cap only if a profile shows overlapping exports. A cap of 1 per user is enough to start and needs no new infrastructure.
- **Risk:** Waiting on a large export lengthens shutdown. The recovery pass must not run two generators on one id. Claim with `UPDATE ... WHERE status = 'pending'`.
- **Validation:** Cancel the process context during `generate` and assert the row is `failed` or a new process finishes it. Two overlapping creates for one user do not both hold a full dataset if the cap is adopted.

### F04 — The advisory lock borrows a pool connection for the whole tick

- **Category:** Concurrency
- **Evidence:** `RunAsLeader` acquires from the request pool, holds that connection across `body(ctx)`, and unlocks on the same session (`joblock.go:22-45`). The body uses the pool again. Banking is the clear case: the lock is held around `SyncOnce`, and `SyncOnce` calls `ListActiveConnections` on the pool (`banking/job.go:53`, `:59-91`). The pool default is 20 (`config.go:62`). pgx `Acquire` waits until a connection is free or the context ends. If acquire returns an error, the function logs and runs the body anyway (`joblock.go:24-27`, `:32-35`). That fail-open is documented Vapor parity (`joblock.go:9-10`).
- **Problem:** A tick that holds one connection and then waits for a second, while every connection is held by such a tick, waits until the process context cancels. Overlapping ticks are possible: 29 call sites, and several intervals are minutes, not hours. They do not all fire together. How often the pool is actually exhausted is unmeasured. Fail-open on a canceled context can also start a tick with no lock during shutdown, on a context that is already done. Bodies that ignore `ctx` still run.
- **Impact:** HTTP and jobs block in `Acquire`. Latency and availability. Duplicate ticks when the lock cannot be taken because the database is unreachable. That duplicate behavior is intentional and should stay.
- **Recommendation:** Take the advisory lock on a `pgx.Conn` that is not a member of the pool, one per in-flight leader, closed after unlock. Leave fail-open in place for a dial failure, and do not call `body` when `ctx` is already canceled. Do not raise `DATABASE_MAX_CONNECTIONS` as the remedy. Extra server sessions equal the number of overlapping ticks, which is far below 29 in steady state and is still an unknown against `max_connections`. Export pool acquired and empty-acquire wait in the same change (F20) so the stall is visible.
- **Risk:** More Postgres connections. A leaked lock connection pins a session until process exit. The unlock must stay on that same connection, using `context.WithoutCancel` the way line 44 does today.
- **Validation:** A test with a pool of 2, two leaders whose bodies each need a pooled query, both complete. A test that a canceled context does not enter `body`. Watch `pgxpool` empty-acquire count in staging before and after.

### F05 — Shutdown abandons jobs after 15 seconds, silently

- **Category:** Concurrency
- **Evidence:** `Wait` starts `wg.Wait` on another goroutine and returns on `time.After(15 * time.Second)` with an empty case (`features.go:409-416`). `serve` then returns and `cleanup` closes the pool (`main.go:180`, `:218`). Scenario `execute` returns early when `ctx` is canceled and leaves the lease (`worker.go:154-158`), which is safe. Any other tick still inside `RunAsLeader` is still using the pool. The inner goroutine at `features.go:411` stays blocked until the jobs actually return, which may be after `Close`.
- **Problem:** In-flight bank sync, news refresh, report generation, and similar lose their database handle at a random statement. The operator gets no log that the deadline fired. The comment on `Wait` says it blocks until every job returns. The timer contradicts the comment.
- **Impact:** Partial writes, and a lock session that dies with the connection so the next process can take the lock. Scenario runs survive via the lease. Other jobs have no lease.
- **Recommendation:** Log the job names still running when the timer fires. Check `ctx.Err()` at the top of `RunAsLeader` and skip `body`. Keep 15 seconds until an orchestrator grace period is known. Lengthening the wait without that number risks the process being killed anyway. Prefer making long ticks observe `ctx` (banking's per-connection loop at `job.go:67` does not check it).
- **Risk:** A log-only change has almost none. Teaching every job to exit on `ctx` is the real work and can drop a tick that today runs to completion during the 20 second HTTP drain plus 15 seconds.
- **Validation:** A fake job that blocks in `body` until canceled. Shutdown logs the timeout, `body` returns, and `pool.Close` happens after `RunAsLeader` returns.

### F06 — Target alerts send, then mark

- **Category:** Reliability
- **Evidence:** The file comment says the poller is idempotent via `alert_triggered_at` (`jobs.go:33-35`). `evaluate` publishes to the inbox, sends APNs, and only then calls `markTriggered` (`jobs.go:172-197`). `markTriggered` is its own transaction (`jobs.go:211-232`). `UnresolvedTargets` is `SELECT * FROM targets WHERE alert_triggered_at IS NULL` with no limit (`db/queries/watchlist/targets.sql:41`). The loop calls `Quote` once per target, serially (`jobs.go:144-146`, `:160-161`), inside the advisory lock (`jobs.go:67`). Indexes on `targets` are `(user_id, symbol)` and `(user_id, updated_at)` only (`00001_baseline.sql:5082-5084`).
- **Problem:** Inbox publish dedupes, so the in-app row is safe. The push is not. Success followed by a failed mark, or a crash before the mark, leaves `alert_triggered_at` null. The next poll, default 300 seconds (`jobs.go:78-80`), sends again. Zero devices returns before the mark (`jobs.go:189-190`), so the target stays unresolved. That part is reasonable. A partial APNs success (`Delivered > 0`) marks the target and does not retry the failed devices.
- **Impact:** Duplicate pushes. Provider quota grows with the number of unresolved targets because quotes are serial and unbatched. Both effects are unmeasured. A partial index on `alert_triggered_at IS NULL` is a hypothesis, not a recommendation, until `EXPLAIN` and a count exist.
- **Recommendation:** In one transaction, lock the target (`TargetForUpdate` already exists at `targets.sql:44`), set `alert_triggered_at` when it is null, commit, then send. A crash after the mark and before the send skips the push. That matches the earnings poller and is the safer direction for a price alert that will not be true tomorrow in the same way. If product wants at-least-once push, record an attempt id and send through a path that claims the attempt before APNs, with a single retry cap. Separately, unique the symbols and call `FMP.StockQuotes` or a batched `Quote` once per tick. Do that only after a count of unresolved targets. Keep the advisory lock off the HTTP calls once F04 lands.
- **Risk:** Mark-then-send loses a push if the process dies in between. Send-then-mark duplicates it. Pick one and test it. Batching quotes changes failure behavior: one provider error currently skips one symbol (`jobs.go:162-164`); a batch error can skip the tick.
- **Validation:** Fail `MarkTargetTriggered` after a successful send in today's code and show a second send. After the fix, show one send. A fixture with N targets sharing a symbol shows one provider call per distinct symbol.

### F07 — Earnings reminders are recorded before APNs

- **Category:** Reliability
- **Evidence:** `evaluateUser` returns when `EarningsReminderDelivered > 0` (`jobs.go:377-386`). `inbox.Publish` and `InsertEarningsReminderDelivery` both run before `Send` (`jobs.go:393-421`). A send with `Delivered == 0` logs and returns. The delivery row stays.
- **Problem:** The next poll treats a failed push, a device-lookup error, and a crash between insert and send as "already sent". This is the opposite hole from F06. The comment at `jobs.go:33-35` describes the row as the idempotency key. It is, and it treats failure as success.
- **Impact:** A missed earnings reminder for that symbol, date, and lead day. In-app inbox may still have the event, because `Publish` ran first.
- **Recommendation:** Insert the delivery row in the same decision as "we are done trying", after a successful send, or with a status of `failed` that the next poll may claim once. Cap at one extra attempt. Do not loop forever. Align the choice with F06 so the two pollers share one rule.
- **Risk:** A retry can double-send if the first APNs call succeeded and the status write failed. The claim has to be the same row the insert uses today.
- **Validation:** `Delivered == 0` leaves a state the next poll can send once. `Delivered > 0` is never sent twice.

### F08 — Plaid webhook acknowledges a failed sync

- **Category:** Reliability
- **Evidence:** For `SYNC_UPDATES_AVAILABLE` and the other update codes, the handler calls `p.Sync` and ignores the error, then returns 200 (`webhook.go:193-209`). Unknown items also return 200 (`:186-188`), which is right. The reconcile job sleeps one full interval before every tick, including the first, default 6 hours (`banking/job.go:19-54`), then holds the leader lock across every active connection (`job.go:67-91`) with no `ctx` check between connections.
- **Problem:** Plaid will not retry a sync that this process accepted and then failed. Recovery waits for the poller. A stuck Enable Banking loop (F09) inside that poller delays every later connection.
- **Impact:** Stale transactions until the next successful poll. Duplicate bank rows were not established. `upsert` uniqueness was not fully traced, so this finding does not claim duplicates.
- **Recommendation:** Log the sync error with the connection id. Return 500 for an error that is safe to retry, and 200 for a permanent one (`reauth`, unknown item). Shorten nothing until the poller's first-tick delay is confirmed as product intent. Add a `ctx` check in the connection loop as part of F05.
- **Risk:** 500 on a deterministic sync bug makes Plaid retry until its own limit. Classify errors before flipping the status code. A first-tick delay change makes the job sync at boot, which is a behavior change from the comment at `job.go:18-20`.
- **Validation:** A sync error returns non-200 and is logged. A second delivery after a repaired sync writes the cursor. A canceled context stops the connection loop before the next provider call.

### F09 — Enable Banking pages have no cap

- **Category:** Reliability
- **Evidence:** `transactions` appends every page until `continuation_key` is null or empty (`enablebanking.go:195-224`). There is no page cap and no repeated-key guard. `send` uses the caller's context. On the sync job that context is the process context, and the caller holds the advisory lock (F04).
- **Problem:** A provider that returns the same key, or a very long history, grows `all` without a bound and pins the lock connection until shutdown.
- **Impact:** Memory, a stuck leader, and no progress for other connections in that tick. How often the key repeats is unknown.
- **Recommendation:** Stop after a fixed page count or a repeated key, return the error, and let F08 decide the HTTP status. Persist the cursor the way the Plaid path does only if the existing upsert is safe to resume. That upsert was not fully traced, so the first change is the cap and the error, not a new cursor scheme.
- **Risk:** A low cap truncates history that today would finish. Pick the cap from the largest successful sync already stored, which means a count query on a staging copy. A cap that returns an error is safer than a cap that returns a silent partial page.
- **Validation:** A fake client that repeats one key returns an error within N pages and does not grow without bound. A normal two-page response still returns both pages.

### F10 — Achievement evaluation is not tied to shutdown

- **Category:** Concurrency
- **Evidence:** `HooksContext` is documented as nil because `Register` has no lifecycle context (`achievements/feature.go:40-42`). Production passes a nil context and `SetEvaluator` stores `context.Background()` (`feature.go:70-74`). `launch` waits `Delay` and calls `eval` (`achievementhooks/hooks.go:123-145`). `WaitUntilIdle` is for tests (`hooks.go:168-169`). `serve` does not call it. Maps are updated under `h.mu`.
- **Problem:** An evaluation scheduled during the 20 second HTTP shutdown still runs, and `ctx.Done()` never fires, so the delay cannot skip it. `Evaluate` then uses the pool after `Close` if it loses the race with F05.
- **Impact:** Shutdown errors in the log. A user-level debounce already limits this to one goroutine per user (`hooks.go:109-116`). There is no global cap.
- **Recommendation:** Pass the process context into `SetEvaluator` from `serve`, after `Register`. Call `WaitUntilIdle` with the F05 deadline, or cancel and abandon. Do not add a worker pool.
- **Risk:** Canceling mid-`Evaluate` rolls the transaction back (`service.go:75`). The next request reschedules. That is safe.
- **Validation:** Cancel the process context during the delay and assert `eval` was not called. Cancel during `Evaluate` and assert the transaction rolled back.

### F11 — Group push fan-out uses `context.Background()`

- **Category:** Concurrency
- **Evidence:** `detach` starts `fn(context.Background())` and `Wait` is nil unless tests set it (`groups/notifier.go:60-80`). `messageCreated` does the database and push work inside that function (`notifier.go:86`).
- **Problem:** Fan-out outlives the request and the process context. `pool.Close` can land in the middle of it. There is no in-flight limit across users.
- **Impact:** Wasted APNs calls during shutdown, and pool errors. Volume is one goroutine per qualifying message, not per member. Member sends inside it are sequential. Unmeasured.
- **Recommendation:** Use the process context. On shutdown, cancel and move on. A per-process limit is justified only after a count of concurrent `detach` calls. The APNs client timeout still bounds a single send.
- **Risk:** A canceled shutdown drops pushes that today would finish in the 15 second job window. Group chat can tolerate a missed push better than a stuck process. Confirm with product if that is wrong.
- **Validation:** Shutdown cancels a blocked fan-out. A message still pushes when the process stays up.

### F12 — Telegram turns have a timeout and no cap

- **Category:** Concurrency
- **Evidence:** The webhook answers 200 and then `dispatch` (`telegram_bridge.go:132-134`). Each update gets a 360 second context and a goroutine (`:32`, `:154-171`). `inflight` is a drain set, not a limit. `DrainTelegramTurns` cancels and waits (`:91-105`) and `serve` calls it (`main.go:217`). The route registration has no limiter (`assistant/feature.go:124`).
- **Problem:** Shutdown is covered. While the process is up, deliveries can stack 360 second turns with no process cap. Each turn uses the database and the model.
- **Impact:** Goroutines and provider spend under a webhook flood. Telegram's own retry behavior was not measured.
- **Recommendation:** Cap in-flight turns. When the cap is full, still return 200 so Telegram does not retry storm, and skip or queue with a bound of one pending update per chat. A 200-and-drop needs a log. Do not add an unbounded queue.
- **Risk:** Dropped updates under load. The cap hides a flood and also hides a stuck model call. The 360 second timeout has to stay.
- **Validation:** Fill the cap, deliver one more update, assert the extra turn did not start, and assert drain still returns.

### F13 — HTTP server sets only `ReadHeaderTimeout`

- **Category:** Reliability
- **Evidence:** `cmd/api/main.go:189-193`. Zero `ReadTimeout` and zero `IdleTimeout`. Body size is limited to 10 MB in middleware (`server.go:103`), with per-route lifts.
- **Problem:** A client can dribble a body after the headers, or hold an idle keep-alive, until the process restarts. `ReadHeaderTimeout` covers the header only. SSE is why `WriteTimeout` is unset. `http.Server.Shutdown` does not wait for hijacked responses (`main.go:194-195`).
- **Impact:** Connection and goroutine buildup. Unmeasured.
- **Recommendation:** Set `ReadTimeout` to a value above the slowest non-SSE handler, and `IdleTimeout` to a keep-alive bound. Leave `WriteTimeout` unset. If a single handler needs longer than `ReadTimeout`, give that handler a request context deadline instead of raising the server-wide read limit to the worst case. Streaming uploads need a look before the timeout is chosen. None was singled out in this pass beyond the body limit.
- **Risk:** A timeout below a legitimate slow client aborts the request. Pick the number from the HTTP latency histogram once F20's per-route histogram exists. A first conservative `IdleTimeout` of 120 seconds does not cut uploads.
- **Validation:** A client that stalls after headers is disconnected. An SSE stream still lives until shutdown. An idle keep-alive is closed.

### F14 — Dashboard insights loads every expense

- **Category:** Database
- **Evidence:** `ListExpensesRange` selects every column, and both date predicates are compiled away when the arguments are nil (`expenses.sql:106-110`). Insights wires `GetMonthlyReports(ctx, d.DB, userID, nil, nil)` (`dashboard/feature.go:87-88`), which calls that query (`expenses/summaries.go:93-95`). The same nil, nil pattern is in `dashboard/export.go:26` and three assistant call sites. `expenses.receipt_metadata` is jsonb (`00001_baseline.sql:804`). Indexes on `(user_id, occurred_on)` exist. This is not a missing-index finding.
- **Problem:** One dashboard request materializes every expense the user has ever stored, then groups them in Go (`summaries.go:111-117`).
- **Impact:** Memory, response time, and bytes from Postgres, proportional to the user's history. No row count was taken. Severity stays Medium until that count exists. A user with a short history will not notice.
- **Recommendation:** Measure first: count and `EXPLAIN (ANALYZE, BUFFERS)` on a staging copy for a heavy user. If the plan shows a large heap fetch, aggregate in SQL by month, or select the columns `GetMonthlyReports` actually reads, and pass a window if the product only needs a streak. `ListExpensesPage` already has `LIMIT 1000` (`expenses.sql:85-104`). The insights path does not use it.
- **Risk:** A window shorter than the user's history changes `budgetStreak` and the savings rate (`dashboard/service.go:151-168`). Read that loop before cutting the range. Dropping `receipt_metadata` from this query is safe if no field in the summary reads it. Confirm before narrowing the select list.
- **Validation:** A user with more rows than the new bound still gets the same streak and savings rate the product definition requires. `EXPLAIN` shows an index range on `(user_id, occurred_on)` rather than a fetch of every toast version. Compare buffer reads before and after on staging.

### F15 — Scenario lists are unbounded

- **Category:** Database
- **Evidence:** `ListScenarioDefinitions` and `ListScenarioRuns` (`runs.sql:4-8`, `:46-49`) have no `LIMIT`. Both tables carry jsonb (`00001_baseline.sql:2726`, `:2743`). `runs.go:473` loads runs for the list handler. Index `(user_id, created_at)` matches (`00001_baseline.sql:5038`). The claim query is a different statement and already has `LIMIT 1`.
- **Problem:** The list endpoint returns every historical run, including `result`, for one user.
- **Impact:** Same shape as F14. Cost unmeasured. Completed runs are the heavy rows.
- **Recommendation:** After a count, add a limit the clients already tolerate, or keyset pagination on `(created_at, id)`. Do not use a large `OFFSET`. Keep `SELECT *` for the single-run get. The list can omit `result` until the detail route.
- **Risk:** A wire change if clients depend on the full array. Check the iOS client before shipping a cap. Pagination tokens are a contract change.
- **Validation:** A user with more runs than the page size gets a stable page and can open one run with its `result`. Bytes per list response drop on the staging fixture.

### F16 — `QuoteBatch` does not use the batch endpoint

- **Category:** Performance
- **Evidence:** `QuoteBatch` fans out `Quote` in chunks of 10 (`service.go:281-314`). Each `Quote` does Redis, then Postgres, then one provider call (`service.go:161-208`). `FMP.StockQuotes` already accepts a comma-joined list (`fmp.go:535-546`) and is used for the single-symbol FMP fallback (`service.go:251`). `singleflight` is not taken on `Quote`. End-of-day, basic financials, and crypto markets do take it. Quote TTL defaults to 20 seconds (`config.go:77`). The cache treats Redis errors as misses.
- **Problem:** A burst of the same symbol after a cache expiry starts one provider call per waiter. A 100-symbol batch is up to 100 provider calls. This is a stampede hypothesis. The cache may already absorb it.
- **Impact:** Provider latency and 429s, if the miss path is hot. No hit-rate metric exists.
- **Recommendation:** Do not add `singleflight` or switch `QuoteBatch` to `StockQuotes` until a counter of provider calls per `Quote` exists. The target-alert loop (F06) and goal planning (F18) are the callers worth fixing if that counter is high, because they run on a timer and ignore the HTTP batch route's cap of 100. `StockQuotes` returns a different type (`CryptoQuoteResponse`). Mapping has to preserve the current fallback and stale-cache behavior in `fetchQuote`.
- **Risk:** A batched provider call fails the whole chunk. Today's code fails one symbol. `singleflight` hides a context bug: the shared call must use a detached timeout, not the first caller's cancel. Getting that wrong cancels everyone.
- **Validation:** A benchmark is the wrong tool. Count provider HTTP calls for 20 concurrent `Quote` of one symbol with a cold cache, on a fake server. Then the same with `singleflight`. Ship only if the fake server shows a stampede the Redis TTL does not already prevent.

### F17 — Achievement evaluation holds a row lock across a wide read

- **Category:** Database
- **Evidence:** `Evaluate` begins a transaction, `LockEvaluation` (`FOR UPDATE` on the user's evaluation row), then `measure` for every in-scope catalog key, then commit (`service.go:71-163`). `knownSectorCount` loads quotes and about 400 days of price history for the user's symbols on that same transaction (`metrics.go:355-393`). There is no HTTP inside the transaction. The hook path debounces per user.
- **Problem:** The lock duration is the full measurement. A second evaluation of the same user waits. The transaction is read-heavy and user-scoped, so it does not lock other users' evaluation rows. It does hold a snapshot open while reading price history.
- **Impact:** Lock wait for that user, and a long transaction if the catalog or the price history is large. Both sizes are unknown. This is not an N+1 against other users.
- **Recommendation:** Measure `Evaluate` duration and the number of statements inside `measure` on staging. If it is long, compute `measure` before the transaction and hold `FOR UPDATE` only across the insert of unlocks. Re-check the `needs_backfill` flag inside the short transaction so a concurrent sweep cannot clear it. Do not do this split on a guess. The comment at `service.go:60-62` says the flag is read and cleared under the lock on purpose.
- **Risk:** A split transaction can unlock twice or skip a backfill if the re-check is wrong. Keep one transaction until a duration says otherwise.
- **Validation:** A test with two concurrent `Evaluate` calls for one user, one waits, both leave a consistent unlock set. A timing log of the transaction, before any split.

### F18 — Goal value quotes one holding at a time

- **Category:** Performance
- **Evidence:** `currentValue` lists holdings, then `s.quote` per holding (`goalplanning/service.go:245-261`), and `quote` calls `quotes.Quote` (`:287-291`). Portfolio valuation already has a batch path. This one does not. The daily job holds the advisory lock around the tick (`goalplanning/job.go:80`).
- **Problem:** One provider or cache round trip per holding, per allocation, inside the leader lock.
- **Impact:** Tick duration. Holding counts are unknown. Low severity until a count exists. Same fix shape as the target poller, and it should share one helper so the two do not diverge.
- **Recommendation:** After F16's provider-call counter, batch distinct symbols per `currentValue` call through the existing quote service. Preserve the per-symbol fallback to `BuyPrice` (`service.go:252-259`).
- **Risk:** A batch error must still fall back per symbol. Collapsing that to "whole portfolio unavailable" changes the warnings the client sees.
- **Validation:** N holdings, one symbol, one provider call, same total and the same warning strings.

### F19 — Symbol owner lookups do not match the leading index columns

- **Category:** Database
- **Evidence:** `StockOwnersForSymbols` is `SELECT symbol, user_id FROM stocks WHERE symbol = ANY($1)` (`news.sql:39-40`). `WatchlistOwnersForSymbols` is the same shape on `watchlist_items` (`news.sql:42-46`). Stocks indexes all lead with `user_id` or `source_account_id` (`00001_baseline.sql:5060-5070`). Watchlist indexes lead with `user_id` (`:5124-5126`). No `EXPLAIN` was run. Cardinality is unknown.
- **Problem:** The planner cannot use those indexes for a predicate that does not constrain `user_id`. It may sequential-scan. A sequential scan of a small table is cheap. This audit does not know which case this is.
- **Impact:** Webhook or notification latency, if these statements run on a large table. Hypothesis.
- **Recommendation:** On a staging copy, `EXPLAIN (ANALYZE, BUFFERS)` with a representative symbol list. Consider an index on `stocks (symbol, user_id)` and `watchlist_items (symbol, user_id) WHERE status <> 'archived'` only if the plan is a sequential scan whose buffer reads matter and the write rate on those tables can afford another index. `watchlist_items` is written by users, not by a market feed, so a partial index is plausible. Do not add it in the same migration as anything else, and do not add it from this document alone.
- **Risk:** Write amplification on `stocks` and `watchlist_items`. A wrong column order (`user_id, symbol`) does not help this query. The existing `(user_id, symbol)` index is for the other direction.
- **Validation:** The plan uses the new index, buffer reads drop on the staging fixture, and an insert benchmark on `stocks` does not regress by a margin you care about. If the table is small, delete the migration.

### F20 — The signals that would confirm the rest of this audit are missing or unwired

- **Category:** Reliability
- **Evidence:** `SetScenarioQueueDepth` is defined (`metrics.go:105-106`) and tested, and the worker never calls it. The worker comment says there is no registry (`worker.go:131-133`). Prometheus is off by default (`config.go:48`). No pool stats. No job duration. pprof exists and was not sampled. Block and mutex profiles are off.
- **Problem:** F04, F14, F15, F16, F17, and F19 cannot be ranked by cost from production data, because the process does not publish the right series, or publishing is disabled.
- **Impact:** The next optimization will be another code reading. That is how an index or a pool-size change gets shipped on a hypothesis.
- **Recommendation:** Wire the existing gauge to the scenario claim loop. Export `pgxpool.Stat()` (acquired, idle, empty acquire count, canceled acquire). Time each `RunAsLeader` body and export a histogram by job name. Turn Prometheus on in staging. Take one pprof CPU and heap sample with jobs enabled before any F16 or F18 code change. Leave block and mutex profiles off until a stall points at a lock.
- **Risk:** A histogram per job name is one series per job, which is fine at 38 jobs. Label cardinality from raw SQL text would not be. Do not label by query text. Scraping pool stats is cheap.
- **Validation:** The gauge moves when a run is queued. A forced pool acquire shows up in the scrape. A job tick appears in the histogram.

### F21 — Scenario renew, complete, and fail are not fenced by lease owner

- **Category:** Concurrency
- **Evidence:** `ClaimScenarioRun` sets `lease_owner` and a 60 second expiry (`runs.sql:90-93`). `RenewScenarioRunLease` is `UPDATE scenario_runs SET lease_expires_at = $2 WHERE id = $1` (`runs.sql:99-100`). `CompleteScenarioRun` and `FailScenarioRun` also update by id only (`runs.sql:105-114`). The heartbeat reads the owner and then renews (`worker.go:274-282`). If that read errors, the heartbeat returns without canceling the simulation (`worker.go:274-276`). The fail path checks the owner (`worker.go:165-169`) and then calls `FailScenarioRun` with no owner predicate (`worker.go:172`). The success path checks `state != cancelled` and not the owner (`worker.go:244-252`). The worker does not start unless `SCENARIO_PLANNING_ENABLED` is true (`worker.go:70`). That flag defaults to false (`feature.go:115`).
- **Problem:** Worker A can pass the owner check, then lose the lease. Worker B claims the same run. A's later `Renew` extends `lease_expires_at` without taking `lease_owner` back, so B stays owner and the expiry moves. A's later `Complete` or `Fail` still writes the row, clears the lease, and on success schedules achievements (`worker.go:252-256`). B can do the same. Last writer wins. A database error on the heartbeat read disables the cancel that was supposed to stop A.
- **Impact:** Two simulations for one run, two achievement schedules, and a result that belongs to whichever worker finished last. This is dormant while the flag is off. Production's flag value is not in the repo.
- **Recommendation:** Put `AND lease_owner = $worker` on renew, complete, and fail. If the update affects zero rows, cancel the simulation and do not schedule achievements. On a heartbeat read error, cancel the simulation rather than keep running unfenced. Keep the claim SQL. Do not add an advisory lock around the tick.
- **Risk:** A worker whose renewal loses the race stops a simulation that was almost done. The reclaiming worker runs it. That is the point of the lease. A mistaken predicate that never matches would fail every run. The test below is the guard.
- **Validation:** Two workers, one run. Expire A's lease before A's complete. Assert one `completed` row, one owner, one achievement schedule. A heartbeat `GetScenarioRunByID` error cancels the sim. Run this only in the disposable test database.

### Paths reviewed and left as findings only in the roadmap notes

- Inbox `claim` treats `pending`, `delivered`, and `no_devices` as owned (`inbox.go:211-214`). Inserts start at `pending` (`notifications.sql:72`). `failed` can be retried, and nothing sweeps old `pending`. Thesis-watch counts a `PublishAndPush` return as delivered even when a channel logs an error (`news/jobs.go:279-286`, `inbox.go:174-184`). Digest and market brief are documented at-most-once. Leave them. A bounded reclaim of `pending` older than N minutes, one attempt, is P3.
- News ingestion loads every stock symbol and every active watchlist symbol each tick, then keeps 30 (`news/jobs.go:126-144`, `db/queries/news/thesiswatch.sql:83-87`). The provider work is capped. The full-table read is the part to measure, not to "fix" with a cache.
- `joblock` fail-open stays. See below.

---

## B. Original implementation roadmap

Superseded by [Revised roadmap](#revised-roadmap). The text below is the proposal the staff review judged. Do not implement it as written.

Each item answers the ten audit questions in one place: the problem, the evidence, whether it is necessary, the simpler alternative, the failure modes it can add, overload and shutdown, downstream load, complexity, how to measure, and rollback.

### P0 — Correctness and safe shutdown

#### P0.1 Billing webhook transaction (F01)

- **Approach:** Open one transaction with `Service.WithDB`. Insert the event and apply the subscription or tax pack in that transaction. Commit, then return 200. On unique violation, `SELECT` the row inside a follow-up that reapplies only when the entitlement write is not recorded. If a single transaction cannot cover `GrantTaxPack` because that function opens its own connection, change `GrantTaxPack` to accept the querier. Do not call RevenueCat or any other network from inside the transaction.
- **Benefit:** A retry cannot acknowledge an event whose entitlement did not commit. Confidence high on the bug, high that the transaction fixes this shape.
- **Complexity:** Medium. The upsert helpers must be read for pool use that escapes the querier. Risk of a retry storm if the new code returns 500 for a permanent data error. Map permanent errors to 200 with a log, and transient errors to 500, on purpose.
- **Dependencies:** None. Do this before any performance work.
- **Tests:** Redelivery after a forced failure of `upsertEntitlement`. Two overlapping deliveries. Unknown `app_user_id` still logs and does not grant Pro. HMAC rejection still returns 401 and writes nothing.
- **Metrics:** Count of webhook 500s, and a one-off query of events with no matching entitlement change. The second needs a definition of "applied" once the status exists.
- **Rollback:** Revert the commit. The old skip-on-retry behavior returns. Events stored by the new code must still be readable by the old `BillingEventExists` check, so do not rename `provider_event_id`.
- **Challenge:** The bug is real in the source. A simpler fix, "delete the event row if the upsert fails", still loses the race where the process is killed after insert. The transaction is the small fix. It can deadlock if the upsert order disagrees with another transaction that locks subscriptions and billing events the other way. Check lock order before shipping. Under overload, a long transaction holds the event row. Keep the body free of HTTP and the transaction stays short. Shutdown mid-transaction rolls back, and RevenueCat retries. That is the outcome we want.

#### P0.2 Idempotency reservation (F02)

- **Approach:** Before `next`, `SET key in-progress NX EX <lease>`. The winner runs the handler. On success, `SET` the Vapor body with the 24 hour TTL. On failure, delete the in-progress key so a retry can run, except where the handler may have committed. Those routes need their own idempotency in Postgres. Banking and brokers are the ones to inspect first. A peer that sees `in-progress` polls for a bounded time, then returns 409. Redis errors in production return 503 when the header is present.
- **Benefit:** One execution per key under parallel retries. Confidence high on the race. Confidence low that production clients hit it. The change is still justified because the routes move money or import files.
- **Complexity:** Medium. The crash window after commit and before the finished `SET` is the hard part. A 409 is simpler and may be the whole fix if clients already retry on 409 by reusing the key after a delay.
- **Dependencies:** Decide the peer behavior (wait, or 409) before coding. Preserve the cache encoding.
- **Tests:** Parallel pair. Redis `GET`/`SET` error. Crash between handler success and `SET`, then a retry. A 4xx is not replayed. A stored Vapor-format value from a hand-written fixture still replays.
- **Metrics:** Count of idempotency replays, 409s, and 503s. A rise in 409 after deploy means clients are colliding and the new behavior is visible.
- **Rollback:** Revert the middleware. In-progress keys expire on their own. Finished keys stay valid for the old code because the encoding is unchanged.
- **Challenge:** Necessary for correctness of the header's promise, not for a measured duplicate rate. The simpler alternative is 503 on Redis errors only, which does not fix the parallel miss. `SET NX` can stick a key if the process dies. The lease has to be shorter than 24 hours, and the finished body replaces it. Under overload, waiters hold HTTP connections. Prefer 409 over a long poll. Downstream load drops when duplicates stop, and rises if 409 makes clients retry harder. Keep the rate limiter in front.

#### P0.3 Export lifecycle (F03)

- **Approach:** Store the process context on the handler at startup. `generate` uses it. `serve` waits on the export `WaitGroup` with the same deadline and log as P0.4. A cleanup tick marks `pending` older than a chosen budget as `failed`. One in-flight generate per user, enforced with an update from `pending` that a second generator refuses.
- **Benefit:** Deploys stop leaving invisible pending rows, and shutdown stops using a closed pool. Confidence high.
- **Complexity:** Low to medium. The budget needs a number. Start with something larger than the slowest export you are willing to wait for, and log when it fires.
- **Dependencies:** P0.4's shutdown log, so both deadlines are one mechanism.
- **Tests:** Canceled context fails the row. A second process's cleanup fails a stale pending row. Two creates for one user: the second gets a conflict the API already knows how to spell, or queues behind the first. Do not invent a new status the client has never seen without checking the decoder.
- **Metrics:** Count of exports by status, age of the oldest `pending`.
- **Rollback:** Revert. Stale `pending` rows from the new code are still `pending`, which is today's behavior.
- **Challenge:** This is a real leak of work. A simpler change is only the stale-pending update, without the `WaitGroup`. That fixes restart and leaves the pool-close race. Do both. The per-user cap can reject a legitimate second export. Prefer "one running, the second stays pending until the first finishes" over a new error if the client polls status. Shutdown must cancel the context or the wait is just `wg.Wait` with no end.

#### P0.4 Leader connections and a visible shutdown (F04, F05)

- **Approach:** Dial a dedicated `pgx.Conn` for `pg_try_advisory_lock`, run `body` against the pool, unlock with `context.WithoutCancel`, close the lock connection. If `ctx` is done, return before `body`. Keep fail-open when the dial fails and `ctx` is still active. `Jobs.Wait` logs the names still running at 15 seconds. Banking's connection loop returns when `ctx` is done.
- **Benefit:** Removes the pool self-deadlock and makes shutdown observable. Confidence high on the mechanism, medium that production has hit it.
- **Complexity:** Medium. Connection lifetime is the risk. One helper, used by `RunAsLeader` only, so the 29 call sites stay.
- **Dependencies:** Know Postgres `max_connections` before enabling this with `V2_JOBS=all` on a small instance. The extra sessions are one per overlapping tick.
- **Tests:** Pool of 2, two bodies that each query the pool, both finish. Canceled context does not run `body`. Dial failure still runs `body` when the context is live. Shutdown of a blocked body logs and then closes the pool after the body returns.
- **Metrics:** Pool empty-acquire count, lock-dial failures, shutdown timeout log.
- **Rollback:** Revert to pool-held locks. No schema change.
- **Challenge:** Raising `pool_max_conns` is simpler and moves the deadlock to a higher number. It also lets HTTP and jobs consume more of `max_connections`. The dedicated connection is the fix that matches the bug. It can leak sessions if `Close` is skipped. `defer conn.Close()` on every path, including fail-open after a successful dial. Under overload, more lock connections mean less room for HTTP. Cap overlapping leaders only if the metric shows it. Do not cap on day one. A canceled tick that returns without running is a skipped interval, which the next tick covers for pollers. A poller that must not skip has to finish its current item and then look at `ctx`, which is the banking loop change.

#### P0.5 Scenario lease fence (F21)

- **Approach:** Add `lease_owner` to the `WHERE` of renew, complete, and fail. Treat a zero-row update as "someone else owns this run": cancel, return, do not call `achievementhooks.Schedule`. If the heartbeat's read fails, cancel the simulation. Confirm `SCENARIO_PLANNING_ENABLED` in production before scheduling the work ahead of F01. The code bug does not run while the flag is off.
- **Benefit:** One finisher per run. Confidence high on the SQL, medium on production exposure.
- **Complexity:** Low. Three statements and the two call sites.
- **Dependencies:** None. Independent of the pool work.
- **Tests:** As in F21. Also a renewal that still matches the owner and extends the lease.
- **Metrics:** Count of complete/fail updates that matched zero rows, once the statement can report that.
- **Rollback:** Revert the predicates. In-flight runs keep today's last-writer behavior.
- **Challenge:** The claim path is already right, so a new lock would serialize workers the comment at `worker.go:42-45` explicitly avoids. Fencing the three updates is the smaller fix. It does not stop two workers from both spending CPU until the next heartbeat. That window is at most one heartbeat plus the time inside `Process` after the last successful check. Canceling on a transient read error aborts a healthy run. That run is reclaimed after the lease, which is acceptable. A lost lease during shutdown still leaves the row running until expiry, which matches the comment at `worker.go:157-159`. Keep that.

#### P0.6 Target and earnings send state (F06, F07)

- **Approach:** One rule for both pollers. Claim the row (`alert_triggered_at` or the earnings delivery row) in a transaction before any provider call. Send after commit. If the send fails, set a `failed` state that the next poll may claim once, then stop. Inbox publish stays deduped and can stay before the claim. Do not batch quotes in this change.
- **Benefit:** Stops duplicate target pushes and stops treating a failed earnings push as delivered. Confidence high.
- **Complexity:** Medium, because the two pollers disagree today and product has to accept "at most two tries, then stop".
- **Dependencies:** None. Independent of P0.4, and easier to reason about after P0.4 so a slow quote sweep cannot pin the pool.
- **Tests:** Crash between claim and send: one later send, or a recorded failure, never a second success. `Delivered == 0`: one retry, then stop. Two pollers, one target: one mark.
- **Metrics:** Target pushes per target id, earnings retries, `failed` rows.
- **Rollback:** Revert. Rows already marked stay marked, which matches both old and new "do not send again".
- **Challenge:** The comment claims idempotency the code does not implement for the push. Mark-then-send can drop one notification. That is the residual risk and it is better than a duplicate on every poll until the mark succeeds. A queue with retries would be more machinery than these two loops need. Under shutdown, a claimed-but-not-sent row must be retryable, so the claim cannot be the final `alert_triggered_at` unless a separate attempt column exists. Use the attempt column. Cancellation between claim and send leaves `failed` or `claimed`, and the next process retries once.

### P1 — High-impact, after a measurement or a small bound

#### P1.1 Pool, job, and scenario metrics (F20)

- **Approach:** Export `pgxpool.Stat()` and a job-duration histogram from `RunAsLeader`. Call `SetScenarioQueueDepth` from the worker with the count the claim loop already can see. Enable Prometheus in staging.
- **Benefit:** Makes P1.2, P1.5, and every P2 decision about evidence. Confidence high that the gauges are unwired. Confidence high that this does not change request behavior.
- **Complexity:** Low.
- **Dependencies:** A scrape config outside this repo. The code change is still worth it while the flag is off, because a staging flag flip then works.
- **Tests:** The existing metrics test pattern. A worker test that a queued run moves the gauge.
- **Metrics:** These are the metrics.
- **Rollback:** Revert. No data migration.
- **Challenge:** This solves ignorance, not a proven hot path. It is necessary before indexes and batching. Skipping it and "just adding the index" is the failure mode this audit is here to prevent. Cardinality stays bounded by job name. No user id labels.

#### P1.2 Bound or aggregate the heavy reads (F14, F15)

- **Approach:** Staging `EXPLAIN (ANALYZE, BUFFERS)` and row counts first. Then, for expenses, select only the columns the monthly summary reads, or aggregate in SQL if the Go grouping does not need the raw rows. For scenarios, omit `result` and `configuration` from the list queries and keep them on the get-by-id queries. Add a limit only after the client contract is checked.
- **Benefit:** Less memory and fewer buffers per dashboard and scenario list, if the counts are large. If the counts are small, stop after the `EXPLAIN` and do not change the SQL.
- **Complexity:** Low for a column trim. Medium if the JSON shape changes.
- **Dependencies:** P1.1 is helpful and not required for a staging `EXPLAIN`. A staging database is required. Do not run `ANALYZE` against production.
- **Tests:** Monthly summary fixtures with and without `receipt_metadata`. Scenario list fixture with a large `result` that must not appear in the list payload if the select list changes. Detail route still returns it.
- **Metrics:** HTTP latency for the two routes, and `shared_blks_read` from the plan before and after.
- **Rollback:** Revert the query. No schema change in the column-trim version.
- **Challenge:** A missing index is the wrong fix. The indexes match. `SELECT *` is the problem only because the unused columns include jsonb. A limit that changes the streak math is a product bug. Read `dashboard/service.go:151-168` again at implementation time. Offset pagination is worse than a keyset and is not proposed.

#### P1.3 Enable Banking page cap (F09)

- **Approach:** Track the last continuation key. On repeat, or after a constant page cap, return an error. The webhook and the job already have a place to log it (F08).
- **Benefit:** A stuck provider cannot pin the leader until the process dies. Confidence high on the bound, medium on how often the key repeats.
- **Complexity:** Low.
- **Dependencies:** F08's status-code choice can follow. The cap stands alone.
- **Tests:** Repeated key, and a normal multi-page result.
- **Metrics:** Count of cap hits.
- **Rollback:** Revert.
- **Challenge:** A silent partial result would hide missing transactions. Return the error. The cap can cut a legitimate long history. Set it above the maximum page count observed in staging logs once the counter exists. Until then, a cap that only trips on a repeated key fixes the infinite loop and does not cut a long finite history. Ship the repeated-key guard first. Add a numeric cap after one real sync's page count is known.

#### P1.4 Plaid webhook error surface (F08)

- **Approach:** Classify `Sync` errors. Return 500 for transient failures and 200 for permanent ones, with the connection id in the log. Add the `ctx` check in `SyncOnce`'s loop.
- **Benefit:** Plaid retries a blip instead of waiting up to 6 hours plus one interval. Confidence high on the swallowed error. Confidence medium that Plaid's retry schedule is acceptable.
- **Complexity:** Low, once errors are classified. The classification is the work.
- **Dependencies:** Read `errorReason` and the Plaid client before mapping codes.
- **Tests:** Transient error returns 500 and does not mark the connection synced. Permanent error returns 200 and records reauth. Canceled context stops the loop.
- **Metrics:** Webhook 500 rate, sync failure log rate.
- **Rollback:** Revert to 200. Plaid stops retrying, and the poller is the backstop again.
- **Challenge:** Always returning 500 is simpler and can storm. Classification is what makes the change safe. Under overload, retries add provider calls. The poller still runs, so a 200 path remains the backstop if the classification is wrong. Do not shorten the 6 hour first tick in this item.

#### P1.5 Achievement shutdown context (F10)

- **Approach:** Pass the process context from `serve` into the hooks. `WaitUntilIdle` or cancel at shutdown, inside the P0.4 deadline.
- **Benefit:** Evaluations stop using the pool after close. Confidence high. The window is the shutdown race, so the benefit is small and the fix is small.
- **Complexity:** Low.
- **Dependencies:** P0.4's deadline.
- **Tests:** As in F10.
- **Metrics:** None beyond the shutdown log.
- **Rollback:** Revert.
- **Challenge:** A process-context parameter is enough. A worker pool is not. Cancel is safe because `Evaluate` rolls back. Do this with P0.4 so shutdown has one order: stop HTTP, cancel process context, drain Telegram, wait jobs and exports and achievement hooks, close the pool.

### P2 — After P1.1 shows they are worth it

#### P2.1 Batch quotes on the pollers (F06 loop, F16, F18)

- **Approach:** Only if the provider-call counter from a small log, or pprof, shows `Quote` dominating a tick. Add one helper that takes distinct symbols, uses `FMP.StockQuotes` when the provider is FMP, and falls back to `Quote` otherwise. Point the target poller and `currentValue` at it. Leave `QuoteBatch`'s HTTP behavior alone until the same evidence says the HTTP route is hot. `singleflight` on `Quote` waits on that same evidence, and the shared call needs its own timeout.
- **Benefit:** Fewer provider calls per tick. Unmeasured. Confidence low until the counter exists.
- **Complexity:** Medium, because stale-cache and empty-quote behavior in `fetchQuote` must survive.
- **Dependencies:** P1.1 or an equivalent log. P0.5 should land first so the poller state machine is stable.
- **Tests:** Same totals and warning strings. One HTTP call per distinct symbol on a fake FMP. A batch error falls back per symbol.
- **Metrics:** Provider calls per target-alert tick, per goal evaluation.
- **Rollback:** Revert the helper's callers.
- **Challenge:** The batch endpoint already exists, which is why this is not a new client. It is still unnecessary if the 20 second cache hits. Shipping it "because N+1 is bad" without a miss count is how provider behavior changes for no gain. A batch that fails open into N calls under 429 is worse than today's serial loop. Honor the existing 429 cache in `fmp.go`.

#### P2.2 Read and idle timeouts (F13)

- **Approach:** Set `IdleTimeout`. Set `ReadTimeout` from the staging latency histogram, above the slowest successful non-stream handler. Leave `WriteTimeout` unset.
- **Benefit:** Slow clients and idle connections get closed. Confidence high on the missing fields, low on current abuse.
- **Complexity:** Low.
- **Dependencies:** A histogram that includes slow routes, or a deliberate conservative idle timeout that does not touch reads yet.
- **Tests:** The server config test if one exists. A stall test is an integration test and optional.
- **Metrics:** Inflight connections, client-canceled requests.
- **Rollback:** Revert the two fields.
- **Challenge:** `IdleTimeout` alone is the simple version and does not cut uploads. Do that first if there is any doubt about `ReadTimeout`. A global write timeout breaks SSE. That is why it is absent.

#### P2.3 Group fan-out and Telegram cap (F11, F12)

- **Approach:** Process context for group `detach`. A fixed cap on Telegram `inflight`, with 200 and a log when full.
- **Benefit:** Bounds two goroutine sources. Confidence high on the missing cap, low on the flood ever happening.
- **Complexity:** Low.
- **Dependencies:** P0.4 shutdown order for the group context.
- **Tests:** Cap test. Shutdown cancels a group fan-out.
- **Metrics:** Dropped Telegram updates, in-flight turns.
- **Rollback:** Revert.
- **Challenge:** A cap that returns non-200 makes Telegram retry, which makes the flood worse. Return 200. Dropped messages are the cost. If that is unacceptable, the cap is the wrong tool and the work belongs in a real queue with a lease. This codebase has no such queue. Do not add one for Telegram.

#### P2.4 Shorten the achievement transaction only if it is long (F17)

- **Approach:** Log transaction duration. Split `measure` out of the `FOR UPDATE` only when the log says so, and re-read `needs_backfill` inside the short transaction.
- **Benefit:** Shorter row locks for one user. Unknown until timed.
- **Complexity:** Medium. Easy to get the backfill flag wrong.
- **Dependencies:** A duration log.
- **Tests:** Concurrent evaluations, backfill flag, no duplicate unlocks.
- **Metrics:** Transaction duration.
- **Rollback:** Revert to one transaction.
- **Challenge:** The current shape is deliberate. Leave it if the duration is small. A split that is wrong double-awards XP. The test is the gate.

### P3 — Optional, evidence required

#### P3.1 News symbol indexes (F19)

Staging `EXPLAIN` only. Add an index only if the plan and the write rate justify it. One migration, `CONCURRENTLY` if the table is large enough that a lock matters. The baseline migration style may not use `CONCURRENTLY`. Match the repo's migration practice, and do not rewrite the baseline.

#### P3.2 One-shot reclaim of stuck `pending` notification deliveries

A job that flips `pending` older than a few minutes to `failed`, once, lets the existing `claim` path retry `failed`. Do not retry `delivered`. Do not loop. Digest and market brief stay at-most-once unless their comments are revised on purpose.

#### P3.3 Partial index on untriggered targets

Only after `EXPLAIN` on `UnresolvedTargets` shows a sequential scan that dominates the tick. The quote loop's provider time will dominate a small table. The index does not fix F06.

---

## Leave unchanged

- **Job-lock fail-open.** `joblock.go:9-10` runs the tick when the database cannot be locked, matching Vapor. Removing it stops pushes and syncs during an outage. P0.4 keeps it, and refuses to run when the process is already shutting down.
- **Realtime Redis pub/sub.** The bus is correct for one replica, and the docs say to keep one replica. A bus rewrite is a product decision about replica count, not a performance fix.
- **A global `WriteTimeout`.** SSE and pprof streams need the write side open. `Shutdown` does not wait for hijacked responses.
- **`sync.Pool`, pointer conversions, and allocation passes.** No profile shows allocation pressure. There is no `sync.Pool` today, which is a fine steady state. Adding one without a heap profile adds use-after-put bugs.
- **A worker pool for style.** The scenario worker, quote chunks, and receipt semaphore already bound the places that fan out. New pools need a measured queue.
- **Raising `DATABASE_MAX_CONNECTIONS` as the F04 fix.** It delays the stall and spends server connections.
- **An extra index on the scenario claim.** `idx_scenario_runs_state_lease_expires_at` matches the claim filter (`00001_baseline.sql:5036`). `ORDER BY created_at` may still sort. That is a hypothesis. Do not add `(state, created_at)` until a plan shows a sort that matters. The claim is `LIMIT 1`.
- **A `transactions (user_id)` index.** The column does not exist. User scope goes through accounts.
- **Retries on provider 4xx, AI chain 4xx, or a new retry layer around webhooks.** Non-retryable failures would become storms. F08 adds a retry only by telling Plaid the truth. Idempotency keys on DeepAPI are minted per POST today. Do not wrap that in a blind retry.
- **Rewriting reflection-based JSON decoding.** It is on the request path and it is not shown to be hot.
- **Treating `go vet` and `staticcheck` as a race clean bill.** They passed. `go test -race` did not run.

---

## Original suggested order

Superseded. Use [Revised roadmap](#revised-roadmap). The list below is kept so the review can be checked against what was proposed.

1. P1.1 metrics, in the same release as P0.4 if possible, so the pool change has a before and after.
2. P0.1 billing transaction.
3. P0.2 idempotency.
4. P0.5 scenario lease fence if `SCENARIO_PLANNING_ENABLED` is on in production. If it is off, do this after the billing and idempotency changes and before turning the flag on.
5. P0.6 target and earnings state.
6. P0.3 exports and P0.4 lock connection plus shutdown log, together, because they share the shutdown sequence.
7. P1.3 repeated continuation key.
8. Staging `EXPLAIN` for P1.2 and P3.1. Stop if the plans are cheap.
9. P1.4 Plaid status codes after error classification.
10. Everything in P2 only with a profile or a counter in hand.

No item in this plan should be implemented until this document is reviewed. Schema migrations, pool size, and replica count stay as they are until that review names them.
