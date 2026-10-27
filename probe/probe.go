// Package probe sends a ladder of transactions at fixed tips and records which were included and how fast, so the tip
// inclusion needs is measured rather than bounded from the tips blocks happened to include.
package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/BenWolfaardt/gasometer"
)

// Ladder is the tips under test in wei, rung i on account i+1; a nil tip bids the node's suggestion of the moment.
var Ladder = []*big.Int{
	big.NewInt(0), big.NewInt(1), big.NewInt(10), big.NewInt(100), nil,
	big.NewInt(10_000), big.NewInt(1_000_000), big.NewInt(1_000_000_000),
}

// FundingAccount is the account Fund pays rungs from and Sweep returns them to.
const FundingAccount = 0

// transferGas is a plain transfer's gas.
const transferGas = 21_000

// Params are a campaign's timings.
type Params struct {
	Network string
	// Timeout is how long a probe may wait for inclusion before it is replaced, and the replacement after it.
	Timeout time.Duration
	// PollInterval is the time between receipt lookups.
	PollInterval time.Duration
}

// Campaign sends rounds of the ladder and records each probe.
type Campaign struct {
	chain  gasometer.ProbeChain
	store  gasometer.ProbeStore
	log    *slog.Logger
	params Params
}

// NewCampaign returns a Campaign. store is used only by Run and Round, so Fund and Sweep may pass nil.
func NewCampaign(chain gasometer.ProbeChain, store gasometer.ProbeStore, log *slog.Logger, params Params) *Campaign {
	return &Campaign{chain: chain, store: store, log: log, params: params}
}

// Run starts a round every interval until duration has passed or ctx ends. A round in progress when the duration
// ends is finished; a failed round is logged and the campaign goes on.
func (c *Campaign) Run(ctx context.Context, interval, duration time.Duration) error {
	deadline := time.Now().Add(duration)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.Round(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.log.Warn("probe round failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if !time.Now().Before(deadline) {
			return nil
		}
	}
}

// Round sends one probe per rung at once and waits for every one to settle.
func (c *Campaign) Round(ctx context.Context) error {
	baseFee, err := c.chain.BaseFee(ctx)
	if err != nil {
		return fmt.Errorf("base fee: %w", err)
	}
	nodeTip, err := c.chain.NodeTip(ctx)
	if err != nil {
		return fmt.Errorf("node tip: %w", err)
	}
	errs := make([]error, len(Ladder))
	var wg sync.WaitGroup
	for i, tip := range Ladder {
		if tip == nil {
			tip = nodeTip
		}
		wg.Go(func() { errs[i] = c.probe(ctx, i+1, tip, baseFee, nodeTip) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// probe sends one rung's transaction, replaces it at the same nonce if it is not included in time, and records it.
// A rung with a transaction still pending skips the round, so nonces never fork.
func (c *Campaign) probe(ctx context.Context, account int, tip, baseFee, nodeTip *big.Int) error {
	latest, pending, err := c.chain.Nonces(ctx, account)
	if err != nil {
		return fmt.Errorf("rung %d nonce: %w", account, err)
	}
	if pending > latest {
		c.log.Warn("rung has a transaction pending; skipping the round", "rung", account, "latest", latest, "pending", pending)
		return nil
	}
	head, err := c.chain.Head(ctx)
	if err != nil {
		return fmt.Errorf("rung %d head: %w", account, err)
	}
	tx := gasometer.ProbeTx{From: account, To: account, Value: new(big.Int), Nonce: latest, Tip: tip, MaxFee: maxFee(baseFee, tip)}
	p := gasometer.Probe{
		Network: c.params.Network, Rung: account, Account: c.chain.Address(account), Nonce: tx.Nonce,
		Tip: tx.Tip, MaxFee: tx.MaxFee, HeadAtSend: head, SentAt: time.Now(),
	}
	hash, err := c.chain.Send(ctx, tx)
	switch {
	case errors.Is(err, gasometer.ErrTxRejected):
		p.Outcome, p.Error = gasometer.ProbeRejected, err.Error()
		return c.insert(ctx, p)
	case err != nil:
		return fmt.Errorf("rung %d send: %w", account, err)
	}
	p.TxHash = hash
	_, inc, err := c.wait(ctx, hash)
	if err != nil {
		return err
	}
	if inc != nil {
		return c.insert(ctx, included(p, inc))
	}
	replacement := tx
	replacement.Tip = bump(maxInt(tip, nodeTip))
	replacement.MaxFee = maxInt(maxFee(baseFee, replacement.Tip), bump(tx.MaxFee))
	replacementHash, err := c.chain.Send(ctx, replacement)
	if err != nil {
		p.Outcome = gasometer.ProbeTimeout
		c.log.Warn("replacement not sent; the rung waits until its transaction lands", "rung", account, "err", err)
		return errors.Join(c.insert(ctx, p), ignoreRejected(err))
	}
	which, inc, err := c.wait(ctx, hash, replacementHash)
	if err != nil {
		return err
	}
	if inc != nil && which == 0 {
		return c.insert(ctx, included(p, inc))
	}
	if inc == nil {
		c.log.Warn("replacement not included in time; the rung waits until it lands", "rung", account, "hash", replacementHash)
	}
	p.Outcome, p.ReplacedBy = gasometer.ProbeReplaced, replacementHash
	return c.insert(ctx, p)
}

// wait polls for a receipt of any of hashes until the timeout, returning the index of the one included, or a nil
// inclusion when none was. A failed lookup is retried: behind a load balancer a receipt can be missing on one backend.
func (c *Campaign) wait(parent context.Context, hashes ...string) (int, *gasometer.Inclusion, error) {
	ctx, cancel := context.WithTimeout(parent, c.params.Timeout)
	defer cancel()
	ticker := time.NewTicker(c.params.PollInterval)
	defer ticker.Stop()
	for {
		for i, h := range hashes {
			inc, err := c.chain.Inclusion(ctx, h)
			if err != nil {
				c.log.Debug("receipt lookup failed", "hash", h, "err", err)
				continue
			}
			if inc != nil {
				return i, inc, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, nil, parent.Err()
		case <-ticker.C:
		}
	}
}

// Fund tops each rung up to target from FundingAccount and waits for the transfers.
func (c *Campaign) Fund(ctx context.Context, target *big.Int) error {
	baseFee, nodeTip, err := c.fees(ctx)
	if err != nil {
		return err
	}
	_, nonce, err := c.chain.Nonces(ctx, FundingAccount)
	if err != nil {
		return fmt.Errorf("funding account nonce: %w", err)
	}
	var hashes []string
	for account := 1; account <= len(Ladder); account++ {
		balance, err := c.chain.Balance(ctx, account)
		if err != nil {
			return fmt.Errorf("rung %d balance: %w", account, err)
		}
		if balance.Cmp(target) >= 0 {
			continue
		}
		hash, err := c.chain.Send(ctx, gasometer.ProbeTx{
			From: FundingAccount, To: account, Value: new(big.Int).Sub(target, balance), Nonce: nonce,
			Tip: nodeTip, MaxFee: maxFee(baseFee, nodeTip),
		})
		if err != nil {
			return fmt.Errorf("fund rung %d: %w", account, err)
		}
		nonce++
		hashes = append(hashes, hash)
	}
	return c.waitAll(ctx, hashes)
}

// Sweep returns every rung's balance, less the transfer's maximum fee, to FundingAccount and waits for the transfers.
// A rung with a transaction pending is left for a later sweep.
func (c *Campaign) Sweep(ctx context.Context) error {
	baseFee, nodeTip, err := c.fees(ctx)
	if err != nil {
		return err
	}
	fee := maxFee(baseFee, nodeTip)
	cost := new(big.Int).Mul(fee, big.NewInt(transferGas))
	var hashes []string
	for account := 1; account <= len(Ladder); account++ {
		latest, pending, err := c.chain.Nonces(ctx, account)
		if err != nil {
			return fmt.Errorf("rung %d nonce: %w", account, err)
		}
		if pending > latest {
			c.log.Warn("rung has a transaction pending; not swept", "rung", account)
			continue
		}
		balance, err := c.chain.Balance(ctx, account)
		if err != nil {
			return fmt.Errorf("rung %d balance: %w", account, err)
		}
		if balance.Cmp(cost) <= 0 {
			continue
		}
		hash, err := c.chain.Send(ctx, gasometer.ProbeTx{
			From: account, To: FundingAccount, Value: new(big.Int).Sub(balance, cost), Nonce: latest,
			Tip: nodeTip, MaxFee: fee,
		})
		if err != nil {
			return fmt.Errorf("sweep rung %d: %w", account, err)
		}
		hashes = append(hashes, hash)
	}
	return c.waitAll(ctx, hashes)
}

func (c *Campaign) fees(ctx context.Context) (baseFee, nodeTip *big.Int, err error) {
	if baseFee, err = c.chain.BaseFee(ctx); err != nil {
		return nil, nil, fmt.Errorf("base fee: %w", err)
	}
	if nodeTip, err = c.chain.NodeTip(ctx); err != nil {
		return nil, nil, fmt.Errorf("node tip: %w", err)
	}
	return baseFee, nodeTip, nil
}

func (c *Campaign) waitAll(ctx context.Context, hashes []string) error {
	for _, h := range hashes {
		_, inc, err := c.wait(ctx, h)
		if err != nil {
			return err
		}
		if inc == nil {
			return fmt.Errorf("transfer %s not included within %s", h, c.params.Timeout)
		}
	}
	return nil
}

func (c *Campaign) insert(ctx context.Context, p gasometer.Probe) error {
	if err := c.store.Insert(ctx, p); err != nil {
		return fmt.Errorf("store rung %d probe: %w", p.Rung, err)
	}
	return nil
}

func included(p gasometer.Probe, inc *gasometer.Inclusion) gasometer.Probe {
	p.Outcome = gasometer.ProbeIncluded
	p.IncludedBlock, p.IncludedAtMillis = &inc.Block, &inc.TimeMilliseconds
	return p
}

// maxFee is twice the base fee plus the tip, so the base fee never decides inclusion and only the tip is under test.
func maxFee(baseFee, tip *big.Int) *big.Int {
	f := new(big.Int).Lsh(baseFee, 1)
	return f.Add(f, tip)
}

// bump is v raised by the 10% a node requires of a replacement, rounded up, and by at least 1.
func bump(v *big.Int) *big.Int {
	b := new(big.Int).Mul(v, big.NewInt(11))
	b.Add(b, big.NewInt(9))
	b.Quo(b, big.NewInt(10))
	if b.Cmp(v) <= 0 {
		b.Add(v, big.NewInt(1))
	}
	return b
}

func maxInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

// ignoreRejected drops a refusal, which leaves the rung waiting, and keeps any other failure.
func ignoreRejected(err error) error {
	if errors.Is(err, gasometer.ErrTxRejected) {
		return nil
	}
	return err
}
