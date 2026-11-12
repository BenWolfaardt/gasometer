#!/usr/bin/env bash
# Installs alert-rule.json and alert-rule-logs.json into a Grafana stack, replacing any earlier copies. Reads these
# from deploy/grafana/.env when it exists (copy .env.example), else from the environment:
#   GRAFANA_URL          e.g. https://<stack>.grafana.net
#   GRAFANA_TOKEN        a service account token with the Admin role (Editor gets 403 on alert-rule provisioning)
#   DATASOURCE_UID       the Prometheus data source the dashboard uses
#   LOKI_DATASOURCE_UID  the Loki data source the dashboard's logs panel uses
set -euo pipefail

dir=$(cd "$(dirname "$0")" && pwd)
if [[ -f "${dir}/.env" ]]; then
	set -a
	# shellcheck source=/dev/null
	. "${dir}/.env"
	set +a
fi
: "${GRAFANA_URL:?}" "${GRAFANA_TOKEN:?}" "${DATASOURCE_UID:?}" "${LOKI_DATASOURCE_UID:?}"

# Writes the response body to $1 and prints the HTTP status without failing on it, so each caller decides which
# statuses are fine.
status() {
	local out=$1
	shift
	curl -sS -o "$out" -w '%{http_code}' -H "Authorization: Bearer ${GRAFANA_TOKEN}" \
		-H "Content-Type: application/json" -H "X-Disable-Provenance: true" "$@"
}

# Grafana's response body is the only place a rejected rule says why, so it is printed on failure.
install_rule() {
	local uid=$1 file=$2 code body
	code=$(status /dev/null -X DELETE "${GRAFANA_URL}/api/v1/provisioning/alert-rules/${uid}")
	case "$code" in 200 | 204 | 404) ;; *) echo "delete old ${uid}: HTTP ${code}" >&2 && exit 1 ;; esac
	body=$(mktemp)
	code=$(sed -e "s/\${DATASOURCE_UID}/${DATASOURCE_UID}/" -e "s/\${LOKI_DATASOURCE_UID}/${LOKI_DATASOURCE_UID}/" "${dir}/${file}" |
		status "$body" -X POST "${GRAFANA_URL}/api/v1/provisioning/alert-rules" -d @-)
	case "$code" in
	2??) rm -f "$body" ;;
	*) echo "install ${uid}: HTTP ${code}: $(cat "$body")" >&2 && rm -f "$body" && exit 1 ;;
	esac
	echo "installed ${uid}"
}

code=$(status /dev/null -X POST "${GRAFANA_URL}/api/folders" -d '{"uid":"gasometer","title":"gasometer"}')
case "$code" in 200 | 409 | 412) ;; *) echo "create folder: HTTP ${code}" >&2 && exit 1 ;; esac
install_rule gasometer-stalled alert-rule.json
install_rule gasometer-logs-absent alert-rule-logs.json
