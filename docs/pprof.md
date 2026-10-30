# Profiling (pprof and flame graphs)

`PPROF_ADDR` turns on Go's `net/http/pprof` on **its own listener**, never the
API port. Unset (the default) means off.

Bind it to loopback: `PPROF_ADDR=127.0.0.1:6060`. Then nothing inside or
outside the cluster can reach it (no Service, no ingress, NetworkPolicy
irrelevant), and `kubectl port-forward` still can, because it connects
inside the pod's network namespace.

In infra, add to the api-v2 values (`extraEnv`):

```yaml
  - name: PPROF_ADDR
    value: "127.0.0.1:6060"
```

## Taking a profile

```bash
export KUBECONFIG=~/.kube/maat.yaml
kubectl -n norviq port-forward deploy/api-v2 6060:6060    # or -n norviq-staging

# CPU, 30 s, opened as an interactive UI; View → Flame Graph
go tool pprof -http=:7070 'http://localhost:6060/debug/pprof/profile?seconds=30'

# Heap in use / allocations since start
go tool pprof -http=:7070 http://localhost:6060/debug/pprof/heap
go tool pprof -http=:7070 -sample_index=alloc_space http://localhost:6060/debug/pprof/heap

# Goroutines (leaks, stuck SSE streams, job loops)
curl -s 'http://localhost:6060/debug/pprof/goroutine?debug=2' | less

# Execution trace, 5 s
curl -so trace.out 'http://localhost:6060/debug/pprof/trace?seconds=5' && go tool trace trace.out
```

The image is built with `-ldflags="-s -w"`, but profiles are symbolized by
the running process (function names and lines come from the Go pclntab,
which `-s -w` keeps), so no local binary is needed. The source view wants a
checkout of the deployed commit.

A CPU profile costs a few percent of CPU while it runs; heap and goroutine
snapshots are cheap. Block and mutex profiles are off (their sampling rates
are 0).
