# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import os
import socket
import stat

from comps.cores.utils.process_pool import new_process_pool, replace_if_broken


def _open_sockets():
    families = []
    for fd in os.listdir("/proc/self/fd"):
        try:
            if stat.S_ISSOCK(os.fstat(int(fd)).st_mode):
                s = socket.socket(fileno=int(fd))
                families.append(s.family)
                s.detach()
        except OSError:
            pass
    return families


def test_workers_drop_the_servers_tcp_sockets():
    listener = socket.create_server(("127.0.0.1", 0))
    client = socket.create_connection(listener.getsockname())
    accepted, _ = listener.accept()
    try:
        with new_process_pool(1) as pool:
            families = pool.submit(_open_sockets).result(timeout=30)
        assert socket.AF_INET not in families and socket.AF_INET6 not in families
        # Only the parent holds the accepted connection now, so closing it reaches the peer.
        accepted.close()
        client.settimeout(5)
        assert client.recv(1) == b""
    finally:
        for s in (client, accepted, listener):
            s.close()


def test_workers_can_open_their_own_connections():
    listener = socket.create_server(("127.0.0.1", 0))
    try:
        with new_process_pool(1) as pool:
            assert pool.submit(_connect, listener.getsockname()).result(timeout=30)
    finally:
        listener.close()


def _connect(address):
    with socket.create_connection(address):
        return True


def test_a_broken_pool_is_replaced_once():
    broken = new_process_pool(1)
    replacement = replace_if_broken(broken, broken, 1)
    try:
        assert replacement is not broken
        # A second task that saw the same broken pool keeps the replacement.
        assert replace_if_broken(replacement, broken, 1) is replacement
        assert replacement.submit(os.getpid).result(timeout=30) != os.getpid()
    finally:
        replacement.shutdown()
