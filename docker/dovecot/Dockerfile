FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends dovecot-core dovecot-imapd openssl \
	&& rm -rf /var/lib/apt/lists/*

# Shared mail-store identity: both this container and the postfix container
# read and write the mail volume as uid/gid 5000.
RUN groupadd -g 5000 vmail && useradd -u 5000 -g 5000 -d /var/mail -s /usr/sbin/nologin vmail

COPY dovecot.conf /etc/dovecot/dovecot.conf
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh \
	&& printf 'test@posthaste.local:{SHA512-CRYPT}%s\n' "$(openssl passwd -6 posthaste)" > /etc/dovecot/users \
	&& chown root:dovecot /etc/dovecot/users && chmod 640 /etc/dovecot/users

EXPOSE 143

ENTRYPOINT ["/entrypoint.sh"]
CMD ["dovecot", "-F"]
