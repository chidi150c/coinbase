#!/bin/bash
# Prune bot_state backups older than 30 days

BACKUP_DIR="/opt/coinbase/state/backup"
LOG_FILE="$HOME/prune_old_state.log"

echo "---- $(date '+%Y-%m-%d %H:%M:%S') Starting prune ----" >> "$LOG_FILE"

find "$BACKUP_DIR" -type f -name 'bot_state.*.gz' -mtime +14 -print -delete >> "$LOG_FILE" 2>&1

echo "---- $(date '+%Y-%m-%d %H:%M:%S') Prune complete ----" >> "$LOG_FILE"
