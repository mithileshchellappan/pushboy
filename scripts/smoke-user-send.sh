#!/usr/bin/env bash
set -euo pipefail

api=${1:?Pass the disposable server /v1 URL}
tenant=(-H "Authorization: Bearer ${PUSHBOY_SMOKE_KEY:?Set the first tenant backend key}")
other=(-H "Authorization: Bearer ${PUSHBOY_SMOKE_OTHER_KEY:?Set the second tenant backend key}")

uid=$(curl -fsS "${tenant[@]}" --json '{}' "$api/users" | jq -er 'select(.kind == "anonymous") | .uid | strings | select(length > 0)')
curl -fsS "${tenant[@]}" "$api/users/$uid" | jq -e --arg uid "$uid" '.uid == $uid and .kind == "anonymous"' >/dev/null

payload='{"title":"Pushboy HTTP smoke check"}'
job=$(curl -fsS "${tenant[@]}" --json "$payload" "$api/users/$uid/send" | jq -er 'select(.status == "queued") | .job_id | strings | select(length > 0)')
for ((attempt=0; attempt<30; attempt++)); do
  state=$(curl -fsS "${tenant[@]}" "$api/notifications/$job")
  if jq -e '.Status == "COMPLETED"' <<<"$state" >/dev/null; then
    break
  fi
  sleep 1
done
jq -e --arg uid "$uid" '.UserID == $uid and .Status == "COMPLETED" and .TotalCount == 0' <<<"$state" >/dev/null
curl -fsS "${tenant[@]}" "$api/notifications/$job/receipts" | jq -e '.items == [] and .has_more == false' >/dev/null

for path in "users/$uid" "notifications/$job" "notifications/$job/receipts"; do
  status=$(curl -sS -o /dev/null -w '%{http_code}' "${other[@]}" "$api/$path")
  if [[ $status != 404 ]]; then
    echo "Second tenant received HTTP $status for GET $path (expected 404)" >&2
    exit 1
  fi
done
status=$(curl -sS -o /dev/null -w '%{http_code}' "${other[@]}" --json "$payload" "$api/users/$uid/send")
if [[ $status != 404 ]]; then
  echo "Second tenant received HTTP $status for POST users/$uid/send (expected 404)" >&2
  exit 1
fi
status=$(curl -sS -o /dev/null -w '%{http_code}' "${tenant[@]}" -X DELETE "$api/users/$uid")
if [[ $status != 409 ]]; then
  echo "Deleting a user with job history returned HTTP $status (expected 409)" >&2
  exit 1
fi
echo "Created and read user $uid, completed job $job with zero devices, and verified second-tenant isolation."
