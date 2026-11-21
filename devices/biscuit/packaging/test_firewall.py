"""Firewall contracts: stub failures plus optional isolated legacy packet checks.

Never execute the firewall in the orb's shared network namespace. All files and
locks are relocated to private scratch space, even in the isolated packet test.
"""

import ipaddress
import json
import os
from pathlib import Path
import shlex
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import unittest


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
TOOLS = ("iptables-legacy", "ip6tables-legacy", "iptables-legacy-restore",
         "ip6tables-legacy-restore")


def parse_rules(source):
    lines = source.splitlines()
    assert lines[0] == "*filter" and lines[-1] == "COMMIT"
    table = {}
    for line in lines[1:-1]:
        words = shlex.split(line)
        if line.startswith(":"):
            name, policy, counters = words
            assert counters == "[0:0]" and name[1:] not in table
            table[name[1:]] = {"policy": policy, "rules": []}
        else:
            assert words[0] == "-A" and len(words) % 2 == 0
            rule = dict(zip(words[2::2], words[3::2]))
            assert len(rule) * 2 == len(words) - 2
            assert set(rule) <= {"-i", "-p", "-d", "-s", "-j", "-m", "--state",
                                 "--dport", "--sport", "--icmp-type", "--icmpv6-type"}
            assert rule.get("-m", "state") == "state"
            table[words[1]]["rules"].append(rule)
    for chain in table.values():
        for rule in chain["rules"]:
            assert rule["-j"] in {"ACCEPT", "DROP"} | set(table)
    assert table["INPUT"]["policy"] == table["FORWARD"]["policy"] == "DROP"
    assert table["OUTPUT"]["policy"] == "ACCEPT"
    return table


def verdict(table, chain="INPUT", **packet):
    for rule in table[chain]["rules"]:
        matches = True
        for key, value in rule.items():
            if key in ("-m", "-j"):
                continue
            actual = packet.get(key, "")
            if key in ("-s", "-d"):
                match = bool(actual) and ipaddress.ip_address(actual) in ipaddress.ip_network(value)
            elif key == "--state":
                match = actual in value.split(",")
            else:
                match = str(actual) == value
            matches &= match
        if matches:
            target = rule["-j"]
            return target if target in ("ACCEPT", "DROP") else verdict(table, target, **packet)
    return table[chain]["policy"]


def stub():
    """Atomic restore model; invoked only through private tool wrappers."""
    root = Path(os.environ["FIREWALL_TEST_ROOT"])
    name = os.environ["FIREWALL_TEST_TOOL"]
    family = "6" if name.startswith("ip6") else "4"
    assert sys.argv[1:] == ["-w", "5"]
    with (root / "events").open("a") as log:
        log.write(json.dumps([os.getppid(), family]) + "\n")
    if os.environ.get("FIREWALL_TEST_DELAY"):
        time.sleep(0.03)
    source = sys.stdin.read()
    if f"{family}-restore" == os.environ.get("FIREWALL_TEST_FAIL"):
        sys.exit(1)
    path = root / (family + ".json")
    path.write_text(json.dumps(parse_rules(source)))


def packet_peer():
    """JSON RPC inside a second, disposable network namespace."""
    held = {}
    print(os.getpid(), flush=True)
    for line in sys.stdin:
        request = json.loads(line)
        family = socket.AF_INET6 if request.get("family") == 6 else socket.AF_INET
        operation = request["op"]
        try:
            if operation == "accept":
                connection, _ = held[request["key"]].accept()
                connection.sendall(b"outbound-reply")
                connection.close()
            else:
                connection = socket.socket(family, socket.SOCK_DGRAM if operation == "udp" else socket.SOCK_STREAM)
                connection.settimeout(0.35)
                if operation == "listen":
                    connection.bind((request["address"], request["port"]))
                    connection.listen(8)
                elif operation == "udp":
                    connection.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
                    connection.bind((request["source"], request["sport"]))
                    destination = (request["address"], request["port"])
                    if ipaddress.ip_address(request["address"]).is_multicast:
                        if family == socket.AF_INET:
                            connection.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_IF,
                                                  socket.inet_aton(request["source"]))
                        else:
                            index = socket.if_nametoindex("client0")
                            connection.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_MULTICAST_IF, index)
                            destination = (*destination, 0, index)
                    connection.sendto(b"discovery-or-dhcp", destination)
                else:
                    connection.connect((request["address"], request["port"]))
                if "key" in request:
                    held[request["key"]] = connection
                else:
                    connection.close()
            result = True
        except OSError:
            result = False
        print(json.dumps(result), flush=True)


def packet_scenario(script):
    # Refuse before the first network mutation if isolation did not take effect.
    assert os.readlink("/proc/self/ns/net") != os.environ["FIREWALL_PARENT_NETNS"]
    def command(*args):
        return subprocess.run(args, check=True, capture_output=True, text=True, timeout=10).stdout

    peer = subprocess.Popen(["unshare", "--net", sys.executable, __file__, "--peer"],
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    listeners = []
    try:
        pid = int(peer.stdout.readline())
        assert os.readlink(f"/proc/{pid}/ns/net") != os.readlink("/proc/self/ns/net")
        def remote(*args):
            return command("nsenter", "-t", str(pid), "-n", *args)

        def request(**args):
            peer.stdin.write(json.dumps(args) + "\n")
            peer.stdin.flush()
            return json.loads(peer.stdout.readline())

        command("ip", "link", "set", "lo", "up")
        remote("ip", "link", "set", "lo", "up")
        for interface, client, prefix4, prefix6 in (("wlan0", "client0", "192.0.2", "fd00"),
                                                   ("usb0", "clientusb0", "198.51.100", "fd01")):
            command("ip", "link", "add", interface, "type", "veth", "peer", "name", client)
            command("ip", "link", "set", client, "netns", str(pid))
            command("ip", "addr", "add", prefix4 + ".1/24", "dev", interface)
            command("ip", "-6", "addr", "add", prefix6 + "::1/64", "dev", interface, "nodad")
            command("ip", "link", "set", interface, "up")
            remote("ip", "addr", "add", prefix4 + ".2/24", "dev", client)
            remote("ip", "-6", "addr", "add", prefix6 + "::2/64", "dev", client, "nodad")
            remote("ip", "link", "set", client, "up")
        remote("ip", "route", "add", "224.0.0.0/4", "dev", "client0")
        remote("ip", "-6", "route", "add", "ff00::/8", "dev", "client0")
        for family in (socket.AF_INET, socket.AF_INET6):
            for port in (22, 6053, 6055, 6054, 8181, 8928):
                listener = socket.socket(family)
                if family == socket.AF_INET6:
                    listener.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                listener.bind(("0.0.0.0" if family == socket.AF_INET else "::", port))
                listener.listen(32)
                listener.settimeout(0.35)
                listeners.append(listener)
        def firewall(action):
            command("/bin/sh", script, action)

        firewall("apply")
        firewall("apply")
        for family, address, usb, client in ((4, "192.0.2.1", "198.51.100.1", "192.0.2.2"),
                                             (6, "fd00::1", "fd01::1", "fd00::2")):
            remote("ping", "-4" if family == 4 else "-6", "-c", "1", "-W", "1", address)
            for port in (22, 6053, 6055, 6054, 8181, 8928):
                assert request(op="connect", family=family, address=address, port=port) == (port in (6053, 6055)), (family, port)
            for port in (22, 6053, 6055):
                assert not request(op="connect", family=family, address=usb, port=port), (family, "usb", port)
            # Outbound requests and their established replies remain permitted.
            assert request(op="listen", family=family, address=client, port=12345, key=f"reply{family}")
            with socket.socket(socket.AF_INET if family == 4 else socket.AF_INET6) as outbound:
                outbound.settimeout(1)
                outbound.connect((client, 12345))
                assert request(op="accept", key=f"reply{family}")
                assert outbound.recv(32) == b"outbound-reply"
            # Receive joined groups so wrong-destination rejection is the
            # firewall's work, not the network stack discarding an unjoined group.
            with socket.socket(socket.AF_INET if family == 4 else socket.AF_INET6, socket.SOCK_DGRAM) as discovery:
                discovery.bind(("0.0.0.0" if family == 4 else "::", 5353))
                discovery.settimeout(0.35)
                correct, wrong = (("224.0.0.251", "239.1.2.3") if family == 4 else ("ff02::fb", "ff02::123"))
                for group in (correct, wrong):
                    if family == 4:
                        discovery.setsockopt(socket.IPPROTO_IP, socket.IP_ADD_MEMBERSHIP,
                                             socket.inet_aton(group) + socket.inet_aton(address))
                    else:
                        discovery.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_JOIN_GROUP,
                                             socket.inet_pton(socket.AF_INET6, group) + struct.pack("@I", socket.if_nametoindex("wlan0")))
                assert request(op="udp", family=family, source=client, sport=5353, address=correct, port=5353)
                assert discovery.recv(64) == b"discovery-or-dhcp"
                assert request(op="udp", family=family, source=client, sport=5353, address=wrong, port=5353)
                try:
                    discovery.recv(64)
                    raise AssertionError("wrong multicast destination reached mDNS")
                except socket.timeout:
                    pass
                assert request(op="udp", family=family, source=client, sport=5353, address=address, port=5353)
                assert discovery.recv(64) == b"discovery-or-dhcp"
                assert request(op="udp", family=family, source=client, sport=45000, address=address, port=5353)
                try:
                    discovery.recv(64)
                    raise AssertionError("unsolicited unicast UDP reached mDNS")
                except socket.timeout:
                    pass
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as dhcp:
            dhcp.bind(("0.0.0.0", 68))
            dhcp.settimeout(0.5)
            assert request(op="udp", source="192.0.2.2", sport=67, address="192.0.2.1", port=68)
            assert dhcp.recv(64) == b"discovery-or-dhcp"
            assert request(op="udp", source="192.0.2.2", sport=67, address="255.255.255.255", port=68)
            assert dhcp.recv(64) == b"discovery-or-dhcp"
        for tool in ("iptables-legacy", "ip6tables-legacy"):
            assert "-P FORWARD DROP" in command(tool, "-S")
        print("IPv4+IPv6: legacy restore/reapply, service boundaries, USB isolation, outbound replies, ICMP/ND, mDNS multicast/unicast and DHCPv4 unicast/broadcast packets passed")
    finally:
        for listener in listeners:
            listener.close()
        peer.stdin.close()
        peer.wait(timeout=5)


class FirewallTests(unittest.TestCase):
    def setUp(self):
        backup = ROOT / ".amp/in/orb-backup"
        backup.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=backup)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        tools = self.root / "tools"
        tools.mkdir()
        # A deliberately closed PATH: a missing stub must not invoke a host tool.
        for name in ("cat", "mkdir", "flock"):
            (tools / name).symlink_to(shutil.which(name))
        for name in TOOLS[2:]:
            path = tools / name
            path.write_text(f"#!{sys.executable}\nimport os, runpy\n"
                            f"os.environ['FIREWALL_TEST_TOOL'] = {name!r}\n"
                            f"runpy.run_path({str(Path(__file__).resolve())!r})['stub']()\n")
            path.chmod(0o755)
        self.script = self.root / "firewall"
        self.script.write_text((HERE / "firewall").read_text().replace("/run/encore", str(self.root / "run")))
        self.env = {**os.environ, "PATH": str(tools), "FIREWALL_TEST_ROOT": str(self.root)}

    def run_firewall(self, action="apply", fail="", success=True):
        result = subprocess.run(["/bin/sh", str(self.script), action], env={
            **self.env, "FIREWALL_TEST_FAIL": fail}, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode == 0, success, result.stderr)
        return result

    def tables(self):
        return [json.loads((self.root / (family + ".json")).read_text()) for family in ("4", "6")]

    def test_services_and_default_boundaries(self):
        self.run_firewall()
        for table in self.tables():
            self.assertEqual(verdict(table, "FORWARD"), "DROP")
            self.assertEqual(verdict(table, "OUTPUT"), "ACCEPT")
            self.assertEqual(verdict(table, **{"-i": "lo"}), "ACCEPT")
            self.assertEqual(verdict(table, **{"--state": "ESTABLISHED"}), "ACCEPT")
            self.assertEqual(verdict(table, **{"--state": "RELATED"}), "ACCEPT")
            for port in (22, 6053, 6055, 6054, 8181, 8928, 5000, 7000, 4070, 80, 443):
                for interface in ("wlan0", "usb0"):
                    result = verdict(table, **{"-i": interface, "-p": "tcp", "--dport": port})
                    self.assertEqual(result, "ACCEPT" if interface == "wlan0" and port in (6053, 6055) else "DROP")
            for port in (6053, 6055, 1900, 319, 320, 6001):
                self.assertEqual(verdict(table, **{"-i": "wlan0", "-p": "udp", "--dport": port}), "DROP")

    def test_discovery_dhcp_and_icmp(self):
        self.run_firewall()
        for family, table in zip((4, 6), self.tables()):
            multicast = "224.0.0.251" if family == 4 else "ff02::fb"
            unicast = "192.0.2.2" if family == 4 else "2001:db8::2"
            wrong = "239.1.2.3" if family == 4 else "ff02::1"
            udp = {"-i": "wlan0", "-p": "udp", "--dport": 5353, "--sport": 5353}
            for destination, source_port, expected in ((multicast, 5353, "ACCEPT"),
                    (multicast, 45000, "ACCEPT"), (unicast, 5353, "ACCEPT"),
                    (unicast, 45000, "DROP"), (wrong, 5353, "DROP")):
                self.assertEqual(verdict(table, **{**udp, "-d": destination, "--sport": source_port}), expected)
            self.assertEqual(verdict(table, **{**udp, "-i": "usb0", "-d": multicast}), "DROP")
            dhcp = {"-i": "wlan0", "-p": "udp", "-s": "192.0.2.1" if family == 4 else "fe80::1",
                    "--sport": 67 if family == 4 else 547, "--dport": 68 if family == 4 else 546}
            self.assertEqual(verdict(table, **dhcp), "ACCEPT")
            self.assertEqual(verdict(table, **{**dhcp, "--sport": 9999}), "DROP")
            protocol, option = ("icmp", "--icmp-type") if family == 4 else ("ipv6-icmp", "--icmpv6-type")
            allowed = (0, 3, 8, 11, 12) if family == 4 else (1, 2, 3, 4, 128, 129, 130, 134, 135, 136)
            for kind in allowed:
                self.assertEqual(verdict(table, **{"-i": "wlan0", "-p": protocol, "-s": dhcp["-s"], option: kind}), "ACCEPT")
            self.assertEqual(verdict(table, **{"-i": "wlan0", "-p": protocol, option: 137}), "DROP")

    def test_repeated_apply(self):
        self.run_firewall()
        original = self.tables()
        self.run_firewall()
        self.assertEqual(self.tables(), original)

    def test_busybox_shell_apply(self):
        busybox = shutil.which("busybox")
        if not busybox:
            self.skipTest("BusyBox shell not installed")
        subprocess.run([busybox, "sh", str(self.script), "apply"], env=self.env, check=True, timeout=10)
        self.assertEqual(len(self.tables()), 2)

    def test_failure_still_attempts_both_families(self):
        for fail, other in ((4, 6), (6, 4)):
            with self.subTest(family=fail):
                (self.root / f"{other}.json").unlink(missing_ok=True)
                result = self.run_firewall(fail=f"{fail}-restore", success=False)
                self.assertIn("policy install failed", result.stderr)
                self.assertEqual(json.loads((self.root / f"{other}.json").read_text())["INPUT"]["policy"], "DROP")

    def test_missing_required_tools_and_invalid_command(self):
        self.assertEqual(self.run_firewall("ssh-open", success=False).returncode, 2)
        (self.root / "tools/ip6tables-legacy-restore").unlink()
        self.run_firewall(success=False)
        self.assertFalse((self.root / "events").exists())

    def test_updates_are_serialized(self):
        processes = [subprocess.Popen(["/bin/sh", str(self.script), "apply"], env={
            **self.env, "FIREWALL_TEST_DELAY": "1"}, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            for _ in range(4)]
        for process in processes:
            _, stderr = process.communicate(timeout=15)
            self.assertEqual(process.returncode, 0, stderr)
        owners = [json.loads(line)[0] for line in (self.root / "events").read_text().splitlines()]
        groups = [owner for index, owner in enumerate(owners) if not index or owners[index - 1] != owner]
        self.assertEqual(len(groups), 4)
        self.assertEqual(len(set(groups)), 4)

    def test_boot_failure_prevents_links_radio_and_ready(self):
        source = (HERE / "boot.sh").read_text()
        firewall = source.index("/usr/local/sbin/encore-firewall apply")
        for marker in ("ip link", "/usr/local/bin/wmtup", "wpa_supplicant -B", "udhcpc -b", "touch /run/encore/base-ready"):
            self.assertLess(firewall, source.index(marker))
        # Execute the real boot control flow, replacing all privileged commands.
        source = source.replace("export PATH=", "# export PATH=")
        source = source.replace("BB=/bin/busybox.static", "BB=:")
        source = source.replace("/run/encore", str(self.root / "run"))
        source = source.replace("/usr/local/sbin/encore-firewall apply", "/bin/sh -c 'exit 71'")
        source = source.replace("ip link set lo up", "echo UNSAFE_LINK")
        source = source.replace("/usr/local/bin/wmtup", "echo UNSAFE_RADIO")
        (self.root / "run").mkdir()
        (self.root / "run/slot").write_text("a\n")
        for name in ("mount", "mkdir", "chmod"):
            path = self.root / "tools" / name
            path.unlink(missing_ok=True)
            path.write_text("#!/bin/sh\nexit 0\n")
            path.chmod(0o755)
        script = self.root / "boot"
        script.write_text(source)
        result = subprocess.run(["/bin/sh", str(script)], env=self.env, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 71, result.stderr)
        self.assertNotIn("UNSAFE", result.stdout)
        self.assertFalse((self.root / "run/base-ready").exists())

    def test_real_packets_in_disposable_namespaces(self):
        env = {**os.environ, "PATH": "/usr/sbin:/sbin:/usr/bin:/bin",
               "XTABLES_LOCKFILE": str(self.root / "xtables.lock"),
               "FIREWALL_PARENT_NETNS": os.readlink("/proc/self/ns/net")}
        for tool in (*TOOLS, "unshare", "nsenter", "ip", "ping"):
            if not shutil.which(tool, path=env["PATH"]):
                self.skipTest(f"packet test requires {tool}")
        probe = subprocess.run(["unshare", "--user", "--map-root-user", "--net", "/bin/sh", "-c",
                                "iptables-legacy -S && ip6tables-legacy -S"],
                               env=env, capture_output=True, text=True, timeout=10)
        if probe.returncode:
            self.skipTest("isolated legacy netfilter unavailable: " + probe.stderr.strip())
        result = subprocess.run(["unshare", "--user", "--map-root-user", "--net", sys.executable,
                                 str(Path(__file__).resolve()), "--packets", str(self.script)],
                                env=env, capture_output=True, text=True, timeout=45)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    if sys.argv[1:2] == ["--peer"]:
        packet_peer()
    elif sys.argv[1:2] == ["--packets"]:
        packet_scenario(sys.argv[2])
    else:
        unittest.main()
