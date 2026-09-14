#!/bin/sh
# Sourced by local smoke checks. Secrets travel on stdin; cookies stay in a
# private temporary file and are never printed or passed as command arguments.
auth_init() {
  : "${ROUTEFORGE_ADMIN_SECRET:?Export the configured local admin secret for smoke verification}"
  umask 077
  auth_cookie_jar="$(mktemp)"
  auth_base="$1"
  trap 'auth_cleanup' EXIT
  auth_login
}

auth_curl() {
  command curl --cookie "$auth_cookie_jar" "$@"
}

auth_login() {
  jq -n '{secret: env.ROUTEFORGE_ADMIN_SECRET}' |
    command curl --fail --silent --show-error --max-time 5 --cookie "$auth_cookie_jar" --cookie-jar "$auth_cookie_jar" \
      -H 'Origin: http://127.0.0.1:3001' -H 'Content-Type: application/json' \
      --data-binary @- "$auth_base/auth/login" >/dev/null
}

auth_cleanup() {
  auth_curl --silent --max-time 3 -H 'Origin: http://127.0.0.1:3001' -H 'Content-Type: application/json' \
    --data '{}' "$auth_base/auth/logout" >/dev/null 2>&1 || true
  rm -f -- "$auth_cookie_jar"
}
