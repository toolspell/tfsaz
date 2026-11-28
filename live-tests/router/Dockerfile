FROM alpine:3.22
RUN apk add --no-cache wireguard-tools iptables dnsmasq
COPY entrypoint.sh peer.sh dnsmasq.conf /etc/router/
RUN chmod 755 /etc/router/entrypoint.sh /etc/router/peer.sh \
 && ln -s /etc/router/peer.sh /usr/local/bin/peer
EXPOSE 51820/udp
ENTRYPOINT ["/etc/router/entrypoint.sh"]
