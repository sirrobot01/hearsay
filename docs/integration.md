# Host integration

Treat Hearsay as an optional hint source around work the host already performs. A daemon outage, unknown answer, or rejected policy must preserve the host's normal behavior.

## Recommended rollout

1. Configure only the namespaces the host can verify through real outcomes.
2. Query candidates in one batch before selection.
3. Run in shadow mode and keep normal selection unchanged.
4. Report the selected candidate's real outcome and discard unselected decisions.
5. Review coverage and verified accuracy in `hearsay status`.
6. Enable active behavior only after setting explicit support, evidence, source, and age thresholds.

Never schedule work merely to create observations. Searches, lookups, and in-progress samples are not ground truth.

## Go daemon client

```go
api := client.New("http://127.0.0.1:8477", token)

protocol, namespaces, err := api.Namespaces(ctx)
if err != nil || protocol != hearsay.ProtocolVersion {
    // Continue without hints.
}

evidence, err := api.Evidence(
    ctx,
    "debrid.realdebrid.cached",
    "debrid.realdebrid.uncached",
    infohashes,
)
if err == nil {
    // Preserve normal order in shadow mode. In active mode, apply the
    // same explicit thresholds used by debrid.DefaultAdvicePolicy().
}

// After the chosen add completes, close the feedback loop.
err = api.Report(ctx, "debrid.realdebrid.cached", chosenInfohash, wasInstant)
```

Import the client as `github.com/sirrobot01/hearsay/client`. Requests carry caller cancellation and return typed `client.HTTPError` values for non-success responses.

For an in-process integration, use `hearsay.Hearsay` directly and `debrid.Advisor` for the measured shadow-to-active workflow.

## Public-torrent availability

Enable `adapters.torrent` to activate `torrent.public.availability`. The subject is a v1 or v2 infohash. The scalar value is the fraction completed by a terminal attempt: `1` for completion, `0` for a final failure, or a fraction for a terminal partial result.

This domain fits the model because swarm viability is costly to test broadly, torrent clients learn it during normal downloads, acting on a stale hint is cheap, and the attempt reveals a new outcome against a stable infohash.

Report only after the client reaches a meaningful terminal state. Do not report active downloads, user cancellations, queue removals, or probe jobs.

```go
answers, err := api.Claims(ctx, swarm.Namespace, infohashes)
if err == nil {
    value, known := answers[0].ScalarValue()
    _ = value
    _ = known
}

err = api.Report(ctx, swarm.Namespace, chosenInfohash, terminalCompletionRatio)
```

## HTTP capability discovery

`GET /v1/namespaces` returns the protocol version and the configured namespace, value type, freshness horizon, and Boolean time bucket. Integrations should check this endpoint at startup instead of assuming every daemon enabled every adapter.

All API calls require the bearer token. Batch endpoints are the primary query path. Unknown evidence is a normal result, not false.
