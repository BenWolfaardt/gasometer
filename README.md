# gasometer

gasometer measures how much EVM fee estimators overpay to get a transaction included, and how often their bids would actually have landed. It scores its own predictor and the two standard baselines (the node's suggestion and the wallet default) continuously on Base, OP Mainnet and Avalanche C-Chain, and publishes the results to a public dashboard.

## What is measured

Fee estimates from nodes and wallets are tuned for safety, not cost. On these networks blocks are almost never full: in a sample taken before building this, blocks over 50% full were 0.1% on Base, 4.4% on OP and 0% on C-Chain. The tip therefore barely affects inclusion, yet the median tip paid was 4.9x the low end of included tips on Base and over 5,000x on OP. Nobody publishes how much a given estimator overpays, or how often its bid would actually have landed.

Every 60 seconds, for each network, gasometer stores the fee data of every new block and a snapshot of the next block: its base fee, the node's suggested tip, the native coin's USD price and, on OP Stack networks, the L1 data fee of each reference transaction. Each predictor then bids at three horizons, 2, 30 and 120 seconds, converted to blocks at the network's measured block time.

| Predictor | Tip | Max fee per gas |
| --- | --- | --- |
| `node` | `eth_maxPriorityFeePerGas` | next base fee + tip |
| `wallet` | `eth_maxPriorityFeePerGas` | 2 × next base fee + tip, the ethers and MetaMask default |
| `market` | median tip by gas of the latest block, what senders are paying | 2 × next base fee + tip |
| `gasometer` | 0.99 quantile of the cheapest required tip in each horizon-long window ending at the last 1,800 blocks | projected upper base fee at the horizon + tip |

A bid lands if it beats the required tip of any one block in its horizon, so gasometer's tip targets the cheapest block of a window rather than a typical block. Backtested on ten hours of all three networks, that rule lands at least 98.7% of bids at every horizon.

The gasometer predictor projects the base fee from the growth observed over the same span in the last hour, at its 0.99 quantile. On OP Stack networks that projection is capped by the protocol's worst case: each full block can raise the base fee by at most (elasticity − 1) ÷ denominator, which is 4% per block on Base and 0.4% on OP. On C-Chain it is capped by the gas clock's worst case: the base fee after every block up to the horizon is full, at the worst gas config the protocol allows.

Once the horizon has passed, each bid made at block n with a horizon of k blocks is scored against the blocks n+1 to n+k:

- **Included** if some block has max fee ≥ base fee + required tip and tip ≥ required tip, so the effective tip, min(tip, max fee − base fee), covers what the block required. The first such block is where it lands.
- **Required** is the cheapest base fee + required tip in the window: the least that would have landed.
- **Paid** is base fee + effective tip at the inclusion block.
- **Overpay** is paid − required. EIP-1559 refunds the gap between the max fee and the base fee, so overpayment comes only from the tip.
- **Estimate error** is |(expected base fee + tip) − paid| ÷ paid, how far off the cost a wallet would have shown was.
- **Expired** if the network has not reached block n+k within ten horizons; **unresolvable** if blocks in the window are missing.

A block's **required tip** is a percentile by gas of the tips it included: the 5th on Base and OP, because every OP Stack block opens with a zero-tip system transaction, and the 0th, the cheapest included tip, on C-Chain, whose blocks carry few transactions. Either way it is an upper bound on what inclusion needed, not a measurement of it; probe transactions will measure it directly. Costs are quoted in USD for a native transfer (21,000 gas) and an ERC-20 transfer (65,000 gas), split into base fee, tip and L1 data fee.

**The fee model checks itself in production.** On Base and OP, every poll computes the next base fee from the latest header with gasometer's own EIP-1559 implementation, including the OP Stack's per-network parameters and Jovian's minimum base fee and metered gas, and compares it with the node's. The same model is property-tested, fuzzed, checked against libevm's `CalcBaseFee`, and replayed against the last 500 Base and OP headers.

C-Chain runs Streaming Asynchronous Execution, whose base fee follows a gas clock rather than EIP-1559. gasometer runs avalanchego's own clock (`vms/saevm/gastime`) over the blocks since the head's settled block, as a validator does when it builds a block, and every poll compares the result with the head's base fee. It matches all of the last 10,000 blocks, including those carrying atomic import and export transactions. [How the C-Chain base fee works under SAE](docs/sae-gas-pricing.md) explains the clock, the ceiling and what the RPC returns.

On every network a mismatch increments `gasometer_model_mismatch_total`, which should stay at zero.

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

The inclusion rate is computed in Grafana: included ÷ (included + missed). At full label coverage the service exports about 8,200 series, inside the Grafana Cloud free tier.

## Running it

The stack is gasometer, Postgres and Grafana Alloy, which scrapes gasometer every minute and pushes to Grafana Cloud. It runs under Docker Compose or `podman compose`.

```bash
cp deploy/.env.example deploy/.env    # Postgres password and Grafana Cloud credentials
make compose-up
curl -s localhost:9090/metrics | grep gasometer_model_mismatch_total
```

Import `deploy/grafana/gasometer.json` into Grafana and pick the Prometheus data source when asked. Its one chart splits each estimator's fee for a native transfer landing within 30 seconds into what the cheapest landing bid needed and what was overpaid, as a share of the fee. A strip under it turns red wherever a network stopped reporting.

The JSON is generated. Edit `deploy/grafana/dashboard.py` instead and regenerate with `python3 deploy/grafana/dashboard.py deploy/grafana/gasometer.json`, which needs only the standard library.

To be told when polling stops, install the alert rule with a service account token (Editor role) and the uid of the same data source. It fires after about 8 minutes without a completed poll on any network, or without any gasometer metrics at all.

```bash
GRAFANA_URL=https://<stack>.grafana.net GRAFANA_TOKEN=<token> DATASOURCE_UID=<uid> deploy/grafana/install-alert.sh
```

Block rows are kept for 30 days, so backtests have a month to replay; snapshots and predictions are kept indefinitely, about 10 MB a day.

To run the binary directly against your own Postgres:

```bash
make build
GASOMETER_DATABASE_URL='postgres://user:pass@localhost:5432/gasometer?sslmode=disable' \
  ./bin/gasometer --config-file configs/gasometer.yml
```

Migrations run at startup. Networks, RPC endpoints, Chainlink feeds, predictors, horizons and reference transactions are set in [configs/gasometer.yml](configs/gasometer.yml). Every top-level key also takes a flag (`--poll-interval`) or an environment variable (`GASOMETER_POLL_INTERVAL`); `./bin/gasometer --help` lists them. The service serves `/metrics`, `/healthz` and `/readyz` on port 9090.

## Development

Requires Go 1.26.10 with cgo and a C compiler, which avalanchego needs, and Docker or Podman for the integration tests.

| Target | Runs |
| --- | --- |
| `make test`, `make test-race` | Unit tests |
| `make fuzz` | Every fuzz target, 10 s each by default |
| `make lint` | `go vet` and golangci-lint |
| `make test-db-up`, `make integration` | Postgres store tests and a start-stop run of the binary |
| `make probe` | Builds `bin/probe`, the tip ladder campaign |
| `make live` | Replays the last 500 Base and OP headers and the last 10,000 C-Chain blocks against the fee model, and samples C-Chain's `eth_feeHistory`; needs network |

The code follows Ben Johnson's [Standard Package Layout](https://medium.com/@benbjohnson/standard-package-layout-7cdbc8391fc1):

| Package | Holds |
| --- | --- |
| `gasometer` | The vocabulary: domain types, the ports to the outside world, errors. Standard library only |
| `fee` | The protocol fee model: EIP-1559 with OP Stack parameters, `extraData` decoding, block fee summaries |
| `predict` | The predictors and the base fee projectors they use |
| `score` | Scoring a bid over a block window, and its USD costs |
| `poll` | Each network's collect, forecast and settle cycle, pruning, and the loop that runs it |
| `probe` | The tip ladder: one account per tip, rounds, replacement at the same nonce, funding and sweeping |
| `rpc`, `sae`, `postgres`, `prometheus`, `http` | Adapters, one per dependency; `sae` runs avalanchego's SAE gas clock |
| `mock` | Hand-written mocks of every port |
| `cmd/gasometer` | Config and wiring |
| `cmd/probe` | The probe campaign: `fund`, `run` and `sweep`, with the key seed read from the environment. It writes to the service's database and never migrates it, so deploy the service first |

## Roadmap

- **Probe transactions.** `cmd/probe` sends eight zero-value transfers every 5 minutes at tips from 0 to 1 gwei, one account per tip, and records which were included and how fast, to measure the required tip directly rather than bounding it from other transactions. Built for C-Chain and not yet run; Base and OP follow with a key per network.
- **Bridge quotes.** Compare cross-chain bridge quotes against what the transfers actually cost.

## Licence

Apache-2.0. See [LICENSE](LICENSE).
