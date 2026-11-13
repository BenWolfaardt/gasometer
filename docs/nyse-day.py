#!/usr/bin/env python3
"""Draws nyse-day.svg: AVAX price, C-Chain base fee and block fullness, 9 Nov 08:00 to 10 Nov 08:00 UTC.

Usage: python3 -I nyse-day.py   (reads nyse-day.csv beside it, writes nyse-day.svg beside it)

nyse-day.csv is ten-minute buckets of gasometer's blocks and snapshots tables, from this query:

    with b as (
      select to_timestamp(floor(extract(epoch from observed_at)/600)*600) at time zone 'UTC' t, count(*) n,
             min(base_fee) mn, max(base_fee) mx, avg(gas_used_ratio) r
      from blocks where network='cchain' and observed_at >= '2026-11-09 08:00Z' and observed_at < '2026-11-10 08:00Z'
      group by 1),
    s as (
      select to_timestamp(floor(extract(epoch from observed_at)/600)*600) at time zone 'UTC' t, avg(native_usd) usd
      from snapshots where network='cchain' and observed_at >= '2026-11-09 08:00Z' and observed_at < '2026-11-10 08:00Z'
      group by 1)
    select to_char(b.t,'YYYY-MM-DD"T"HH24:MI') t, n, mn, mx, round(r::numeric,5) r, round(s.usd,4) usd
    from b left join s using (t) order by b.t;

#808080 ink reads on light and dark pages; blue and orange match the other figures. Small multiples, one y-axis
each: price, fee (log scale) and fullness have different units.
"""

import csv
import math
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).parent
W, LEFT, RIGHT = 640, 64, 616
T0 = datetime(2026, 11, 9, 8, tzinfo=timezone.utc).timestamp()
T1 = datetime(2026, 11, 10, 8, tzinfo=timezone.utc).timestamp()
# Shaded spans: the AVAX jump on the NYSE news (12:00 to 16:00, from the price data) and the floor rise
# by block header time (last block at 2.5 gwei 23:40:20, first block settled at 7.5 gwei 05:14:18).
NEWS = (datetime(2026, 11, 9, 12, tzinfo=timezone.utc).timestamp(), datetime(2026, 11, 9, 16, tzinfo=timezone.utc).timestamp())
RISE = (datetime(2026, 11, 9, 23, 40, 20, tzinfo=timezone.utc).timestamp(), datetime(2026, 11, 10, 5, 14, 18, tzinfo=timezone.utc).timestamp())
PANELS = {"price": (64, 164), "fee": (204, 364), "full": (404, 504)}
BLUE, ORANGE, INK = "#3b82f6", "#e8590c", "#808080"


def x(t):
    return LEFT + (t - T0) / (T1 - T0) * (RIGHT - LEFT)


def y_lin(v, lo, hi, panel):
    top, bottom = PANELS[panel]
    return bottom - (v - lo) / (hi - lo) * (bottom - top)


def y_log(v, lo, hi, panel):
    top, bottom = PANELS[panel]
    return bottom - (math.log(v) - math.log(lo)) / (math.log(hi) - math.log(lo)) * (bottom - top)


def polyline(points, cls):
    return f'<polyline class="{cls}" points="' + " ".join(f"{px:.1f},{py:.1f}" for px, py in points) + '"/>'


def main():
    rows = []
    with open(HERE / "nyse-day.csv") as f:
        for r in csv.DictReader(f):
            t = datetime.strptime(r["t"], "%Y-%m-%dT%H:%M").replace(tzinfo=timezone.utc).timestamp() + 300
            rows.append((t, float(r["usd"]), int(r["mn"]) / 1e9, int(r["mx"]) / 1e9, float(r["r"]) * 100))

    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} 550" font-family="sans-serif" font-size="12">',
        f"<style>text{{fill:{INK}}}.ax{{stroke:{INK};stroke-width:1}}.grid{{stroke:{INK};stroke-width:1;stroke-opacity:0.25}}"
        f".price{{stroke:{INK};stroke-width:2;fill:none}}.lo{{stroke:{ORANGE};stroke-width:2;fill:none}}"
        f".hi{{stroke:{ORANGE};stroke-width:1;stroke-opacity:0.6;fill:none}}.full{{stroke:{BLUE};stroke-width:2;fill:none}}</style>",
    ]
    for (a, b), label in ((NEWS, "AVAX jumps on NYSE news"), (RISE, "validators raise the floor")):
        out.append(f'<rect x="{x(a):.1f}" y="{PANELS["price"][0]}" width="{x(b) - x(a):.1f}" height="{PANELS["full"][1] - PANELS["price"][0]}" fill="{INK}" fill-opacity="0.1"/>')
        out.append(f'<text x="{(x(a) + x(b)) / 2:.1f}" y="16" text-anchor="middle">{label}</text>')

    # Price panel.
    lo, hi = 7.8, 9.0
    for v in (8.0, 8.5, 9.0):
        yy = y_lin(v, lo, hi, "price")
        out.append(f'<line class="grid" x1="{LEFT}" y1="{yy:.1f}" x2="{RIGHT}" y2="{yy:.1f}"/><text x="{LEFT - 6}" y="{yy + 4:.1f}" text-anchor="end">${v:.2f}</text>')
    out.append(polyline([(x(t), y_lin(p, lo, hi, "price")) for t, p, *_ in rows], "price"))
    out.append(f'<text x="{LEFT}" y="{PANELS["price"][0] - 4}">AVAX price (USD, Chainlink)</text>')

    # Fee panel, log scale: the lowest fee in each bucket traces the floor, the highest shows bursts.
    lo, hi = 2, 60
    for v in (2.5, 5, 10, 20, 50):
        yy = y_log(v, lo, hi, "fee")
        out.append(f'<line class="grid" x1="{LEFT}" y1="{yy:.1f}" x2="{RIGHT}" y2="{yy:.1f}"/><text x="{LEFT - 6}" y="{yy + 4:.1f}" text-anchor="end">{v:g}</text>')
    out.append(polyline([(x(t), y_log(m, lo, hi, "fee")) for t, _, m, _, _ in rows], "lo"))
    out.append(polyline([(x(t), y_log(m, lo, hi, "fee")) for t, _, _, m, _ in rows], "hi"))
    peak = max(rows, key=lambda r: r[3])
    out.append(f'<text x="{x(peak[0]) + 6:.1f}" y="{y_log(peak[3], lo, hi, "fee") + 4:.1f}">{peak[3]:.1f} gwei</text>')
    out.append(f'<text x="{RIGHT}" y="{y_log(7.5, lo, hi, "fee") + 30:.1f}" text-anchor="end" style="fill:{ORANGE}">lowest fee: the floor</text>')
    out.append(f'<text x="{x(T0 + 12 * 3600):.1f}" y="{y_log(4.6, lo, hi, "fee"):.1f}" text-anchor="middle" style="fill:{ORANGE}">highest fee</text>')
    out.append(f'<text x="{LEFT}" y="{PANELS["fee"][0] - 4}">Base fee (gwei, log scale), lowest and highest in each 10 minutes</text>')

    # Fullness panel.
    lo, hi = 0, 14
    for v in (0, 5, 10):
        yy = y_lin(v, lo, hi, "full")
        out.append(f'<line class="grid" x1="{LEFT}" y1="{yy:.1f}" x2="{RIGHT}" y2="{yy:.1f}"/><text x="{LEFT - 6}" y="{yy + 4:.1f}" text-anchor="end">{v}%</text>')
    out.append(polyline([(x(t), y_lin(f, lo, hi, "full")) for t, *_, f in rows], "full"))
    out.append(f'<text x="{LEFT}" y="{PANELS["full"][0] - 4}">Mean block fullness (gas charged / gas limit)</text>')

    # Shared time axis.
    bottom = PANELS["full"][1]
    out.append(f'<line class="ax" x1="{LEFT}" y1="{bottom}" x2="{RIGHT}" y2="{bottom}"/>')
    for h in range(0, 25, 4):
        t = T0 + h * 3600
        hour = (8 + h) % 24
        day = "9 Nov" if 8 + h < 24 else "10 Nov"
        out.append(f'<line class="ax" x1="{x(t):.1f}" y1="{bottom}" x2="{x(t):.1f}" y2="{bottom + 4}"/>')
        out.append(f'<text x="{x(t):.1f}" y="{bottom + 18}" text-anchor="middle">{hour:02d}:00</text>')
        if hour in (8, 0) and h < 24:
            out.append(f'<text x="{x(t):.1f}" y="{bottom + 34}" text-anchor="middle">{day}</text>')
    out.append(f'<text x="{RIGHT}" y="{bottom + 34}" text-anchor="end">UTC</text>')
    out.append("</svg>")
    (HERE / "nyse-day.svg").write_text("\n".join(out) + "\n")


if __name__ == "__main__":
    main()
