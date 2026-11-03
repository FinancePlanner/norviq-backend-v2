# Go backend engineering audit — norviq-backend-v2

Date: 2026-11-03. Skill: `go-backend-audit` (default mode, read-only). Report only: no production code, schema, configuration or infrastructure was changed.

## Executive summary

**Source revision.** HEAD `81ce5ec37be56b8251c49d43f8fd2700aa3c8642`. The working tree is dirty and was reviewed as is: about 50 modified files (the job-lock session, shutdown drain, billing transaction, scenario fence, watchlist order, metrics, exports, groups) plus untracked `internal/platform/joblock/export_test.go`. The uncommitted diff's sha1 starts `167b3614c135`. `Golang-audit-plan.md` (untracked) was deliberately not read, so this audit stays independent of it. The security audit ran separately. Its findings are not merged here.

**Deployment facts used.** These come from the infra repo, read-only via `git show origin/main:apps/norviq/api-v2/values-*.yaml` (origin/main `3cffa03`, 2026-11-03):
- `replicaCount: 1`
- memory limit 450Mi, request 256Mi; CPU request 200m
- no GOMEMLIMIT
- RollingUpdate with `maxSurge: 1`
- `V2_JOBS=all`
- `SCENARIO_PLANNING_ENABLED=true`
- `PROMETHEUS_ENABLED=true`
- readiness `/health/ready` every 10s with `failureThreshold 6` and no `timeoutSeconds` (kubelet default 1s)
- no `terminationGracePeriodSeconds` (default 30s)
- `DATABASE_MAX_CONNECTIONS` at its default of 20

**Method.** Four independent read-only lanes, each followed by a parent review: request path, background jobs and concurrency, database, and integrations/reliability. I re-checked every finding marked P1, and every finding whose classification rests on one specific line, against source before including it (see the Evidence log).

**Executed on this snapshot** (before the audit, on a disposable local Postgres 16):
- `go vet ./...`: clean
- `golangci-lint run ./...`: 0 issues
- `go test -race -count=1 ./...`: all 83 packages ok

No profiles, benchmarks, load tests, `EXPLAIN` plans, production row counts or `pg_stat_*` data exist. **No finding is a MEASURED BOTTLENECK.** Byte counts below are arithmetic on constants in the code.

**Top risks.**
- **Availability.** One process (the single replica) serves HTTP, SSE, about 40 jobs and the scenario worker under 450Mi. Several authenticated inputs within today's validation limits can push that process past its memory limit (GO-01, GO-02). A Redis blip marks it NotReady (GO-03).
- **Reliability.** Nothing puts a deadline on DB statements, pool acquires, job ticks or some outbound calls (GO-04, GO-05). Claimed work can be stranded forever after a crash (GO-07).
- **Correctness.** Telegram updates are deduplicated by a racy high-water mark, so messages drop silently (GO-06). RevenueCat events apply in arrival order (GO-10). Three small check-then-act races exist (GO-20 to GO-22).
- **Database.** Several hot or growing queries have no usable index (GO-15 to GO-18). All are **MEASURE FIRST**.

**Top 5 interventions** (strongest evidence, smallest change):
1. **GO-01:** cap scenario Monte Carlo work and add a poison-run guard. Arithmetic shows one maximum-size run is about 229 MiB of live heap and two exceed the pod limit. Crashed runs are re-claimed in a loop.
2. **GO-03:** report Redis unreachable as `degraded` in readiness and bound each check. Today a Redis blip takes the only replica out of service.
3. **GO-04:** server-side `statement_timeout`, `idle_in_transaction_session_timeout` and `lock_timeout`, plus per-tick deadlines. Without them a stuck statement silently holds a pool connection and a job lock forever.
4. **GO-06:** make the Telegram update claim atomic and claim before transcribing.
5. **GO-07:** reclaim stranded advanced-report runs, and record failures with a non-canceled context.

**Not inspected or not verified:** see the "Not inspected" section. The main gaps are the production Postgres settings and statistics, real traffic and workload, the MCP client, and most of the 693 handlers individually.

## Findings table

| ID | Priority | Category | Status | Location | Impact | Proposal | Verification |
|---|---|---|---|---|---|---|---|
| GO-01 | P1 | memory / availability | CODE-SUPPORTED RISK (arithmetic, not observed) | scenarios/runs.go:378-379, engine.go:329-334, worker.go:120 | OOM-kill of the only replica; crash loop on re-claim | work budget + attempt cap | memprofile of one max run |
| GO-02 | P1 | memory / availability | CODE-SUPPORTED RISK | receipts/handlers.go:89-141, multipartform.go:147-163, ai/vision.go:62-81 | concurrent 26 MB uploads can OOM the pod | GOMEMLIMIT, large-body semaphore, gates before read | heap pprof under 1–3 concurrent scans |
| GO-03 | P1 | availability | CODE-SUPPORTED RISK | server/health_core.go:35-51, server.go:236-275 | Redis or pool saturation makes the API NotReady | Redis→degraded, bounded concurrent checks | readiness transitions |
| GO-04 | P1 | database / reliability | CODE-SUPPORTED RISK | cmd/api/main.go:111-131, config/config.go:62-71 | no deadline on any statement, acquire or tick | DB runtime params + per-tick ctx timeout | `SHOW statement_timeout`, pg_stat_activity |
| GO-05 | P2 | integrations | CODE-SUPPORTED RISK | aiplatform/feature.go:35, insights/feature.go:238, assistant/jobs.go:305-330, receipts OCR | a stalled upstream hangs a job tick (holding its lock) or a request | client Timeout / ctx timeouts, LimitReader | blocking stub test |
| GO-06 | P1 | concurrency / correctness | CONFIRMED DEFECT | assistant/messaging.go:156-174, messaging.sql:9-12, telegram_bridge.go:148-172 | Telegram messages silently dropped or processed twice | conditional claim, claim before transcription, per-chat serialization | concurrent webhook test |
| GO-07 | P2 | reliability | CONFIRMED DEFECT (ported) | reporting.sql ClaimRun, reporting/worker.go:412-416,426,488,712 | report runs stuck in `generating` forever | reclaim stale runs; WithoutCancel in failOrRetry | cancel-mid-render test |
| GO-08 | P2 | concurrency / shutdown | CODE-SUPPORTED RISK | cmd/api/main.go:232-256, telegram_bridge.go:93-106 | shutdown budget > 30s grace; unbounded Telegram drain; WaitGroup Add/Wait race on the timeout path | single deadline, bounded drains, grace 45s | serve-level shutdown test |
| GO-09 | P2 | concurrency | CODE-SUPPORTED RISK | features.go:456, scenarios/worker.go:90,148,226, achievementhooks, groups notifier, dataexport, telegram | a panic in any detached goroutine kills the process | recover at tick and goroutine boundaries, fail the claimed row | panicking-tick test |
| GO-10 | P2 | reliability / billing | CODE-SUPPORTED RISK | billing/webhook.go:54-117,270-358, subscriptions.go:62-77 | an older retried event overwrites a newer entitlement | MEASURE FIRST, then ordering guard | read-only inversion SQL |
| GO-11 | P2 | reliability | CONFIRMED DEFECT (rare trigger) | banking/provider.go:136-148, assistant/messaging.go:113-123, scenarios/worker.go:306-313 | INCR then EXPIRE leaves TTL-less keys: permanent lockout or stopped sync | shared atomic Lua helper | fake-Redis test; `TTL` scan |
| GO-12 | P2 | reliability | CODE-SUPPORTED RISK | banking/job.go:44-55, banking/webhook.go:192-208 | bank backstop never runs on days with deploys under 6h apart; webhook sync failures not recorded | short initial delay or last_synced_at selection; record errors | injected-clock test |
| GO-13 | P2 | integrations / cost | CONFIRMED DEFECT (cancel) / RISK (latency) | platform/ai/chain.go:83-160 | client cancel triggers false "all providers failed" page; no rung cooldown | ctx.Err() short-circuit, 402 cooldown, turn deadline, async alert | stub-server tests |
| GO-14 | P2 | performance / correctness | CONFIRMED DEFECT | marketdata/service.go:283-316, portfolio/valuation.go:89-105 | one unquotable symbol drops live prices for 100 holdings | per-symbol partial results for valuation | stub test |
| GO-15 | P2 | database | CODE-SUPPORTED RISK / MEASURE FIRST | goalplanning.sql:141-195, baseline.sql:824,852,864 | goal job and goal list do N+1 full scans of growing tables | 1–3 indexes | EXPLAIN on staging |
| GO-16 | P2 | database | CODE-SUPPORTED RISK / MEASURE FIRST | lot_disposals / lots, portfolio.sql:149-167, brokers.sql:175-215 | summary/P&L anti-join and IBKR sync scan globally | `lot_disposals(transaction_id)`, `lots(open_transaction_id)` | EXPLAIN on staging |
| GO-17 | P2 | database | CODE-SUPPORTED RISK | budgeting.sql:44, financing.sql:104, planning.sql:31, runs.sql:145 | four queries cannot use the only index | add `AND user_id` (after a data check) or index | data check + EXPLAIN |
| GO-18 | P2 | database | CODE-SUPPORTED RISK / MEASURE FIRST | watchlist/jobs.go:323-365, items.sql:50-53 | earnings sweep: 2 full scans per calendar symbol | batch with existing `= ANY` queries | EXPLAIN |
| GO-19 | P2 | rate limiting | CODE-SUPPORTED RISK | middleware/ratelimit.go:41-79; pre-auth mounts; config.go:44 | all in-cluster MCP users share one per-IP bucket | per-user limiter after auth | two-user test |
| GO-20 | P3 | concurrency | CONFIRMED DEFECT | scenarios/runs.go:583-598, runs.sql:81-83 | cancel can flip a completed run to cancelled | fence cancel on state | stubbed race test |
| GO-21 | P3 | concurrency | CONFIRMED DEFECT (low impact) | groups/service.go:201-234 | 250-member cap and member_count can drift | lock group row first | concurrent-join test |
| GO-22 | P3 | concurrency / DB | CONFIRMED DEFECT + MEASURE FIRST | expenses/service.go:1312-1331, baseline.sql:3661 | duplicate default categories on concurrent first read; no user_id index | advisory-lock seeding; index | race test; EXPLAIN |
| GO-23 | P2 | middleware | CONFIRMED DEFECT (headers) / RISK (race) | middleware/ratelimit.go:85-138, server.go:107 | idempotent replay duplicates headers and can mislabel gzip | header allowlist, Set, WithoutCancel | full-stack replay test |
| GO-24 | P3 | job lock | CODE-SUPPORTED RISK | platform/joblock/joblock.go:150-176 | a saturated pool reads as "DB down" and fails open; session loss mid-tick is unnoticed | classify PgError as "answered"; tick deadline | PgError 53300 test |
| GO-25 | P3 | jobs | CODE-SUPPORTED RISK (parity) | rebalancing/alertjob.go:119-128, digest/runner.go:89-99, reporting/worker.go:167-188, tax/jobs.go:107-117 | one bad row starves the rest of the tick | log, count, continue per item | per-loop test |
| GO-26 | P3 | request path | CODE-SUPPORTED RISK | dashboard/whymoved.go:324-370 | GET waits up to 60s per rung on a slow LLM; failures not cached | own budget + negative cache | blocking stub test |
| GO-27 | P3 | DB lifecycle | HYPOTHESIS / MEASURE FIRST | accountdeletion/feature.go:151-165; 116 unindexed FKs | one long cascade transaction for heavy users | rank by size, index the large children | staging EXPLAIN in ROLLBACK |
| GO-28 | P3 | observability | CODE-SUPPORTED RISK | metrics.go:96-105,281-293, httpx/errors.go:129-163 | one global histogram; SSE distorts it; client cancel logged as 500 | new route-labelled series; 499 for cancel | render test |
| GO-29 | P3 | integrations | CONFIRMED DEFECT | marketdata/finnhub.go:266-272 | Finnhub 429 not remembered | mirror the FMP Redis back-off key | stub test |
| GO-30 | P3 | MEASURE FIRST bundle | HYPOTHESIS | see section | quote singleflight, APNs concurrency, per-tick growth, stale-quote alerts, download streaming, scenario retention race | measure first | counters/profiles |

## Findings

### GO-01 — Monte Carlo scenario runs inside validated limits can OOM the only replica, and the crashed run is re-claimed forever — P1

**Classification:** CODE-SUPPORTED RISK. The cost is derived by arithmetic from limits in the source; no OOM was observed. Source lanes: jobs JOB-01.

**Evidence** (re-checked):
- `internal/features/scenarios/runs.go:378-379` accepts `path_count` up to 50,000 and `horizon_months` up to 600.
- `engine.go:329-334` (`newColumns`) allocates `months+1` slices, each with capacity `paths`. It is called by the net-flow, correlated and simulate stages (`engine.go:376,482,615`).
- `bandsFrom` (`engine.go:337`) adds a sorted copy per column.
- One maximum-size column set is 601 × 50,000 × 8 B = 240 MB (229 MiB).
- The worker runs `maxConcurrent` = 2 claims in parallel (`worker.go:120,139-151`).
- Production has `SCENARIO_PLANNING_ENABLED=true` and a 450Mi limit with no GOMEMLIMIT.
- `ClaimScenarioRun` re-claims `running` rows whose lease has expired. It has no attempt cap.

**Failure scenario.**
1. Two maximum-size runs, or one plus sorting garbage under GOGC=100, push the process past 450Mi.
2. The kubelet kills the pod. That ends all HTTP traffic, SSE streams and jobs, because they share the process.
3. About 60 seconds after restart, the expired leases are re-claimed and the cycle repeats.
4. It continues until someone cancels the rows by hand.

This is Vapor parity, but the harm is real. It is also a denial-of-service path for any Pro user. That cross-boundary aspect belongs to the security audit and was not assessed here.

**Proposal.**
1. A work budget on `paths × (months+1)` per run. The owner chooses either to reject at validation (a 400 for input accepted today, which is a wire change) or to clamp and add a warning.
2. A poison guard: a nullable `attempt_count` column incremented by the claim. After 3 claims, the run is marked failed.

**Simpler alternative:** set `SCENARIO_WORKER_MAX_CONCURRENT=1` and GOMEMLIMIT of about 380MiB in values, and fail any run at claim time whose `started_at` is older than N leases (`started_at` survives re-claim through COALESCE). This needs no schema change.

**Implications:**
- Clamping changes the percentiles a client sees.
- Do not switch to quantile sketches without an owner decision.
- A clamp must be applied in the same place for the dedupe hash, so cached results stay consistent.

**Tests:**
- A unit test that the cell count stays within budget for the maximum validated configuration.
- A worker test where a run whose worker "dies" three times ends `failed`.

**Measure:**
- `go test -memprofile` of one maximum run.
- Container RSS during a staging run.
- `scenario_queue_depth` and the pod restart count.

**Rollback:** revert the env values or the clamp. The column is additive.

### GO-02 — Image-upload routes buffer and copy up to 26 MB several times before cheap gates run — P1

**Classification:** CODE-SUPPORTED RISK. Source lanes: request REQ-01.

**Evidence:**
- `receipts/handlers.go:94` reads the whole body (26 MB cap, `server.go:62,290`).
- `multipartform.go:56,147-163` copies each part again.
- `ai/vision.go:62-81` builds a base64 string, then a data-URL string, then a JSON buffer, each about 10.7 MB per 8 MB image, with up to 3 in flight.
- The consent check (`:101`) and the Pro gate (`:112`) run after the body is buffered.
- The broker screenshot route has the same shape (`brokers/handlers.go:334`, `screenshot.go:279-293`).
- Peak live heap per request is roughly 100–150 MB. This is arithmetic.

**Impact:** two or three concurrent scans can OOM-kill the only replica (450Mi, GOGC=100, no GOMEMLIMIT).

**Proposal:**
1. Set GOMEMLIMIT to about 80% of the limit.
2. Add a process-wide semaphore of 1–2 on the large-body routes, failing fast with 503.
3. Run the cheap gates before `ReadBody`.
4. Later, stream the parts and base64-encode into the writer.

**Simpler alternative:** steps 1–3 only.

**Implications:**
- `docs/vapor-bugs/money.md` F5 keeps "413 before Pro gate". Reordering changes only error precedence, so the owner must confirm.
- A 503 when the semaphore is full is a new status for clients.

**Tests:**
- A free user is rejected without the body being read (byte-counting reader).
- A third concurrent call is rejected.

**Measure:** heap pprof with 1, 2 and 3 concurrent 3×8 MB scans against a stub vision server.

**Rollback:** remove the env var and the semaphore.

### GO-03 — Readiness fails on a Redis blip and waits on the DB pool, so the single replica leaves service — P1

**Classification:** CODE-SUPPORTED RISK (a parity trade-off). Source lanes: request REQ-03.

**Evidence** (re-checked):
- In `server/health_core.go`, Redis not configured gives `degraded` in production, "because failing readiness would take the whole single-replica API down". But a failed Redis ping gives `unhealthy` (`:47-50`).
- The database check runs `SELECT 1` through the shared pool (`:28`).
- Checks run serially with no deadline (`server.go:236-275`).
- The production probe is every 10s, failureThreshold 6, with a 1s default timeout.

**Impact:**
- A Redis outage of more than about 60s, or pool saturation (Acquire waiting past the probe timeout), marks the pod NotReady.
- Traefik then returns 503 on every route, including the majority that never touch Redis.
- Rate-limited routes already fail closed on their own (`ratelimit.go:55-59`).

**Proposal:**
1. Report Redis unreachable as `degraded`.
2. Give each check a deadline of about 800 ms and run the checks concurrently.
3. Treat a pool-acquire timeout as `degraded`, but keep a real query error `unhealthy`.
4. Set probe `timeoutSeconds: 3`.

**Simpler alternative:** step 1 plus `timeoutSeconds`.

**Tests:**
- A failing Redis stub returns 200 with `degraded`.
- A DB check that exceeds its deadline returns `degraded` within that deadline.

**Measure:** `kube_pod_status_ready` and pool `EmptyAcquireCount`.

**Rollback:** revert.

### GO-04 — No deadline reaches the database, the pool or a job tick — P1

**Classification:** CODE-SUPPORTED RISK. Source lanes: DB-01, REQ-05, and the tick part of DB-11.

**Evidence:**
- `openDB` is `pgxpool.New(DSN)` (`cmd/api/main.go:111-131`). The DSN sets only `sslmode` and `pool_max_conns` (`config.go:62-71`).
- No `statement_timeout`, `lock_timeout` or `idle_in_transaction_session_timeout` is set outside the migrations.
- There is no request-deadline middleware, and job ticks run on the process context.

**Impact:**
- A query blocked on a lock holds a pool connection with no bound. The pool has 20 connections, shared by HTTP, about 40 jobs, up to 8 scenario workers, achievement evaluations and fan-out.
- Inside a job, the tick also keeps its advisory lock, so that job silently never runs again until restart.
- Migrations using `CREATE INDEX CONCURRENTLY` wait on any idle-in-transaction session.

**Proposal:**
- Set `RuntimeParams` per connection through `pgxpool.ParseConfig`: `statement_timeout`, `idle_in_transaction_session_timeout` and `lock_timeout`. The values are a starting point to tune against measured p99, not measured themselves.
- Wrap each tick in `context.WithTimeout` (per job, at least its interval).
- Optionally add a request-context deadline middleware that exempts `/v1/realtime/stream` and the upload routes.

**Exemptions:**
- `runMigrate` shares `openDB`, so it must override `statement_timeout`.
- Exports, the reporting worker and tax report generation need `SET LOCAL` or a separate configuration.

**Simpler alternative:** `ALTER ROLE … SET …` in infra, plus per-tick timeouts in Go.

**Do not** set the `http.Server` `WriteTimeout` or `ReadTimeout`, and do not use Echo's Timeout or `http.TimeoutHandler`: they cut SSE and slow uploads.

**Tests:**
- A `pg_sleep` longer than the timeout fails with 57014.
- A handler blocked on `ctx.Done()` returns within its budget.

**Measure:** `SHOW statement_timeout` as the API role on staging, `pg_stat_activity` idle-in-transaction age, and the pool `AcquireDuration` and `EmptyAcquireCount`.

**Rollback:** remove the params or reset the role.

### GO-05 — Some outbound calls have no deadline or response-size cap — P2

**Classification:** CODE-SUPPORTED RISK. Source lanes: INT-06, JOB-07, REQ-02.

**Evidence:**
- `aiplatform/feature.go:35` passes `&http.Client{}`, which has no timeout. It is used by the daily-tip call (`assistant/jobs.go:305-330`, on the job context) and by receipt OCR/vision (`receipts/feature.go:44`, `ai/vision.go:73-91`). Unlike the chat adapters, neither adds `context.WithTimeout`.
- `insights/feature.go:238-241` also uses `&http.Client{}`, for StockTwits/Reddit (`direct.go:239-253,333-345`) and the FMP seeder.
- These reads have no `io.LimitReader`: `banking/plaid.go:197`, `brokers/gateway.go:139`, `crypto/coingecko.go:223`, `macro/fred.go:457`, `macro/calendars.go:143`, `insights/direct.go:252`, `insights/provider.go:339`, `insights/deepapi.go:494`, `pilots/edgar.go:440,466`.

**Impact:** an upstream that accepts the connection and never answers holds the job tick, and its job lock, until restart. For a request, it holds the handler and its upload buffers (GO-02).

**Proposal:**
- Set `Timeout` on those clients, or wrap each call in a context with `AI_REQUEST_TIMEOUT_SECONDS`.
- Add LimitReader caps matching the existing 16–32 MB.

**Do not** add a generic retrying transport (see Rejected).

**Tests:** a blocking httptest server; assert the call returns within the timeout.

**Rollback:** trivial.

### GO-06 — Telegram dedupe is a racy high-water mark: messages are dropped silently or answered twice — P1

**Classification:** CONFIRMED DEFECT. Source lanes: INT-01.

**Evidence** (re-checked):
- `assistant/messaging.go:156-174` reads the link, compares `UpdateID <= LastUpdateID`, then calls `ClaimMessagingUpdate`. That call is a blind `UPDATE … SET last_update_id = @x WHERE id = @id` (`messaging.sql:9-12`).
- Each webhook update runs in its own goroutine (`telegram_bridge.go:148-172`).
- The voice path transcribes, up to 120s, before `handle` claims the update (`:233-291`).
- A rejected update returns quietly with no log.

**Failure scenarios:**
- A voice note (update 100) is still transcribing when a text (update 101) is claimed. The voice note is then rejected and never answered.
- Two goroutines both read before either writes, so the same update is processed twice: a double LLM turn and a double reply.
- "yes" confirmations depend on order, so they are racy too.

**Proposal:**
- Make the claim conditional: `… WHERE id = @id AND last_update_id < @x`, with `:execrows`, and treat 0 rows as a duplicate.
- Claim at the top of `transcribeAndAnswer`.
- Then serialize per chat (one worker per chat ID) to preserve order.
- Log rejections at debug level with both IDs.

**Simpler alternative:** the conditional claim plus the earlier claim. This closes the double-processing; ordering still needs serialization.

**Tests:**
- Use a stub Telegram API and a blocking stub whisper. Post voice update 100, then text update 101, and assert two answers.
- Post the same ID twice concurrently and assert one answer.

**Rollback:** revert the query (no schema change).

### GO-07 — Advanced-report runs strand in `generating` or `delivering` after shutdown or crash — P2

**Classification:** CONFIRMED DEFECT (ported from `AdvancedReportWorker.swift`). Source lanes: JOB-03.

**Evidence:**
- `ClaimRun` reclaims only `status='claimed' AND claimed_at < now()-10m`.
- The worker sets `generating` at `reporting/worker.go:426` and `delivering` at `:488`.
- `failOrRetry` (`:712`) calls `GetRun` with the already-canceled job context, so on SIGTERM it returns silently.
- A stranded run is never retried, and `next_run_at` has already moved past the period.

**Same shape elsewhere:**
- The digest claim leaves `pending` after a crash. This is at-most-once by design.
- `data_exports` rows stay `pending` after SIGKILL.

**Proposal:**
- In `failOrRetry`, use `context.WithTimeout(context.WithoutCancel(ctx), 5s)`.
- Reclaim stale `generating` rows (for example `updated_at` older than 15 minutes) within the existing `attempt_count < 3`.
- Do not reclaim `delivering` into delivery. `RetryableDeliveries` owns the deliveries, and reclaiming there can re-send email.

**Tests:** cancel during render, advance the clock, and assert the run is re-claimed with an incremented attempt count.

**Measure:** read-only `SELECT status, count(*) … WHERE status IN ('generating','delivering') AND updated_at < now()-'1h'`.

**Rollback:** revert.

### GO-08 — Shutdown budget exceeds the 30s kubelet grace; two waits are unbounded; WaitGroup misuse on the timeout path — P2

**Classification:** CODE-SUPPORTED RISK, established by the production values. Source lanes: JOB-04, REQ-10.

**Evidence:**
- The shutdown sequence in `cmd/api/main.go:232-256`:
  - HTTP `Shutdown` up to 20s
  - `DrainTelegramTurns` with no bound
  - the 15s drain
  - `LockSession.Close`
  - `pool.Close`, which waits for every acquired connection
- The chart sets no `terminationGracePeriodSeconds` or preStop.
- After a `Shutdown` timeout, still-running handlers can call `h.wg.Add` (dataexport) or `inflight.Go` (groups) while `RunShutdownHooks` is in `Wait`. That is documented WaitGroup misuse.
- With maxSurge 1, the listener closes before Traefik removes the endpoint.

**Impact:** SIGKILL cuts the drain short, which feeds GO-07. Requests that arrive during rollout fail.

**Proposal:**
- On SIGTERM, set readiness to draining and sleep about 5s.
- Use one overall deadline: HTTP ≤ 15s, then the remainder for all drains, with the Telegram drain bounded by it.
- Bound `pool.Close` with a 3s select.
- Stop accepting detached work (exports, fan-out) before waiting: a closed flag checked before `Add`.
- Set `terminationGracePeriodSeconds: 45`.

**Simpler alternative:** chart-only preStop `sleep 5` and a 45s grace.

**Tests:** a serve-level test where a slow handler and a still-running job do not block return past the budget.

**Rollback:** revert.

### GO-09 — No detached goroutine recovers panics — P2

**Classification:** CODE-SUPPORTED RISK. No concrete panic trigger was found. Source lanes: JOB-02.

**Evidence:** `grep recover()` finds only `echomw.Recover()` for HTTP (`server.go:110`). There is no recover in job goroutines (`features.go:456`), scenario workers (`worker.go:90,148,226`), achievement hooks, group fan-out, dataexport, Telegram turns or market brief.

**Impact:** one nil dereference on unexpected provider data ends the process. For claimed or leased work, it repeats after every restart.

**Proposal:** a `recoverTick` helper used inside `joblock.run` and at each detached goroutine. It logs the stack, increments `job_panics_total{job}` and continues. A recovered scenario `execute` must also fail the run, fenced, so it is not re-claimed.

**Reject** a recover that only logs: it turns a crash into a row that stays stuck.

**Tests:** a tick that panics once; the process survives, `held` is cleared and the next tick runs.

**Rollback:** remove the wrapper.

### GO-10 — RevenueCat events apply in arrival order — P2, MEASURE FIRST

**Classification:** CODE-SUPPORTED RISK. Source lanes: INT-02.

**Evidence:**
- `event_timestamp_ms` is never decoded (`billing/webhook.go:54-117`).
- Status and level come from the event type alone (`:270-358`).
- `SubscriptionByTransaction` is a plain read (`subscriptions.go:62-77`).
- The uncommitted one-transaction change is correct, and it makes a failed event redeliverable. That widens the reorder window: a failed BILLING_ISSUE retried after a successful RENEWAL downgrades a paying user.

**Proposal:** first run the read-only inversion count. For each user, count pairs in `billing_events` where `created_at` order disagrees with `raw_payload` `event_timestamp_ms` order. If it is non-zero, either:
- add a nullable `subscriptions.last_event_at` and skip older events (always inserting the event row, and locking the subscription row `FOR UPDATE`), or
- derive state from RevenueCat's subscriber API.

Tax-pack events need the same guard.

**Tests:** a signed RENEWAL at t2, then a BILLING_ISSUE at t1 < t2; the user stays pro.

**Rollback:** an env flag on the skip.

### GO-11 — INCR then EXPIRE outside the limiter leaves keys with no TTL — P2

**Classification:** CONFIRMED DEFECT (rare trigger, lasting effect). Source lanes: INT-03.

**Evidence:**
- `middleware/ratelimit.go:20-30` already fixed this pattern with a Lua script.
- Still unfixed:
  - `banking/provider.go:136-148`: PSD2 daily budget, no date in the key, `Expire` error discarded.
  - `assistant/messaging.go:113-123`: Telegram command limiter.
  - `scenarios/worker.go:306-313`: `SET … 0` then `EXPIRE`.
  - `ai/cap.go:79-82` and `transcription.go:423-426`: these leak keys only.

**Impact:** a pod killed between the two commands leaves an EU bank account that never syncs again, or a chat that is rate-limited forever, with no error shown to the user.

**Proposal:** share the existing script. Use `SET key v EX 86400` for the scenario cache.

**Measure:** read-only `redis-cli --scan` of those prefixes, plus `TTL`.

**Rollback:** trivial (same keys).

### GO-12 — The bank-sync backstop waits a full interval after every restart; webhook sync failures are not recorded — P2

**Classification:** CODE-SUPPORTED RISK. Source lanes: INT-04.

**Evidence:**
- `banking/job.go:44-55` arms `NewTimer(interval)` (6h) before the first tick.
- `banking/webhook.go:192-208` logs a failed Plaid sync but does not call `UpdateConnectionSyncError`.

**Impact:** on days with deploys less than 6h apart, EU aggregators get no background sync at all, and a failed Plaid webhook sync is not retried.

**Proposal:**
- Select connections by `now - last_synced_at > interval`, with a short initial delay. This is restart-proof and respects the PSD2 budget.
- Record webhook errors.

**Simpler alternative:** a short initial delay only. Beware this can spend the 4/day budget on restart loops.

**Rollback:** revert.

### GO-13 — The AI fallback chain treats a client cancel as provider failure; no rung cooldown; no turn deadline — P2

**Classification:** CONFIRMED DEFECT for the cancel/alert behaviour; CODE-SUPPORTED RISK for latency and cost. Source lanes: INT-05, the chain part of REQ-04.

**Evidence:**
- `platform/ai/chain.go:83-150` demotes on every error without checking `ctx.Err()`.
- On exhaustion it alerts Discord synchronously (`:151-160`, up to 10s).
- A 402 "credit balance" (the $20 trial cap) is classified but not remembered.

**Impact:**
- A user closing the app pages Discord with a false "every provider failed", and uses up the 15-minute throttle slot.
- When the trial cap is hit, every call pays a failed Anthropic round trip first.
- A slow rung waits 60s before the fallback.

**Proposal:**
- Return `ctx.Err()` before and after each rung, with no alert.
- Add an in-process cooldown for 402/401/403 (copy `insights.CooldownRegistry`).
- Give each turn a deadline below the 360s Telegram cap.
- Send the alert asynchronously.

**Do not** add temperature/top_p/top_k or prefill (Haiku 5.5 returns 400), and do not retry inside a rung.

**Tests:** stub servers only.

**Rollback:** an env flag on the cooldown.

### GO-14 — An all-or-nothing `QuoteBatch` blanks live prices in portfolio valuation — P2

**Classification:** CONFIRMED DEFECT. Source lanes: INT-07, REQ-06.

**Evidence:**
- `marketdata/service.go:283-316`: the first error in an errgroup cancels the other fetches and fails the chunk.
- An empty quote with no cached row returns `QuoteUnavailableError`, which is not cached (`:203-206`).
- `portfolio/valuation.go:89-105` then `continue`s, so up to 100 holdings show cost basis. Sentiment has the same pattern (`insights/sentiment_service.go:134`).

**Impact:** one delisted ticker permanently degrades the user's portfolio views. Sibling fetches are cancelled before they reach the cache, so every view re-calls the provider.

**Proposal:**
- Add a per-symbol `QuoteMany` (plain errgroup with `SetLimit(10)`, partial results) for valuation and sentiment.
- Keep `QuoteBatch` for the `/market/quotes` wire contract.
- Add a short negative cache (5–10 minutes) keyed like the positive one.

**Rollback:** switch callers back.

### GO-15 to GO-18 — Database: missing or unusable indexes on growing or hot queries — P2, MEASURE FIRST

Sources: DB-02 to DB-05. Every index follows the repo's pattern from `00003_expenses_external_id.sql`: `-- +goose NO TRANSACTION`, then `DROP INDEX CONCURRENTLY IF EXISTS`, then `CREATE INDEX CONCURRENTLY`. Rollback is `DROP INDEX CONCURRENTLY`. Validate with `EXPLAIN (ANALYZE, BUFFERS)` of SELECTs on a staging copy, never production, plus `pg_stat_user_tables` `seq_scan` and `n_live_tup`.

- **GO-15. Goal planning.** `financial_goal_contributions`, `financial_goal_progress_snapshots` and `financial_goal_suggestions` have primary keys only (`baseline.sql:824,852,864`).
  - Queries at `goalplanning.sql:141-195` filter on `goal_id`.
  - The daily job (`goalplanning/job.go:98-150`) and the list handler (`feature.go:167`) run about 3 scans per goal.
  - Snapshots grow by goals × days (400 days of dailies, plus month-ends kept forever), so cost grows roughly with the square of active goals.
  - Proposal: `snapshots(goal_id, calculated_at)`. Optionally also `contributions(goal_id, occurred_at)` and `suggestions(goal_id, status, created_at)`.
- **GO-16. Lot ledger.**
  - `lot_disposals` has only `UNIQUE(lot_id, transaction_id)`, and `lots` has no index on `open_transaction_id`.
  - `RealizedPnlBySymbol` (`portfolio.sql:149-167`, used on `/v1/portfolio/summary` and `/v1/pnl`) anti-joins on `d.transaction_id`.
  - IBKR sync makes two lookups per imported transaction (`ibkrsync.go:404-408,707`).
  - Six unindexed FK columns point at `transactions`.
  - Proposal: `lot_disposals(transaction_id)`, `lots(open_transaction_id)`.
- **GO-17. Budget plan items.**
  - The only index is `(user_id, snapshot_id)` (`baseline.sql:4888`), but four queries filter on `snapshot_id` alone.
  - Proposal: add `AND user_id = $n`. Each caller has already loaded the snapshot scoped to the user, so this is also defense in depth.
  - Run this only after `SELECT count(*) FROM budget_plan_items i JOIN budget_snapshots s ON s.id=i.snapshot_id WHERE i.user_id <> s.user_id` returns 0. Otherwise add a `(snapshot_id)` index.
- **GO-18. Earnings sweep.**
  - The sweep makes two full-scan owner queries per calendar symbol (`items.sql:50-53`; every index starts with `user_id`).
  - Proposal: batch with the existing `= ANY(...)` owner queries in `news.sql:39-46`, keeping `status='active'`, before considering `stocks(symbol)` and `watchlist_items(symbol)` indexes.

Write amplification for each is one index entry per insert, on low-write tables.

### GO-19 — Pre-auth per-IP limiters merge every in-cluster MCP user into one bucket — P2

**Classification:** CODE-SUPPORTED RISK (parity). Source lanes: REQ-09.

**Evidence:**
- Limiters for market, macro, insights, AI, receipts, brokers, banks and others are mounted before auth (for example `market/feature.go:90`, `assistant/feature.go:131`).
- The default trusted CIDRs include 10/8.
- MCP calls `http://api-v2:8080` and sends no XFF or edge headers, according to a lane grep of norviq-mcp that I did not re-check.

**Impact:** one heavy MCP user returns 429 to every MCP user.

**Proposal:** keep a generous per-IP pre-auth limit, and add the per-user limiter after `Scoped()`. Alternatively, have MCP forward the client IP with the edge key; that secret must be sealed for MCP's namespace.

**Cross-audit note:** the trust in forwarded IP headers is a security question, and the security audit owns it.

### GO-20, GO-21, GO-22 — Small check-then-act races — P3, APPROVE

- **GO-20.** `cancelRun` checks the state in Go, then runs an unconditional `CancelScenarioRun` (`runs.go:583-598`, `runs.sql:81-83`; re-checked). A run that completes between the two becomes `cancelled`, and dedupe by hash stops finding it.
  - Fix: `:execrows … AND state IN ('queued','running')`. On 0 rows, return the existing 409 "Run is already terminal". The wire format is unchanged.
- **GO-21.** Group join counts, inserts, then recounts with no lock on the group row (`groups/service.go:201-234`). The 250 cap and `member_count` can drift.
  - Fix: `SELECT … FROM groups WHERE id=$1 FOR UPDATE` first, in both add and remove.
- **GO-22.** `expense_categories` has no `user_id` index, and default seeding on first read runs outside a transaction (`expenses/service.go:1312-1331`), so concurrent first reads seed duplicates.
  - Fix: seed under the existing `AdvisoryXactLock`.
  - Index: MEASURE FIRST.
  - Do not add `UNIQUE(user_id, name)`: existing data may violate it, and duplicate names may be legitimate.

### GO-23 — Idempotent replay duplicates headers and can mislabel gzip; no in-flight reservation — P2 (headers) / P3 (race)

**Classification:** CONFIRMED DEFECT (header replay); CODE-SUPPORTED RISK (race). Source lanes: REQ-08.

**Evidence:**
- `middleware/ratelimit.go:85-138` records the headers that the global gzip, CORS and security middleware already set (`server.go:107`), and replays them with `Add`.
- There is no reservation between GET and SET.
- The SET uses the request context.

**Impact:**
- Duplicated `X-Request-ID`, `Vary` and security headers.
- A client that did not send `Accept-Encoding: gzip` can receive a plain body labelled gzip.
- Concurrent duplicates both execute.

**Proposal:**
- Store an allowlist of headers, replay them with `Set`, and store with `WithoutCancel` plus a 2s timeout.
- The NX in-flight reservation with 409 is a new client contract. Defer it until the client's retry behaviour is known.

**Tests:** a full-stack replay test with and without gzip.

### GO-24 — Job lock: a saturated pool reads as "database down"; a session lost mid-tick is unnoticed — P3

**Classification:** CODE-SUPPORTED RISK. Source lanes: JOB-05, DB-11.

**Evidence:**
- After a session error, `joblock.go:150-166` fails open if `pool.Ping` fails. Ping acquires a pool connection, so with all 20 checked out it times out after 2s.
- During a maxSurge rollout, a session redial can fail with 53300 while both pods' pools are busy, and then both run the tick unlocked.
- Separately, if the session connection dies mid-body, Postgres releases the lock while the body keeps running.

**Proposal:**
- Classify the session error: a `*pgconn.PgError` or a timeout on an established connection means the database answered, so skip the tick. Fail open only on dial-level errors.
- Add per-tick deadlines (GO-04).
- Optionally add a session heartbeat that cancels the bodies when the connection is lost.

**Tests:** a dial that returns PgError 53300 while the pool Ping blocks; the body does not run.

### GO-25 — One bad row ends the whole tick — P3

**Classification:** CODE-SUPPORTED RISK (parity). Source lanes: JOB-08.

**Locations:** `rebalancing/alertjob.go:119-128` ("first error ends the run, as in Swift"), `digest/runner.go:89-99`, `reporting/worker.go:167-188`, `tax/jobs.go:107-117`.

**Proposal:** log, count and continue per item, as `news.NotifyOnce` and goalplanning already do (fixed in `docs/vapor-bugs`). Keep the early return for context errors.

### GO-26 — `why-moved` blocks on the LLM chain and does not cache failures — P3

**Classification:** CODE-SUPPORTED RISK. Source lanes: REQ-04.

**Evidence:**
- `dashboard/whymoved.go:324-370` calls `client.Chat` on the request context and caches only successes.
- The route has no rate limit.

**Proposal:** an own budget of about 8s, plus a negative cache of about 5 minutes under the same key. `aiSummary` may already be absent.

### GO-27 — Account deletion runs one long cascade transaction for heavy users — P3, MEASURE FIRST

**Classification:** HYPOTHESIS. Source lanes: DB-06.

**Evidence:**
- One `pgx.BeginFunc` ends with `DELETE FROM users` (`accountdeletion/feature.go:151-165`).
- A lane scan found 116 FK columns with no leading index, about 41 of them on `users`.

**Proposal:**
- Do not index all 116.
- Rank the tables by `n_live_tup` and `seq_tup_read` on staging, then index the large child tables first (the GO-16 transaction children, `ai_messages.user_id`, the board/group read tables).
- Keep the single transaction: the code relies on all-or-nothing before bank revocation.

**Validate:** `BEGIN; EXPLAIN (ANALYZE, BUFFERS) DELETE … ; ROLLBACK;` on staging only.

### GO-28 — Observability is too coarse to measure the rest — P3

**Classification:** CODE-SUPPORTED RISK. Source lanes: REQ-12.

**Evidence:**
- One global latency histogram with no route or status label.
- SSE streams (600s) land in `+Inf`.
- `context.Canceled` is mapped to 500 and logged twice (`httpx/errors.go:129-163`).

**Proposal:**
- Add a new route-labelled series (`c.Path()` is bounded) instead of relabelling the existing one, whose names are fixed for dashboards and alerts.
- Exclude the SSE route.
- Map client cancel to 499 or log it at debug.

This enables the measurement plans in GO-01, GO-02, GO-04 and GO-26.

### GO-29 — Finnhub 429 is not remembered — P3

**Classification:** CONFIRMED DEFECT. Source lanes: INT-08.

**Evidence:** `marketdata/finnhub.go:266-272` logs "backing off" but stores no state. FMP already shares a Redis back-off key (`fmp.go:90-92,132-141`).

**Proposal:** mirror the FMP key.

### GO-30 — MEASURE FIRST bundle (no change until a counter or profile justifies it)

- **Quote singleflight and the profile2 call per quote** (INT-09, REQ-07): no singleflight on a 20s-TTL miss, and each Finnhub quote also fetches `/stock/profile2`. First add a counter of provider calls per endpoint. If it shows a need, add a singleflight that runs on `WithoutCancel` plus a timeout, so one caller's cancel cannot fail the others, and cache the currency for about 24h.
- **APNs sends one device at a time** with a 30s client timeout (JOB-06). Measure Send duration by kind first. Then consider a small semaphore of 8–16.
- **Per-tick growth** in rebalancing alerts, target alerts and the thesis feed (JOB-09). Use `job_duration_seconds{job}` (now exported) and pool `EmptyAcquireCount`. Moving the Pro check before `ThesisFeed` is a cheap win to approve once measured.
- **Stale-quote fallback feeding alerts** (INT-10): add an age bound for alert jobs only.
- **Download handlers read the whole file before the ETag check** and gzip PDFs (REQ-11): `http.ServeContent` plus a gzip skipper.
- **Scenario retention** count-then-delete race and missing `scenario_runs(scenario_id)` index (DB-07).
- **Board, social and achievement feeds** scan `social_settings` for suspended users (DB-12). Consider a partial index.
- **Email and push are at-most-once** with no bounded retry (INT-11), and Telegram turns are dropped on deploy (JOB-10). Document these as product choices. A single bounded retry with idempotency keys is optional.

## What is already sound

- **Job locking:** ticks never overlap within a process (sleep after each tick, CAS guards). The job-lock session mutex covers only the lock statements, the `held` map blocks re-entry, and a dead session is redialed.
- **Queue claims:** reporting, scenarios and assistant watches claim with a single `FOR UPDATE SKIP LOCKED` statement on usable indexes. Scenario writes are fenced on owner and state.
- **Transactions:** every transaction has a deferred rollback or uses `BeginFunc`. No outbound I/O was found inside a transaction.
- **Billing and banking imports:** the billing webhook is one local transaction that answers 200 on a duplicate. Plaid import is idempotent on a unique key and persists the cursor only after a full sync.
- **Rate limiting and caches:** the HTTP rate limiter is an atomic Lua script. FMP has a shared back-off and 32 MB read caps. Market-data and chat adapter calls have per-call deadlines. Singleflight is used for basic financials and FMP EOD.
- **Telegram transport:** constant-time secret check, 1 MB cap, retries only on Telegram 429, and an HTML-to-plain fallback.
- **Transcription:** uses the in-cluster OpenAI-compatible service. There is no paid speech API.
- **Realtime bus:** bounded buffers, at most 5 streams per user, no lock held across I/O.
- **Migrations:** run as a PreSync job, not at boot, with `lock_timeout`, `NO TRANSACTION` and `CONCURRENTLY`.
- **Per-request auth lookups:** use unique or primary-key indexes. PAT touches are throttled.

## Missing data and evidence gaps

- No production `pg_stat_statements`, `pg_stat_user_tables` or `EXPLAIN` output; Postgres `max_connections` and role settings. Every index item is therefore MEASURE FIRST.
- No traffic profile: request mix, the share of MCP users, device counts, active goals, scenario run sizes in practice.
- No heap or CPU profile of the scenario engine or the upload paths. GO-01 and GO-02 rest on arithmetic.
- The norviq-mcp source was not reviewed. The claim behind GO-19 that it sends no client IP comes from a lane grep.
- Redis client behaviour under partition (library defaults).

## Not inspected

- Most handlers individually (lanes sampled dashboard, portfolio, statistics, banking lists, screens, receipts, exports, reports).
- The internals of: automation, identity cleanup, expense-import retention, crypto and macro refresh, Hermes sync, pilots ingestion, the tax projection job, the assistant watch runner, the boards daily publisher, GoCardless, bankrevoke, social OAuth clients, the Gotenberg path, the BYOK verifier, and the MCP server.
- Security boundaries, which are covered by the separate security audit.

## Evidence log

- **Commands run before the audit on this snapshot**, against a disposable `postgres:16-alpine` on loopback:
  - `go vet ./...`: clean.
  - `golangci-lint run ./...` (v2.15.0): `0 issues.`
  - `TEST_DATABASE_URL=… go test -race -count=1 ./...`: exit 0, 83 packages ok.
- **Parent re-checks of lane claims** (source reads):
  - `scenarios/runs.go:372-382` and `engine.go:327-334` (GO-01)
  - `assistant/messaging.go:150-176` and `messaging.sql:7-13` (GO-06)
  - `scenarios/runs.go:580-600` (GO-20)
  - `server/health_core.go:20-55` (GO-03)
  - The infra `origin/main` values listed in the summary (replica, limits, flags, probes)
- **Not run:**
  - Benchmarks, pprof, load tests and EXPLAIN: no isolated staging environment was authorized.
  - Staticcheck: not installed, and downloading it was declined.
  - `-race` beyond the existing suite.
- **Contradictions resolved:** the database lane called the deployment values unverified because its local infra checkout was stale. The jobs and request lanes read them from `origin/main`, and I confirmed them there (`3cffa03`).

## Prioritized roadmap

| Order | Item | Status | Depends on | Effort | Risk | Success check |
|---|---|---|---|---|---|---|
| P1-a | GO-03 readiness: Redis→degraded, bounded checks, probe timeout | APPROVE | — | S | low | Redis stub test; no NotReady on a Redis blip |
| P1-b | GO-01 simpler form: max-concurrent 1 + GOMEMLIMIT + stale-start fail | APPROVE | owner OK on env | S | low | no restarts from max runs on staging |
| P1-c | GO-01 work budget and attempt cap | REVISE (owner decides reject vs clamp) | P1-b | M | wire change | memprofile under budget |
| P1-d | GO-02 GOMEMLIMIT, large-body semaphore, gates before read | APPROVE (gate reorder: owner OK, F5) | — | S | low | heap under 3 concurrent scans |
| P1-e | GO-04 DB runtime params + per-tick deadlines (migrate exempt) | APPROVE | values per job | M | medium (timeouts surface as errors) | 57014 rate, acquire wait |
| P1-f | GO-06 conditional Telegram claim + claim before transcription | APPROVE; per-chat serialization REVISE | — | S / M | low | concurrent webhook test |
| P2-a | GO-07 report reclaim + WithoutCancel | APPROVE | — | S | low (no delivery reclaim) | cancel-mid-render test |
| P2-b | GO-08 single shutdown deadline, bounded drains, grace 45s, preStop | APPROVE | infra PR | S | low | shutdown test; no SIGKILL events |
| P2-c | GO-09 recover at tick and goroutine boundaries, failing claimed rows | APPROVE | GO-07 for reporting | S | low | panic test |
| P2-d | GO-05 client timeouts + LimitReader | APPROVE | — | S | none | blocking-stub tests |
| P2-e | GO-11 shared atomic INCR+TTL | APPROVE | — | S | none | fake Redis test |
| P2-f | GO-12 restart-proof bank backstop + record webhook errors | APPROVE | — | S | PSD2 budget | injected-clock test |
| P2-g | GO-13 ctx short-circuit, 402 cooldown, turn deadline, async alert | APPROVE | — | M | low | stub tests |
| P2-h | GO-14 partial quote results for valuation + negative cache | APPROVE | — | S | none | stub test |
| P2-i | GO-23 header allowlist + WithoutCancel | APPROVE; NX/409 MEASURE FIRST | client behaviour | S | low | replay test |
| P2-j | GO-19 per-user limiter after auth | REVISE (coordinate with security audit on IP trust) | security audit | M | medium | 429 rate on MCP |
| P2-k | GO-10 ordering guard | MEASURE FIRST | inversion SQL | M | migration | inversion count = 0 |
| P2-l | GO-15/16/18 indexes, GO-17 predicate | MEASURE FIRST | staging EXPLAIN | S each | write amp (low) | plan + seq_scan drop |
| P3 | GO-20, 21, 22 (race fixes) | APPROVE | — | S | none | race tests |
| P3 | GO-24, 25, 26, 28, 29 | APPROVE | — | S | low | per item |
| P3 | GO-27, GO-30 bundle | MEASURE FIRST | metrics (GO-28) | — | — | counters / profiles |

## Proposals deliberately not made (REJECT)

- **A global `WriteTimeout`, `ReadTimeout`, Echo Timeout or `TimeoutHandler`:** they break SSE and slow uploads. Use per-route context deadlines instead.
- **Raising `DATABASE_MAX_CONNECTIONS`:** the Postgres budget is shared across pods, staging and other apps. Fix the deadlines and scans first.
- **A second replica for resilience:** realtime is in-process, which pins the API to one replica.
- **A generic retrying `http.RoundTripper`:** it would retry non-idempotent POSTs (APNs, Resend, Telegram, LLM), causing duplicate sends and double billing.
- **Retries inside an LLM rung, or longer AI timeouts:** they add cost and latency on top of the fallback chain and hit the $20 trial hardest.
- **Adding temperature, top_p, top_k or prefill on Haiku:** Haiku 5.5 returns 400 for them.
- **Returning 5xx from the Plaid webhook on a sync failure:** the redelivery semantics are unknown, and a large item could loop.
- **Broker or DLQ infrastructure for push, email or Telegram:** at-most-once is the accepted contract.
- **A third-party circuit-breaker library, or a Redis-distributed singleflight:** with one replica, the in-repo patterns are enough.
- **Indexing all 116 unindexed FKs, converting OFFSET pagination to keyset (a wire contract), or SERIALIZABLE isolation:** cost exceeds evidence. Targeted locks are cheaper.
- **`UNIQUE(user_id, name)` on `expense_categories`:** existing data may violate it.
- **An in-process cache of `SessionsValidAfter` or suspended IDs:** it delays revocation and moderation.
- **`sync.Pool` JSON buffers or a JSON library swap:** apijson is byte-exact to the wire contract, and there is no profile.
- **Approximate quantile sketches for scenarios without an owner decision.**
- **Moving jobs to a separate Deployment now:** storage is on RWO PVCs and realtime is single-replica. That is a redesign.
- **A hosted transcription API:** forbidden by policy.
