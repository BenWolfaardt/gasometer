# gasometer

> [!TIP]
> **[How the C-Chain base fee works under SAE](docs/sae-gas-pricing.md).** gasometer replays avalanchego's gas clock and reproduces 10,000 of 10,000 mainnet base fees exactly. The write-up explains the clock, a provable ceiling on future fees, and why `eth_feeHistory` and `eth_baseFee` disagree.

gasometer measures how much fee estimators overpay to get a transaction included, and how often their bids would actually have landed. It scores them continuously on Base, OP Mainnet and Avalanche C-Chain and publishes the results to a dashboard.

![C-Chain's gas clock and base fee during a burst of full blocks](docs/sae-gas-clock.svg)

*C-Chain's base fee during a burst of full blocks: each one raises it 12.2%, and it only falls once wall time catches the gas clock. From the [write-up](docs/sae-gas-pricing.md#3-the-gas-clock).*

## Why

Fee estimates from nodes and wallets are tuned for safety, not cost, and on these networks blocks are almost never full:

| | Base | OP | C-Chain |
| --- | --- | --- | --- |
| Blocks over 50% full | 0.1% | 4.4% | 0% |
| Median tip paid ÷ cheapest included tip | 4.9x | over 5,000x | not measured |

So the tip barely affects inclusion, yet people pay several times what they need. Nobody publishes how much a given estimator overpays, or how often its bid would have landed.

These figures come from October 2026, after validators raised C-Chain's minimum gas price to price out spam, in a quiet market. When demand rises, as in a bull run, blocks fill up and the tip starts to decide who gets in, so expect the gap to narrow. Measuring it continuously is what shows when that happens.

## How it works

1. **Poll.** Every 60 seconds, for each network, gasometer stores every new block's fees and a snapshot of the next block: base fee, the node's suggested tip, the coin's USD price from the network's Chainlink feed and, on OP Stack, each reference transaction's L1 data fee.
2. **Bid.** Each predictor bids for landing within 2, 30 and 120 seconds.
3. **Score.** Once the horizon has passed, each bid is checked against the blocks that followed.

| Predictor | Tip | Max fee per gas |
| --- | --- | --- |
| `node` | `eth_maxPriorityFeePerGas` | next base fee + tip |
| `wallet` | `eth_maxPriorityFeePerGas` | 2 × next base fee + tip, the [ethers](https://github.com/ethers-io/ethers.js/blob/v6.17.0/src.ts/providers/abstract-provider.ts#L992) and [web3.js](https://github.com/web3/web3.js/blob/bf1691765bd9d4b0f7a4479e915207707d69226d/packages/web3-eth/src/utils/get_transaction_gas_pricing.ts#L69) default |
| `market` | median tip of the latest block: what senders are paying | 2 × next base fee + tip |
| `gasometer` | 0.99 quantile of the cheapest required tip over recent windows as long as the horizon | projected base fee at the horizon + tip |

A bid lands if it beats one block's required tip within its horizon, so gasometer targets the cheapest block in a window rather than a typical one. Backtested on ten hours of all three networks, that lands at least 98.7% of bids at every horizon. Its base fee projection is capped by each protocol's worst case: 4% per block on Base, 0.4% on OP, and on C-Chain [the gas clock's ceiling](docs/sae-gas-pricing.md#5-the-worst-case-bound).

### Scoring

A bid made at block n with a horizon of k blocks is scored against blocks n+1 to n+k:

| Term | Meaning |
| --- | --- |
| Required tip | A percentile of the tips a block included: the 5th on Base and OP, whose blocks open with a zero-tip system transaction, and the 0th on C-Chain. An upper bound on what inclusion needed; [probe transactions](#roadmap) will measure it |
| Included | Some block in the window has max fee ≥ base fee + required tip and tip ≥ required tip |
| Required | The cheapest base fee + required tip in the window |
| Paid | Base fee + effective tip at the inclusion block |
| Overpay | Paid − required. EIP-1559 refunds the rest of the max fee, so overpaying comes only from the tip |
| Estimate error | How far the cost a wallet would have shown was from what was paid |
| Expired, unresolvable | The chain did not reach n+k within ten horizons, or blocks in the window are missing |

Costs are in USD for a native transfer (21,000 gas) and an ERC-20 transfer (65,000 gas), split into base fee, tip and L1 data fee.

### The fee models check themselves

- **Base and OP:** every poll computes the next base fee with gasometer's own EIP-1559 implementation, OP Stack parameters and Jovian rules included, and compares it with the node's. The model is also property-tested, fuzzed, checked against libevm and replayed against 500 recent headers.
- **C-Chain:** every poll runs avalanchego's own SAE gas clock over the blocks since the head's settled block and compares the result with the head's base fee. The [write-up](docs/sae-gas-pricing.md) covers how.

A mismatch increments `gasometer_model_mismatch_total`, which should stay at zero.

## Metrics

All metrics carry the `gasometer_` prefix.

| Metric | Type | Labels |
| --- | --- | --- |
| `predictions_resolved_total` | counter | network, predictor, horizon, outcome (`included`, `missed`, `expired`, `unresolvable`) |
| `overpay_usd` | histogram | network, predictor, horizon, tx |
| `overpay_ratio` | histogram, overpay ÷ required | network, predictor, horizon |
| `estimate_error_ratio` | histogram | network, predictor, horizon |
| `paid_usd` | histogram | network, predictor, horizon, tx, component (`base_fee`, `tip`, `l1_fee`) |
| `required_usd` | histogram | network, predictor, horizon, tx, component |
| `base_fee_wei`, `next_base_fee_wei`, `node_tip_wei`, `required_tip_wei` | gauge | network |
| `l1_fee_wei` | gauge | network, tx |
| `native_usd` | gauge | network |
| `model_mismatch_total` | counter | network |
| `rpc_requests_total` | counter | network, method, endpoint, outcome |
| `rpc_duration_seconds` | histogram | network, method |
| `poll_duration_seconds` | histogram | network |
| `last_poll_timestamp_seconds` | gauge | network |

The dashboard's main chart splits each estimator's fee for a native transfer landing within 30 seconds into what was needed and what was overpaid. Inclusion rate is computed in Grafana as included ÷ (included + missed). The service exports about 8,200 series, inside the Grafana Cloud free tier.

## Running it

```bash
cp deploy/.env.example deploy/.env    # Postgres password and Grafana Cloud credentials
make compose-up
```

This starts gasometer, Postgres and Grafana Alloy under Docker Compose or `podman compose`. Alloy pushes the metrics to Grafana Cloud.

| What | How |
| --- | --- |
| Dashboard | Import `deploy/grafana/gasometer.json` |
| Alert | Fires after about 8 minutes without a poll. Install with `deploy/grafana/install-alert.sh` |
| Config | [configs/gasometer.yml](configs/gasometer.yml). Any key also takes a flag (`--poll-interval`) or env var (`GASOMETER_POLL_INTERVAL`) |
| Endpoints | `/metrics`, `/healthz` and `/readyz` on port 9090 |
| Storage | Blocks for 30 days, snapshots and predictions kept, about 10 MB a day |

The alert script reads `GRAFANA_URL`, `GRAFANA_TOKEN` (a service account with the Admin role, which alert-rule provisioning requires) and `DATASOURCE_UID`.

Without Compose, against your own Postgres (migrations run at startup):

```bash
make build
GASOMETER_DATABASE_URL='postgres://user:pass@localhost:5432/gasometer?sslmode=disable' \
  ./bin/gasometer --config-file configs/gasometer.yml
```

## Development

Requires Go 1.26.10 with cgo and a C compiler, which avalanchego needs, and Docker or Podman for the integration tests.

| Target | Runs |
| --- | --- |
| `make test`, `make test-race` | Unit tests |
| `make fuzz` | Every fuzz target, 10 s each by default |
| `make lint` | `go vet` and golangci-lint |
| `make test-db-up`, `make integration` | Postgres store tests and a start-stop run of the binary |
| `make probe` | Builds `bin/probe`, the tip ladder campaign |
| `make live` | Replays recent Base, OP and C-Chain blocks against the fee models and samples C-Chain's `eth_feeHistory`; needs network |
| `python3 deploy/grafana/dashboard.py deploy/grafana/gasometer.json` | Regenerates the dashboard JSON (standard library only) |

The code follows Ben Johnson's [Standard Package Layout](https://medium.com/@benbjohnson/standard-package-layout-7cdbc8391fc1):

| Package | Holds |
| --- | --- |
| `gasometer` | Domain types, ports and errors. Standard library only |
| `fee` | EIP-1559 with OP Stack parameters, `extraData` decoding, block fee summaries |
| `predict` | The predictors and their base fee projections |
| `score` | Scoring a bid over a block window, and its USD costs |
| `poll` | Each network's collect, forecast and settle cycle |
| `probe` | The tip ladder campaign |
| `rpc`, `sae`, `postgres`, `prometheus`, `http` | One adapter per dependency; `sae` runs avalanchego's gas clock |
| `mock` | Hand-written mocks of every port |
| `cmd/gasometer`, `cmd/probe` | Config and wiring for the service and the campaign |

## Roadmap

- **Probe transactions.** Send real transfers at a ladder of tips and measure the required tip instead of bounding it. Built for C-Chain, not yet run; Base and OP next.
- **Bridge quotes.** Compare cross-chain bridge quotes with what the transfers actually cost.

## Licence

Apache-2.0. See [LICENSE](LICENSE).
