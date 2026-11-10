#!/bin/bash
# Sourced by the packaging and verification scripts. Stapling and validation
# are idempotent, unlike a notary submission with an unknown upload result.
stapler_with_retry() {
  if [[ $# != 2 || ( "$1" != staple && "$1" != validate ) ]]; then
    echo 'usage: stapler_with_retry staple|validate ARTIFACT' >&2
    return 2
  fi
  local action=$1 artifact=$2 attempt=1 delay=5 output status
  local transient='NSURLErrorDomain Code=-(1001|1003|1004|1005|1006|1009)([^0-9]|$)|HTTP( status( code)?)?[ :]+(429|500|502|503|504)([^0-9]|$)'
  while true; do
    if output=$(xcrun stapler "$action" "$artifact" 2>&1); then
      printf '%s\n' "$output"
      return 0
    else
      status=$?
    fi
    printf '%s\n' "$output" >&2
    # Exit code 68 alone is NOT proof of a transient error. Missing, invalid or
    # mismatched tickets and unclassified failures must still fail the build.
    if [[ "$attempt" -ge 4 || ! "$output" =~ $transient ]]; then
      echo "::error::stapler $action failed (exit $status, attempt $attempt/4)." >&2
      return "$status"
    fi
    echo "::warning::stapler $action hit a transient Apple network error; retrying in ${delay}s (attempt $attempt/4)." >&2
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
}
