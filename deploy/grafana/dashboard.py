"""Generates deploy/grafana/gasometer.json. Run from the repo root: python3 deploy/grafana/dashboard.py deploy/grafana/gasometer.json."""

import json
import sys

DS = {"type": "prometheus", "uid": "${DS_PROMETHEUS}"}
NETWORKS = [("cchain", "Avalanche C-Chain"), ("base", "Base"), ("op", "Optimism")]
SHOWN = [("market", "Market"), ("node", "Node"), ("gasometer", "gasometer")]
SEL = 'tx="native",horizon="30s",predictor=~"market|node|gasometer"'
COMPONENTS = [("base_fee", "Base fee"), ("l1_fee", "L1 data fee"), ("tip", "Tip needed")]
COLORS = {"Base fee": "#5794F2", "L1 data fee": "#B877D9", "Tip needed": "#73BF69", "Overpaid": "#F2495C"}
LINE_COLORS = {"Market": "#FF9830", "Node": "#B877D9", "gasometer": "#73BF69"}
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


def bars(sel, share=False):
    """Per predictor: what the cheapest landing bid needed, by component, plus what was paid above it, in USD or as a
    percentage of the bar. Every component is padded with zero so all three networks show the same legend."""
    required = relabel(mean("required_usd", sel, "network, predictor, component"), "component", COMPONENTS)
    overpaid = mean("overpay_usd", sel, "network, predictor")
    expr = f'({required}) or label_replace({overpaid}, "component", "Overpaid", "", "")'
    for _, name in COMPONENTS:
        expr += f' or label_replace(0 * ({overpaid}), "component", "{name}", "", "")'
    if share:
        expr = f"100 * ({expr}) / on (network, predictor) group_left () sum by (network, predictor) ({expr})"
    return relabel(expr, "predictor", SHOWN)


def barchart(title, w, h, x, expr, description, unit):
    return {
        "type": "barchart", "title": title, "description": description, "datasource": DS,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "targets": [{"datasource": DS, "refId": "A", "expr": expr, "format": "table", "instant": True, "range": False}],
        "transformations": [
            {"id": "groupingToMatrix", "options": {"columnField": "component", "rowField": "predictor",
                                                   "valueField": "Value", "emptyValue": "zero"}},
            {"id": "organize", "options": {"indexByName": {"predictor\\component": 0, "Base fee": 1, "L1 data fee": 2,
                                                           "Tip needed": 3, "Overpaid": 4}}},
            {"id": "sortBy", "options": {"sort": [{"field": "predictor\\component"}]}},
        ],
        "fieldConfig": {"defaults": {"unit": unit, **({"decimals": 0, "min": 0, "max": 100} if unit == "percent" else {}),
                                     "custom": {"fillOpacity": 90, "lineWidth": 0,
                                                **({"axisPlacement": "hidden"} if unit == "percent" else {})}},
                        "overrides": [{"matcher": {"id": "byName", "options": name},
                                       "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": c}}]}
                                      for name, c in COLORS.items()]
                                     # The percent axis is hidden because Grafana clips its 100% label; the bar labels stay.
                                     + [{"matcher": {"id": "byName", "options": "predictor\\component"},
                                         "properties": [{"id": "custom.axisPlacement", "value": "auto"}]}]},
        "options": {"orientation": "horizontal", "stacking": "normal", "xField": "predictor\\component",
                    "showValue": "never", "groupWidth": 0.8, "barWidth": 0.9, "fullHighlight": False,
                    "legend": {"showLegend": True, "displayMode": "list", "placement": "bottom"},
                    "tooltip": {"mode": "multi", "sort": "none"}},
    }


def overpaid_over_time(net):
    sel = f'network="{net}",{SEL}'
    expr = (f"100 * sum by (predictor) (increase(gasometer_overpay_usd_sum{{{sel}}}[1h]))"
            f" / sum by (predictor) (increase(gasometer_paid_usd_sum{{{sel}}}[1h]))")
    return relabel(expr, "predictor", SHOWN)


def timeseries(title, x, expr, description):
    return {
        "type": "timeseries", "title": title, "description": description, "datasource": DS, "interval": "5m",
        "gridPos": {"x": x, "y": y, "w": 8, "h": 8},
        "targets": [{"datasource": DS, "refId": "A", "expr": expr, "legendFormat": "{{predictor}}"}],
        "fieldConfig": {"defaults": {"unit": "percent", "decimals": 0, "min": 0, "max": 100,
                                     "custom": {"lineWidth": 2, "fillOpacity": 0, "showPoints": "never",
                                                "spanNulls": False}},
                        "overrides": [{"matcher": {"id": "byName", "options": name},
                                       "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": c}}]}
                                      for name, c in LINE_COLORS.items()]},
        "options": {"legend": {"showLegend": True, "displayMode": "list", "placement": "bottom"},
                    "tooltip": {"mode": "multi", "sort": "desc"}},
    }


def row(title):
    global y
    add({"type": "row", "title": title, "collapsed": False, "gridPos": {"x": 0, "y": y, "w": 24, "h": 1}, "panels": []})
    y += 1


# Grafana Cloud's public dashboards query every panel and fail on one with no queries, so the text panel carries
# a trivial one it never displays.
add({"type": "text", "title": "", "gridPos": {"x": 0, "y": y, "w": 24, "h": 8}, "datasource": DS,
     "targets": [{"datasource": DS, "refId": "A", "expr": "vector(1)", "instant": True, "range": False}],
     "options": {"mode": "markdown", "content":
    "### How much of each transaction fee was unnecessary\n"
    "Every minute, on each network, gasometer prices a native transfer three ways and checks which blocks it would "
    "have landed in within 30 seconds. **Red is what was paid above the cheapest price that landed.**\n\n"
    "- **Market** bids the median tip of the latest block: what senders are paying.\n"
    "- **Node** bids the node's suggested tip, `eth_maxPriorityFeePerGas`.\n"
    "- **gasometer** bids the 0.99 quantile of the cheapest tip that landed in recent 30 s windows.\n"
    "- **Tip needed** is the 5th percentile tip a block included (the lowest on C-Chain), an upper bound on what "
    "inclusion needed.\n\n"
    "The fee model is built on Avalanche's [libevm](https://github.com/ava-labs/libevm), and C-Chain's base fee on "
    "[avalanchego](https://github.com/ava-labs/avalanchego)'s SAE gas clock.\n\n"
    "**GitHub:** [`github.com/BenWolfaardt/gasometer`](https://github.com/BenWolfaardt/gasometer)"}})
y += 8

row("Share of the fee that was overpaid")
for i, (net, name) in enumerate(NETWORKS):
    add(barchart(name, 8, 9, 8 * i, bars(f'network="{net}",{SEL}', share=True),
                 "Each bar is 100% of what that bid paid, averaged over the selected range.", "percent"))
y += 9

row("Cost per transfer, in USD")
for i, (net, name) in enumerate(NETWORKS):
    add(barchart(name, 8, 9, 8 * i, bars(f'network="{net}",{SEL}'),
                 "Mean USD per native transfer, averaged over the selected range.", "currencyUSD"))
y += 9

row("Overpaid over time")
for i, (net, name) in enumerate(NETWORKS):
    add(timeseries(name, 8 * i, overpaid_over_time(net), "The red share of each bar, over the trailing hour."))
y += 8

row("Service health")


def stat(title, x, expr, description, unit, steps):
    return {"type": "stat", "title": title, "description": description, "datasource": DS,
            "gridPos": {"x": x, "y": y, "w": 8, "h": 5},
            "targets": [{"datasource": DS, "refId": chr(ord("A") + i), "legendFormat": name.removeprefix("Avalanche "),
                         "instant": True,
                         "range": False, "expr": expr.replace("NETWORK", f'network="{net}"')}
                        for i, (net, name) in enumerate(NETWORKS)],
            "fieldConfig": {"defaults": {"unit": unit, "decimals": 0,
                                         "thresholds": {"mode": "absolute", "steps": steps}}, "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"]}, "colorMode": "background",
                        "textMode": "value_and_name"}}


green_red = lambda red: [{"color": "green", "value": None}, {"color": "red", "value": red}]
add(stat("Fee model mismatches", 0, "sum (increase(gasometer_model_mismatch_total{NETWORK}[$__range]))",
         "Polls where gasometer's next base fee differed from the node's. Expected 0.", "none", green_red(1)))
add(stat("Since last poll", 8, "time() - max (gasometer_last_poll_timestamp_seconds{NETWORK})",
         "Seconds since each network's last poll. Polls run every 60 s.", "suffix: s", green_red(180)))
add(stat("Settled bids", 16,
         'sum (increase(gasometer_predictions_resolved_total{NETWORK,outcome=~"included|missed"}[$__range]))',
         "Bids scored over the selected range.", "none", [{"color": "blue", "value": None}]))
y += 5

row("Data collected")
collecting = relabel("max by (network) (changes(gasometer_last_poll_timestamp_seconds[$__interval]) > bool 0)",
                     "network", NETWORKS)
add({"type": "state-timeline", "title": "", "datasource": DS, "interval": "3m",
     "description": "Red where polls stalled, so the charts cover less of the range.",
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

dashboard = {
    "__inputs": [{"name": "DS_PROMETHEUS", "label": "Prometheus", "type": "datasource",
                  "pluginId": "prometheus", "pluginName": "Prometheus"}],
    "__requires": [{"type": "datasource", "id": "prometheus", "name": "Prometheus", "version": "1.0.0"}],
    "uid": "gasometer",
    "title": "gasometer",
    "description": "How much of an EVM transaction fee was unnecessary, measured live on Avalanche C-Chain, Base and Optimism.",
    "tags": ["gasometer", "evm", "fees"],
    "timezone": "utc",
    "editable": True,
    "graphTooltip": 1,
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
