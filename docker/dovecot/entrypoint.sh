#!/bin/sh
# Prepare the shared mail store and keep the server in the foreground.
set -e

install -d -o vmail -g vmail /var/mail /var/mail/home

# Pre-create the test user's INBOX Maildir so IMAP works before any delivery.
inbox="/var/mail/test@posthaste.local"
if [ ! -d "$inbox/cur" ]; then
	mkdir -p "$inbox/cur" "$inbox/new" "$inbox/tmp"
	chown -R vmail:vmail "$inbox"
fi

exec "$@"
