"""Generates deploy/grafana/gasometer.json. Run from the repo root: python3 deploy/grafana/dashboard.py deploy/grafana/gasometer.json."""

import json
import sys

DS = {"type": "prometheus", "uid": "${DS_PROMETHEUS}"}
NETWORKS = [("base", "Base"), ("cchain", "Avalanche C-Chain"), ("op", "OP Mainnet")]
SHOWN = [("market", "What senders pay"), ("node", "Node's suggestion"), ("gasometer", "gasometer")]
SEL = 'tx="native",horizon="30s",predictor=~"market|node|gasometer"'
COLORS = {"Base fee": "#5794F2", "L1 data fee": "#B877D9", "Tip it needed": "#73BF69", "Overpaid": "#F2495C"}

panels = []
next_id = 1
y = 0


def add(p, into=None):
    global next_id
    p["id"] = next_id
    next_id += 1
    (panels if into is None else into).append(p)
    return p


def relabel(expr, label, mapping):
    for src, dst in mapping:
        expr = f'label_replace({expr}, "{label}", "{dst}", "{label}", "{src}")'
    return expr


def mean(metric, labels, by):
    return (f"sum by ({by}) (increase(gasometer_{metric}_sum{{{labels}}}[$__range]))"
            f" / sum by ({by}) (increase(gasometer_{metric}_count{{{labels}}}[$__range]))")


def bars(sel, network_in_bar, share=False):
    """Per bar: what the cheapest landing bid needed, by component, plus what was paid above it, in USD or as a
    percentage of the bar."""
    required = mean("required_usd", sel, "network, predictor, component")
    required = relabel(required, "component", [("base_fee", "Base fee"), ("l1_fee", "L1 data fee"), ("tip", "Tip it needed")])
    overpaid = f'label_replace({mean("overpay_usd", sel, "network, predictor")}, "component", "Overpaid", "", "")'
    expr = f"({required}) or ({overpaid})"
    if share:
        expr = f"100 * ({expr}) / on (network, predictor) group_left () sum by (network, predictor) ({expr})"
    expr = relabel(relabel(expr, "predictor", SHOWN), "network", NETWORKS)
    return label_join(expr, network_in_bar)


def label_join(expr, network_in_bar):
    if network_in_bar:
        return f'label_join({expr}, "bar", " · ", "network", "predictor")'
    return f'label_join({expr}, "bar", "", "predictor")'


def barchart(title, w, h, x, expr, description, stacking, unit, legend=True):
    return {
        "type": "barchart", "title": title, "description": description, "datasource": DS,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "targets": [{"datasource": DS, "refId": "A", "expr": expr, "format": "table", "instant": True, "range": False}],
        "transformations": [
            {"id": "groupingToMatrix", "options": {"columnField": "component", "rowField": "bar", "valueField": "Value",
                                                   "emptyValue": "zero"}},
            {"id": "organize", "options": {"indexByName": {"bar\\component": 0, "Base fee": 1, "L1 data fee": 2,
                                                           "Tip it needed": 3, "Overpaid": 4}}},
        ],
        "fieldConfig": {"defaults": {"unit": unit, "decimals": 6 if unit == "currencyUSD" else 1,
                                     **({"min": 0, "max": 100} if unit == "percent" else {}),
                                     "custom": {"fillOpacity": 90, "lineWidth": 0}},
                        "overrides": [{"matcher": {"id": "byName", "options": name},
                                       "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": c}}]}
                                      for name, c in COLORS.items()]},
        "options": {"orientation": "horizontal", "stacking": stacking, "xField": "bar\\component", "showValue": "never",
                    "groupWidth": 0.8, "barWidth": 0.9, "fullHighlight": False,
                    "legend": {"showLegend": legend, "displayMode": "list", "placement": "bottom"},
                    "tooltip": {"mode": "multi", "sort": "none"}},
    }


add({"type": "text", "title": "", "gridPos": {"x": 0, "y": y, "w": 24, "h": 3}, "options": {"mode": "markdown", "content":
    "### How much of each transaction fee was unnecessary\n"
    "Every minute, on each network, gasometer prices a native transfer three ways and checks which blocks it would "
    "have landed in within 30 seconds. **Red is what was paid above the cheapest price that landed.** "
    "The fee model is built on Avalanche's [libevm](https://github.com/ava-labs/libevm), and C-Chain's base fee "
    "on [avalanchego](https://github.com/ava-labs/avalanchego)'s SAE gas clock."}})
y += 3

add(barchart(
    "Share of a native transfer's fee, bids landing within 30 s", 24, 12, 0, bars(SEL, True, share=True),
    "Each bar is 100% of what that bid paid, averaged over the selected range. Base fee, L1 data fee and the tip it "
    "needed are what the cheapest bid that landed in the same 30 s window paid; red is the rest. What senders pay is "
    "the median tip by gas of the latest block. Node's suggestion is eth_maxPriorityFeePerGas. gasometer bids the "
    "0.99 quantile of the cheapest required tip in each recent 30 s window. The required tip is the 5th percentile "
    "tip by gas a block included (0th on C-Chain), an upper bound on what inclusion needed.",
    "normal", "percent"))
y += 12

collecting = relabel("max by (network) (changes(gasometer_last_poll_timestamp_seconds[$__interval]) > bool 0)",
                     "network", NETWORKS)
add({"type": "state-timeline", "title": "Data collected", "datasource": DS, "interval": "3m",
     "description": "Green while a network completes polls. Red where polls stalled or no data arrived, so the bars "
                    "above cover less of the range.",
     "gridPos": {"x": 0, "y": y, "w": 24, "h": 4},
     "targets": [{"datasource": DS, "refId": "A", "expr": collecting, "legendFormat": "{{network}}"}],
     "fieldConfig": {"defaults": {"custom": {"fillOpacity": 80, "lineWidth": 0, "insertNulls": 360000},
                                  "color": {"mode": "fixed", "fixedColor": "red"},
                                  "mappings": [
                                      {"type": "value", "options": {
                                          "1": {"text": "Collecting", "color": "green", "index": 0},
                                          "0": {"text": "Stalled", "color": "red", "index": 1}}},
                                      {"type": "special", "options": {
                                          "match": "null", "result": {"text": "No data", "color": "red", "index": 2}}}]},
                     "overrides": []},
     "options": {"showValue": "never", "mergeValues": True, "rowHeight": 0.8, "alignValue": "left",
                 "legend": {"showLegend": False}, "tooltip": {"mode": "single", "sort": "none"}}})
y += 4

usd = {"type": "row", "title": "The same, in USD", "collapsed": True, "gridPos": {"x": 0, "y": y, "w": 24, "h": 1},
       "panels": []}
add(usd)
y += 1
for i, (net, name) in enumerate(NETWORKS):
    add(barchart(name, 8, 8, 8 * i, bars(f'network="{net}",{SEL}', False),
                 "Mean USD per native transfer landing within 30 s.", "normal", "currencyUSD", legend=i == 0), usd["panels"])
y += 8

health = {"type": "row", "title": "Service health", "collapsed": True, "gridPos": {"x": 0, "y": y, "w": 24, "h": 1},
          "panels": []}
add(health)
y += 1


def stat(title, x, expr, description, unit, steps):
    return {"type": "stat", "title": title, "description": description, "datasource": DS,
            "gridPos": {"x": x, "y": y, "w": 8, "h": 5},
            "targets": [{"datasource": DS, "refId": "A", "expr": expr, "legendFormat": "{{network}}",
                         "instant": True, "range": False}],
            "fieldConfig": {"defaults": {"unit": unit, "decimals": 0,
                                         "thresholds": {"mode": "absolute", "steps": steps}}, "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"]}, "colorMode": "background",
                        "textMode": "value_and_name"}}


green_red = lambda red: [{"color": "green", "value": None}, {"color": "red", "value": red}]
add(stat("Fee model mismatches", 0, "sum by (network) (increase(gasometer_model_mismatch_total[$__range]))",
         "Polls where gasometer's next base fee differed from the node's. Expected 0.", "none", green_red(1)),
    health["panels"])
add(stat("Since last poll", 8, "time() - gasometer_last_poll_timestamp_seconds",
         "Seconds since each network's last completed poll. Polls run every 60 s.", "s", green_red(180)),
    health["panels"])
add(stat("Settled bids", 16,
         'sum by (network) (increase(gasometer_predictions_resolved_total{outcome=~"included|missed"}[$__range]))',
         "Bids scored over the selected range.", "none", [{"color": "blue", "value": None}]), health["panels"])

dashboard = {
    "__inputs": [{"name": "DS_PROMETHEUS", "label": "Prometheus", "type": "datasource",
                  "pluginId": "prometheus", "pluginName": "Prometheus"}],
    "__requires": [{"type": "datasource", "id": "prometheus", "name": "Prometheus", "version": "1.0.0"}],
    "uid": "gasometer",
    "title": "gasometer",
    "description": "How much of an EVM transaction fee was unnecessary, measured live on Base, Avalanche C-Chain and OP Mainnet.",
    "tags": ["gasometer", "evm", "fees"],
    "timezone": "utc",
    "editable": True,
    "graphTooltip": 0,
    "refresh": "5m",
    "time": {"from": "now-24h", "to": "now"},
    "schemaVersion": 39,
    "version": 1,
    "templating": {"list": []},
    "annotations": {"list": []},
    "links": [],
    "panels": panels,
}

with open(sys.argv[1], "w") as f:
    json.dump(dashboard, f, indent=2, ensure_ascii=False)
    f.write("\n")
