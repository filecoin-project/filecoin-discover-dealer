#!/bin/bash

set -e
set -o pipefail

psql -At service=discover -c "
  SELECT client FROM clients WHERE non_activated_datacap > 128 * 1024 * 1024 ORDER BY entry_id
" \
| "$( dirname "${BASH_SOURCE[0]}" )/atomic_cat.bash" "$HOME/WEB/public/clients.txt"
