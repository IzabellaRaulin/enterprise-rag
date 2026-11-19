#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""
Install a pinned helm into a tox env's bin directory, for the chart tests
(test_edp_extractor_hpa.py). The CI image has no helm.

Usage: python tests/unit/scripts/install_helm.py <bin dir>
"""

import hashlib
import io
import os
import shutil
import sys
import tarfile
import urllib.request

VERSION = "v3.19.0"
ARCHIVE = f"helm-{VERSION}-linux-amd64.tar.gz"
SHA256 = "a7f81ce08007091b86d8bd696eb4d86b8d0f2e1b9f6c714be62f82f96a594496"


def main(bin_dir):
    target = os.path.join(bin_dir, "helm")
    if shutil.which("helm") or os.path.exists(target):
        return
    with urllib.request.urlopen(f"https://get.helm.sh/{ARCHIVE}", timeout=120) as resp:
        data = resp.read()
    digest = hashlib.sha256(data).hexdigest()
    if digest != SHA256:
        sys.exit(f"{ARCHIVE}: sha256 {digest}, expected {SHA256}")
    with tarfile.open(fileobj=io.BytesIO(data)) as tar:
        binary = tar.extractfile("linux-amd64/helm").read()
    with open(target, "wb") as f:
        f.write(binary)
    os.chmod(target, 0o755)


if __name__ == "__main__":
    main(sys.argv[1])
