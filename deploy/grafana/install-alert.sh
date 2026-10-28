#!/usr/bin/env bash
# Installs alert-rule.json into a Grafana stack, replacing any earlier copy.
#   GRAFANA_URL     e.g. https://<stack>.grafana.net
#   GRAFANA_TOKEN   a service account token with the Admin role (Editor gets 403 on alert-rule provisioning)
#   DATASOURCE_UID  the Prometheus data source the dashboard uses
set -euo pipefail

: "${GRAFANA_URL:?}" "${GRAFANA_TOKEN:?}" "${DATASOURCE_UID:?}"
dir=$(cd "$(dirname "$0")" && pwd)

api() {
	curl -fsS -H "Authorization: Bearer ${GRAFANA_TOKEN}" -H "Content-Type: application/json" \
		-H "X-Disable-Provenance: true" "$@"
}

# Returns the HTTP status without failing on it, so each caller decides which statuses are fine.
status() {
	curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${GRAFANA_TOKEN}" \
		-H "Content-Type: application/json" -H "X-Disable-Provenance: true" "$@"
}

code=$(status -X POST "${GRAFANA_URL}/api/folders" -d '{"uid":"gasometer","title":"gasometer"}')
case "$code" in 200 | 409 | 412) ;; *) echo "create folder: HTTP ${code}" >&2 && exit 1 ;; esac
code=$(status -X DELETE "${GRAFANA_URL}/api/v1/provisioning/alert-rules/gasometer-stalled")
case "$code" in 200 | 204 | 404) ;; *) echo "delete old rule: HTTP ${code}" >&2 && exit 1 ;; esac
sed "s/\${DATASOURCE_UID}/${DATASOURCE_UID}/" "${dir}/alert-rule.json" |
	api -X POST "${GRAFANA_URL}/api/v1/provisioning/alert-rules" -d @- >/dev/null
echo "installed gasometer-stalled"
