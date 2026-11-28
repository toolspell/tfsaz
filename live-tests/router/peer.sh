#!/bin/sh
# Manage VM peers of the router:
#   peer add NAME PUBLIC_KEY ADDRESS  (exit 3: another peer has ADDRESS)
#   peer remove NAME
#   peer load        (re-apply every saved peer, used on start)
#   peer list
set -eu

dir=/state/peers

# apply leaves nothing behind when it fails. It checks each step itself,
# because set -e does not apply when load calls it with ||.
apply() {
	wg set wg0 peer "$1" allowed-ips "$2/32" || return 1
	if ! ip route replace "$2/32" dev wg0; then
		wg set wg0 peer "$1" remove
		return 1
	fi
}

case "${1:-}" in
add)
	[ $# -eq 4 ] || { echo "usage: peer add NAME PUBLIC_KEY ADDRESS" >&2; exit 2; }
	# One add at a time, so two VMs never get the same address.
	exec 9>"$dir/.lock"
	flock 9
	for file in "$dir"/*; do
		[ -f "$file" ] && [ "$(basename "$file")" != "$2" ] || continue
		if read -r _ other <"$file" && [ "$other" = "$4" ]; then
			echo "peer: $4 is in use by $(basename "$file")" >&2
			exit 3
		fi
	done
	apply "$3" "$4"
	# A record appears only complete; a failed write leaves none.
	if ! { printf '%s %s\n' "$3" "$4" >"$dir/.$2" && mv "$dir/.$2" "$dir/$2"; }; then
		rm -f "$dir/.$2"
		wg set wg0 peer "$3" remove
		ip route del "$4/32" dev wg0 2>/dev/null || true
		exit 1
	fi
	;;
remove)
	[ $# -eq 2 ] || { echo "usage: peer remove NAME" >&2; exit 2; }
	exec 9>"$dir/.lock"
	flock 9
	[ -f "$dir/$2" ] || exit 0
	if read -r key address <"$dir/$2" && [ -n "$key" ]; then
		wg set wg0 peer "$key" remove
		ip route del "$address/32" dev wg0 2>/dev/null || true
	fi
	rm -f "$dir/$2"
	;;
load)
	for file in "$dir"/*; do
		[ -f "$file" ] || continue
		if read -r key address <"$file" && [ -n "$key" ] && apply "$key" "$address"; then
			continue
		fi
		echo "peer: skipped $(basename "$file"): cannot apply it" >&2
	done
	;;
list)
	for file in "$dir"/*; do
		[ -f "$file" ] || continue
		printf '%s %s\n' "$(basename "$file")" "$(cat "$file")"
	done
	;;
*)
	echo "usage: peer add|remove|load|list" >&2
	exit 2
	;;
esac
