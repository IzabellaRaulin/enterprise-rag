# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import os
import socket
import stat
from concurrent.futures import ProcessPoolExecutor

from comps.cores.mega.logger import change_erag_logger_level, get_erag_logger

logger = get_erag_logger("process_pool")
change_erag_logger_level(logger, log_level=os.getenv("ERAG_LOGGER_LEVEL", "INFO"))


def close_inherited_sockets():
    """Pool initializer: fork workers inherit the server's TCP sockets and keep the connections
    it closes open. Point each at /dev/null rather than close it, so its fd number stays taken."""
    try:
        fds = [int(fd) for fd in os.listdir("/proc/self/fd")]
    except OSError as e:  # no /proc
        logger.warning(f"Worker {os.getpid()} keeps the server's sockets: cannot list /proc/self/fd: {e}")
        return
    devnull = os.open(os.devnull, os.O_RDWR)
    try:
        for fd in fds:
            if fd == devnull:
                continue
            try:
                if not stat.S_ISSOCK(os.fstat(fd).st_mode):
                    logger.debug(f"Worker {os.getpid()}: kept fd {fd}, not a socket")
                    continue
                s = socket.socket(fileno=fd)
                family = s.family
                s.detach()
                if family in (socket.AF_INET, socket.AF_INET6):
                    os.dup2(devnull, fd)
                    logger.debug(f"Worker {os.getpid()}: dropped inherited TCP socket fd {fd}")
                else:
                    logger.debug(f"Worker {os.getpid()}: kept socket fd {fd}, family {family!r}")
            except OSError as e:  # closed meanwhile (the listdir fd itself)
                logger.debug(f"Worker {os.getpid()}: skipped fd {fd}: {e}")
                continue
    finally:
        os.close(devnull)


def new_process_pool(max_workers):
    """A ProcessPoolExecutor whose workers do not hold the server's connections."""
    return ProcessPoolExecutor(max_workers=max_workers, initializer=close_inherited_sockets)


def replace_if_broken(current, broken, max_workers):
    """The pool to use after `broken` raised BrokenProcessPool: a new one if `broken` is still
    `current`, otherwise `current` (another task already replaced it)."""
    if current is not broken:
        return current
    broken.shutdown(wait=False)
    return new_process_pool(max_workers)
