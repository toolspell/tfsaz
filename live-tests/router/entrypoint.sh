#!/bin/sh
# WireGuard router for live-test VMs. Each VM is a peer with one address in
# the upper half of the compose subnet. Proxy ARP (set by compose) makes
# those addresses reachable from the containers; traffic to anywhere else
# leaves through Docker's NAT. The key and the peer list live in /state, so
# VMs keep working when the router restarts.
set -eu

mkdir -p /state/peers
if [ ! -s /state/private.key ]; then
	(umask 077 && wg genkey >/state/private.key)
fi
wg pubkey </state/private.key >/state/public.key

ip link add wg0 type wireguard
wg set wg0 listen-port 51820 private-key /state/private.key
ip link set wg0 up
peer load

# The host's loopback publication does not restrict direct access from VMs.
iptables -A FORWARD -i wg0 -d "${MOCK_IP:?}" -p tcp --dport 19090 -j REJECT

# Internet traffic leaves with the router's address, so Docker's NAT accepts it.
iptables -t nat -A POSTROUTING -s "${VM_SUBNET:?}" ! -d "${BED_SUBNET:?}" -o eth0 -j MASQUERADE

exec dnsmasq --keep-in-foreground --conf-file=/etc/router/dnsmasq.conf --server="/mock.test/${DNS_IP:?}"
