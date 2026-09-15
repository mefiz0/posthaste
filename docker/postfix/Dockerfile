FROM debian:bookworm-slim

RUN DEBIAN_FRONTEND=noninteractive apt-get update \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends postfix \
	&& rm -rf /var/lib/apt/lists/*

# Shared mail-store identity: both this container and the dovecot container
# read and write the mail volume as uid/gid 5000.
RUN groupadd -g 5000 vmail && useradd -u 5000 -g 5000 -d /var/mail -s /usr/sbin/nologin vmail

COPY main.cf /etc/postfix/main.cf
COPY vmailbox /etc/postfix/vmailbox
RUN postmap /etc/postfix/vmailbox

EXPOSE 25

CMD ["postfix", "start-fg"]
