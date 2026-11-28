FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
LABEL org.opencontainers.image.source=https://github.com/toolspell/tfsaz \
      org.opencontainers.image.description="Test image for TFSaz live tests, not for production"
# Change the date to refresh the packages: it changes the image key.
ARG REFRESH=2026-11-28
RUN apk add --no-cache wireguard-tools iptables dnsmasq
COPY entrypoint.sh peer.sh dnsmasq.conf /etc/router/
RUN chmod 755 /etc/router/entrypoint.sh /etc/router/peer.sh \
 && ln -s /etc/router/peer.sh /usr/local/bin/peer
EXPOSE 51820/udp
ENTRYPOINT ["/etc/router/entrypoint.sh"]
ARG TFSAZ_LIVE_INPUTS
LABEL org.toolspell.tfsaz-live.key=$TFSAZ_LIVE_INPUTS
