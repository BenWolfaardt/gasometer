# How the C-Chain base fee works under SAE

Avalanche C-Chain runs Streaming Asynchronous Execution (SAE, [ACP-194](https://github.com/avalanche-foundation/ACPs/tree/main/ACPs/194-continuous-execution)). Its base fee no longer follows EIP-1559: it comes from a gas clock that every node replays from the last settled block. This document describes that clock, the worst-case bound gasometer builds on it, and what the RPC returns, and it checks each claim against mainnet.

Code links and snippets are from [avalanchego v1.15.2](https://github.com/ava-labs/avalanchego/tree/v1.15.2/vms/saevm), under `vms/saevm/` unless the path says otherwise.

## Notation

| Symbol | Meaning | Source |
| --- | --- | --- |
| $x$ | gas excess, the clock's state | [`Time.excess`][gastime-price] |
| $T$ | target gas per second | [`TargetExponent.Target`][target] |
| $R = 2T$ | gas per second the clock runs at | [`TargetToRate`][target-to-rate] |
| $K = 87\,T$ | excess scaling factor | [`GasConfigAfter`][gas-config-after] |
| $p_{\min}$ | minimum gas price | [`PriceExponent.Price`][price] |
| $\tau = 5\ \text{s}$, $\lambda = 2$ | settlement delay, gas-limit to gas-used ratio | [`TauSeconds`, `Lambda`][params] |

## 1. Why SAE needs a different fee model

Under SAE a block is accepted before it is executed. Consensus orders transactions, and execution follows in a queue behind it, so when a block is built nobody knows how much gas its transactions will use, or how much the blocks ahead of it in the queue used. EIP-1559 sets the next base fee from the parent's gas used, which SAE does not have in time.

SAE therefore prices on what is known at build time. A block's base fee is fixed when it is built, from a clock that charges every queued block its worst case, and ACP-194 requires the header's fee to be that worst-case bound at the moment execution starts. The block builder sets it straight from the worst-case state ([`sae/block_builder.go`][block-builder-basefee]):

```go
hdr.GasLimit = state.GasLimit()
hdr.BaseFee = state.BaseFee().ToBig()
```

## 2. The gas clock

The clock ([`gastime.Time`][gastime-price]) tracks the excess $x$, and its price is

$$
p = \max\left(p_{\min},\ e^{x/K}\right), \qquad K = 87\,T
$$

```go
func (tm *Time) Price() gas.Price {
	p := calculatePrice(tm.excess, tm.excessScalingFactor())
	// When minPrice can't be represented by e^(x/k), p may be too low.
	return max(tm.config.MinPrice, p)
}
```

The exponential is [`gas.CalculatePrice`][calculate-price], the integer Taylor-series approximation from EIP-4844 (`fake_exponential`). The 87 is C-Chain's choice, set per block in [`cchain/hooks.go`][gas-config-after]:

```go
return te.Target(), gastime.GasPriceConfig{
	TargetToExcessScaling: 87, // 87 ~= 60 / ln(2)
	MinPrice:              priceExponent(header).Price(),
}
```

Two operations move the clock.

**[`Tick`][tick] charges $g$ gas.** Gas time advances by $g/R$, and the excess rises by the share of that gas above the target:

$$
\Delta x = g \cdot \frac{R - T}{R} = \frac{g}{2}
$$

```go
R, T := tm.Rate(), tm.Target()         //nolint:revive // unexported-naming: mathematical convention
quo, _, _ := intmath.MulDiv(g, R-T, R) // overflow is impossible as (R-T)/R < 1
tm.excess = intmath.BoundedAdd(tm.excess, quo, math.MaxUint64)
```

**[`FastForwardTo`][fast-forward] moves gas time up to wall time** if it is behind, and every second skipped drains the target:

$$
\Delta x = -T \cdot \Delta t
$$

```go
// -sT
if s := gas.Gas(sec); tm.excess/T >= s { // sT <= x; division is safe because T > 0
	tm.excess -= s * T
} else { // sT > x
	tm.excess = 0
}
```

At full throughput the clock is fed $R$ gas per second, so the excess grows by $(R - T) = T$ per second, and the price doubles when the exponent grows by $\ln 2$:

$$
\Delta t_{\times 2} = \frac{K \ln 2}{T} = 87 \ln 2 \approx 60\ \text{s}
$$

**Under sustained full blocks the C-Chain base fee doubles every 60 seconds, and when idle it halves every 60 seconds.** That is what the comment on the 87 means. After each block, [`enforceMinExcess`][enforce-min-excess] raises $x$ to at least the excess that prices $p_{\min}$, so the clock never starts a block below the minimum.

The target and the minimum price are themselves set per block, as exponents in the header (`cchain/dynamic`), and each moves at most a capped step per block. The target's step is $2^{15}$ ([`TargetExponent.Toward`][target-toward]):

```go
func (t TargetExponent) Toward(desired *TargetExponent) TargetExponent {
	// Per ACP-176, the per-block exponent change is capped at 2^15.
	const maxDiff = 1 << 15
	return toward(t, desired, maxDiff)
}
```

With the target's conversion rate of $2^{25}$, that moves $T$ by at most a factor of $e^{2^{15}/2^{25}} = e^{1/1024}$ per block. The minimum price's step is a 3,600th of a doubling ([`PriceExponent.Toward`][price-toward]):

```go
func (p PriceExponent) Toward(desired *PriceExponent) PriceExponent {
	const (
		diffToDouble   = 288_230_376_151_711_744 // conversionRate * ln(2)
		blocksToDouble = 3_600
		maxDiff        = diffToDouble / blocksToDouble
	)
	return toward(p, desired, maxDiff)
}
```

That moves $p_{\min}$ by at most $2^{1/3600} \approx 1.000193$ per block.

When the target changes, [`AfterBlock`][after-block] rescales the excess so that $x/K$, and therefore the price, is unchanged, rounding up ([`scaleExcess`][scale-excess]):

$$
x' = \left\lceil x \cdot \frac{K'}{K} \right\rceil = \left\lceil x \cdot \frac{87\,T'}{87\,T} \right\rceil
$$

### What a burst of full blocks does

A full block is $\tau \lambda = 10$ seconds of gas at the clock's rate ([`SafeMaxBlockSize`][safe-max-block-size]):

```go
const (
	maxGasSecondsPerBlock = saeparams.TauSeconds * saeparams.Lambda
)
```

$$
g_{\max} = R \cdot \tau \lambda = 2T \cdot 10 = 20\,T, \qquad \Delta x = \frac{g_{\max}}{2} = 10\,T, \qquad \frac{p'}{p} = e^{10T/87T} = e^{10/87} \approx 1.122
$$

So each full block raises the fee by 12.2% at any target.

![Gas clock and base fee during a burst of full blocks](sae-gas-clock.svg)

The figure simulates nine full blocks, one every 2 s. The gas clock runs 90 s ahead of the wall in 18 s, the fee rises to $e^{90/87} \approx 2.8$ times its start, and it stays there until the wall catches the gas clock. Only then does idle time drain the excess.

## 3. Settlement and the worst case

A block settles the last block whose execution finished at least $\tau$ before its own timestamp ([`sae/block_builder.go`][block-builder-settle], [`LastToSettleAt`][last-to-settle]):

```go
lastSettled, ok, err := blocks.LastToSettleAt(bTime.Add(-saeparams.Tau), parent)
```

Its header records the settled block's height and the clock as it stood after that block executed: `settledHeight`, `settledGasUnix`, `settledGasNumerator`, `settledExcess`.

To price a new block, a node starts from that settled clock and replays every block since through [`worstcase.State`][worstcase-start]. For each block it advances to the block's time, charges the sum of its transactions' gas limits, not their gas used, plus the gas of its atomic import and export transactions, and then applies the gas config the block's header sets for its successor ([`FinishBlock`][worstcase-finish]):

```go
func (s *State) FinishBlock() (*blocks.WorstCaseBounds, error) {
	target, gasCfg := s.hooks.GasConfigAfter(s.curr)
	if err := s.clock.AfterBlock(s.blockSize, target, gasCfg); err != nil {
```

gasometer's [`sae`](../sae/sae.go) package does the same replay with avalanchego's own `gastime`, `proxytime` and `cchain/dynamic`, and parses atomic transactions with `cchain/tx`.

How far back settlement reaches decides how many blocks the replay covers. Over 10,000 mainnet blocks the settled block lagged its block by:

| Lag (blocks) | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12 to 16 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Blocks | 21 | 542 | 653 | 544 | 844 | 1,515 | 2,699 | 2,962 | 151 | 36 | 18 | 15 |

## 4. The worst-case bound

gasometer projects the base fee some blocks ahead, so it needs a ceiling: the highest base fee block $n+1+k$ can have, given the chain up to block $n$. [`sae.MaxBaseFeeAfter`](../sae/sae.go) runs the clock forward from the next block's bound through $k$ blocks, each of them full and each at the worst config its parent can step to, the minimum price up and the target down through avalanchego's own `Toward`. It then adds a rounding slack:

$$
\bar{p}_{n+1+k} = \left\lceil p^{\text{walk}}_k \cdot \left(1 + k \cdot 10^{-6}\right) \right\rceil
$$

```go
b := blocks[len(blocks)-1]
for range k {
	b.TargetExponent = uint64(dynamic.TargetExponent(b.TargetExponent).Toward(new(dynamic.TargetExponent)))
	b.MinPriceExponent = uint64(dynamic.PriceExponent(b.MinPriceExponent).Toward(new(dynamic.PriceExponent(math.MaxUint64))))
	target, cfg := gasConfig(b)
	if err := clock.AfterBlock(worstcase.SafeMaxBlockSize(clock.Rate()), target, cfg); err != nil {
		return nil, fmt.Errorf("full block: %w", err)
	}
}
return withRoundingSlack(price(clock), k), nil
```

Each part of that has a reason:

- **Full blocks with no time passing.** A full block is 10 s of gas, more than any block interval, so the gas clock stays ahead of the wall and `FastForwardTo` never drains anything. Each full block multiplies the fee by $e^{10/87} \approx 1.122$ at any target, as derived above.
- **The minimum price never binds.** It rises at most $2^{1/3600} \approx 1.0002$ per block against the clock's $1.122$, and the clock starts at or above it, so $e^{x/K}$ stays above $p_{\min}$ throughout.
- **The target only matters through rounding.** `scaleExcess` keeps $x/K$ across a target change but rounds up, and `CalculatePrice` approximates the exponential. Over 4,000 random futures (random settled clocks, target and minimum price walks within the `Toward` caps, block gas up to the limit, time advancing) the realised fee exceeded the bound without slack by at most $2.7 \times 10^{-9}$ per block. The $10^{-6}$ per block slack covers that about 370 times over, and overstates the ceiling by 0.012% at 120 blocks.

The slack is necessary. Without it, a target falling at the maximum rate under full blocks beats a fixed-config bound by 6 wei at $k = 1$, and a target falling 1,000 exponent units per block beats the walked bound by 1,282 wei at $k = 30$. The config walk is not the worst rounding path: it states the worst config the protocol can reach, and the slack is what keeps the bound strict.

On mainnet the bound held for every block it covered, and the tightest case at each horizon was:

| Horizon $k$ | 1 | 2 | 30 | 120 |
| --- | --- | --- | --- | --- |
| Smallest $\bar{p}_{n+1+k} / p_{n+1+k}$ | 1.0195 | 1.0451 | 20.94 | 654,192 |

At one or two blocks the ceiling is within 2% to 5% of what happened. Over minutes it is useless as a forecast, because nothing forces blocks to be full ($1.122^{120} \approx 10^{6}$); gasometer uses it only as a cap on its own projection.

## 5. What the RPC returns

Three numbers look like "the next base fee", and they differ.

**`eth_baseFee`** returns the last accepted block's worst-case bound at its latest end time ([`estimateNextBaseFee`][estimate-next-base-fee]):

```go
func (c *customAPI) estimateNextBaseFee() *big.Int {
	bounds := c.b.LastAccepted().WorstCaseBounds()
	if bounds == nil {
		return c.b.LastAccepted().EthBlock().BaseFee()
	}
	return bounds.LatestEndTime.BaseFee().ToBig()
}
```

**A block header's `baseFeePerGas`** is the builder's bound at the block's build time, set as shown in section 1. builders-hub documents both of these as worst-case bounds.

**The last element of `eth_feeHistory`'s `baseFeePerGas`** is one of two different things ([`FeeHistory`][fee-history-branch]). If the newest block requested is the last accepted block, it is that block's bound, as `eth_baseFee` returns. Otherwise it is the header fee of the block after the newest:

```go
if last == lastAcceptedNumber {
	bounds := lastAccepted.WorstCaseBounds()
	if bounds == nil {
		baseFee = append(baseFee, lastAccepted.EthBlock().BaseFee())
	} else {
		baseFee = append(baseFee, bounds.LatestEndTime.BaseFee().ToBig())
	}
} else if b := e.blockCache.getBlock(last + 1); b != nil {
	baseFee = append(baseFee, b.baseFee)
} else {
	return nil, nil, nil, nil, fmt.Errorf("%w: %d", errMissingBlock, last+1)
}
```

So the element depends on which node answers and when, not only on the request. `latest` resolves to the last **executed** block, not the last accepted one ([`ResolveRPCNumber`][resolve-rpc-number]), and `eth_blockNumber` returns the same height:

```go
case rpc.LatestBlockNumber:
	return c.LastExecuted().Height(), nil
```

Even on one node, then, `eth_feeHistory(n, "latest")` takes the second branch whenever execution trails acceptance. `FeeHistory` also reads the requested block and the [last accepted block][fee-history-last-accepted] separately, so a block accepted mid-call has the same effect. Both are rare on one node. The common case is a load-balanced endpoint: a client reads the latest block from one node, asks another for fee history up to it, and the second node has already accepted the next block.

In go-ethereum the element is a pure function of the newest block ([`eth/gasprice/feehistory.go`][geth-fee-history], here as vendored in libevm), and legacy coreth returned no next element at all:

```go
bf.results.nextBaseFee = eip1559.CalcBaseFee(chainconfig, bf.header)
```

Neither value is what the block will actually pay. Both are upper bounds, and the executed fee ([`ExecutedBaseFee`][executed-base-fee]) is no higher than either.

## 6. Validation

All of these figures come from tests in this repository that run against the public endpoint (`go test -tags live ./rpc`): [`live_replay_sae_test.go`](../rpc/live_replay_sae_test.go) and [`live_feehistory_sae_test.go`](../rpc/live_feehistory_sae_test.go).

| Check | Result |
| --- | --- |
| Head base fee replayed exactly, blocks 98,696,168 to 98,706,167 | 10,000 of 10,000 |
| Windows containing atomic transactions | 150, all exact |
| Mutation: transaction gas limits zeroed | 2,610 mismatches |
| Mutation: atomic gas zeroed | 31 of 150 atomic windows mismatch |
| Ceiling at $k$ = 1, 2, 30, 120 against the realised fee | never below it |
| `eth_feeHistory` next base fee, two requests (latest block, then history up to it), 199 samples | the successor's header fee in 40, every one with the successor already accepted on the node that answered |
| `eth_feeHistory(1, "latest")` against `eth_baseFee`, one node in one batch, 199 samples | different in 1 |

The mutations show that both inputs matter. Without transaction gas limits a quarter of the base fees come out wrong, and without atomic gas about a fifth of the windows that contain atomic transactions do.

The two-request samples read the latest block $E$, then ask in one batch for block $E+1$ and `eth_feeHistory(1, E)`. A node runs a batch in order, and a block number above its last accepted one fails with "not accepted yet", so whether $E+1$ resolves says which branch `FeeHistory` took; in every sample the value was the one that branch serves. Through `api.avax.network` a client that reads the latest block and then `eth_feeHistory` gets the successor's header fee about one time in five. On a single node, execution trailing acceptance accounts for about one in two hundred.

## 7. What a wallet should do

Bid on `eth_baseFee`, not on the tail of `eth_feeHistory`. `eth_baseFee` is always the same bound. Through a load-balanced endpoint, the tail of `eth_feeHistory` is a different number about one time in five, depending on which node answers. The fee actually charged is no higher than the bound, so a max fee set from it will not be outbid by the base fee within the next block. For a transaction meant to land within $k$ blocks, the bound in section 4 gives the max fee that is certain to cover the base fee.

[after-block]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gastime/acp176.go#L44-L67
[block-builder-basefee]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/sae/block_builder.go#L248-L249
[block-builder-settle]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/sae/block_builder.go#L409
[calculate-price]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/components/gas/gas.go#L85
[enforce-min-excess]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gastime/acp176.go#L97-L109
[estimate-next-base-fee]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/sae/rpc/custom.go#L46-L52
[executed-base-fee]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/blocks/execution.go#L252-L254
[fast-forward]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gastime/gastime.go#L173-L212
[fee-history-branch]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gasprice/estimator.go#L309-L320
[fee-history-last-accepted]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gasprice/estimator.go#L262-L267
[gas-config-after]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/cchain/hooks.go#L180-L198
[gastime-price]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gastime/gastime.go#L129-L135
[geth-fee-history]: https://github.com/ava-labs/libevm/blob/2c4d255e0a63/eth/gasprice/feehistory.go#L87
[last-to-settle]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/blocks/settlement.go#L178
[params]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/params/params.go#L15-L27
[price]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/cchain/dynamic/price.go#L22-L37
[price-toward]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/cchain/dynamic/price.go#L39-L50
[resolve-rpc-number]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/blocks/access.go#L124-L136
[safe-max-block-size]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/worstcase/state.go#L145-L155
[scale-excess]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gastime/acp176.go#L69-L87
[target]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/cchain/dynamic/target.go#L23-L33
[target-to-rate]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gastime/gastime.go#L91
[target-toward]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/cchain/dynamic/target.go#L35-L43
[tick]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/gastime/gastime.go#L156-L169
[worstcase-finish]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/worstcase/state.go#L330-L343
[worstcase-start]: https://github.com/ava-labs/avalanchego/blob/v1.15.2/vms/saevm/worstcase/state.go#L111-L123
