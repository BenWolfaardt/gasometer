#!/usr/bin/env bash
# Installs alert-rule.json into a Grafana stack, replacing any earlier copy.
#   GRAFANA_URL     e.g. https://<stack>.grafana.net
#   GRAFANA_TOKEN   a service account token with the Editor role
#   DATASOURCE_UID  the Prometheus data source the dashboard uses
set -euo pipefail

: "${GRAFANA_URL:?}" "${GRAFANA_TOKEN:?}" "${DATASOURCE_UID:?}"
dir=$(cd "$(dirname "$0")" && pwd)

api() {
	curl -fsS -H "Authorization: Bearer ${GRAFANA_TOKEN}" -H "Content-Type: application/json" \
		-H "X-Disable-Provenance: true" "$@"
}

api -X POST "${GRAFANA_URL}/api/folders" -d '{"uid":"gasometer","title":"gasometer"}' >/dev/null 2>&1 || true
api -X DELETE "${GRAFANA_URL}/api/v1/provisioning/alert-rules/gasometer-stalled" >/dev/null 2>&1 || true
sed "s/\${DATASOURCE_UID}/${DATASOURCE_UID}/" "${dir}/alert-rule.json" |
	api -X POST "${GRAFANA_URL}/api/v1/provisioning/alert-rules" -d @- >/dev/null
echo "installed gasometer-stalled"
