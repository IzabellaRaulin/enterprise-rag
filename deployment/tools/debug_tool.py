#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""
Intel® AI for Enterprise RAG Debug Tool
Collects comprehensive diagnostic information from a Kubernetes cluster including pod logs,
resource descriptions, version info, and deployment configuration (with sensitive data redacted).
"""

import subprocess # nosec B404
import shlex
import os
import datetime
import glob
import tarfile
import json
import re
import argparse
import logging
import sys
import shutil
import yaml


class EnterpriseRAGDebugger:
    # Line width for error/warning messages
    LINE_WIDTH = 60

    # Resource types collected in kubectl_get, kubectl_getyaml and kubectl_describe
    KUBECTL_RESOURCES = [
        # Workloads (explicit instead of 'all')
        "pods", "daemonsets", "deployments", "replicasets", "statefulsets", "jobs", "cronjobs",
        # Networking
        "services", "ingress", "networkpolicies",
        # Storage
        "pv", "pvc", "sc",
        # Config & access
        "sa", "cm",
        # Cluster
        "nodes", "namespaces", "events", "noderesourcetopology",
        # Trident
        "tridentbackendconfigs",
    ]

    # Resources collected only via 'kubectl get' (no yaml/describe) due to sensitive data
    SENSITIVE_RESOURCES = ["secret"]

    # Keys considered sensitive - values will be redacted
    SENSITIVE_KEYS = re.compile(
        r'(token|password|passwd|secret|key|credential|auth|api_key|apikey|access_key|private_key)',
        re.IGNORECASE
    )

    # Keys that match SENSITIVE_KEYS pattern but should NOT be redacted
    SENSITIVE_KEYS_EXCLUSIONS = re.compile(
        r'\b(keycloak|maxNewTokens|vault_password_file|auth_provider)\b',
        re.IGNORECASE
    )

    # 'KEY: value' line in kubectl describe output (groups: indent, key, separator, value)
    _KV_LINE = re.compile(r'^(\s*)([A-Za-z0-9_][A-Za-z0-9_./-]*):(\s+)\S.*$')

    def __init__(self, output_dir="debug_bundle", config_dir=None):
        self.timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
        self.base_path = f"{output_dir}_{self.timestamp}"
        self.logs_path = os.path.join(self.base_path, "kubectl_logs")
        self.kubectl_get_path = os.path.join(self.base_path, "kubectl_get")
        self.kubectl_getyaml_path = os.path.join(self.base_path, "kubectl_getyaml")
        self.kubectl_describe_path = os.path.join(self.base_path, "kubectl_describe")
        self.config_dir = config_dir
        self.config_data = None
        self.kubeconfig_path = None
        self.logger = None

    def setup_logging(self):
        """Setup logging to both console and file."""
        # Create base directory first
        os.makedirs(self.base_path, exist_ok=True)

        # Setup logger
        self.logger = logging.getLogger("EnterpriseRAGDebugger")
        self.logger.setLevel(logging.DEBUG)

        # Console handler with custom formatter
        console_handler = logging.StreamHandler(sys.stdout)
        console_handler.setLevel(logging.INFO)
        console_formatter = logging.Formatter('%(message)s')
        console_handler.setFormatter(console_formatter)

        # File handler - save script output
        log_file = os.path.join(self.base_path, "debug_tool_execution.log")
        file_handler = logging.FileHandler(log_file)
        file_handler.setLevel(logging.DEBUG)
        file_formatter = logging.Formatter('%(asctime)s - %(levelname)s - %(message)s')
        file_handler.setFormatter(file_formatter)

        # Add handlers
        self.logger.addHandler(console_handler)
        self.logger.addHandler(file_handler)

        self.logger.info(f"[*] Logging initialized. Output saved to: {log_file}")

    def run_command(self, cmd, ignore_errors=False):
        """Helper to execute a command (given as an argument list) and return output."""
        try:
            # Set up environment with kubeconfig if available
            env = os.environ.copy()
            if self.kubeconfig_path:
                env["KUBECONFIG"] = self.kubeconfig_path

            result = subprocess.run(
                cmd, check=True, capture_output=True, text=True, env=env
            )
            return result.stdout.strip()
        except subprocess.CalledProcessError as e:
            if ignore_errors:
                self.logger.debug(f"  [!] Command failed (ignored): {shlex.join(cmd)} -> {e.stderr.strip()}")
                return None
            self.logger.error(f"  [!] Command failed: {shlex.join(cmd)}")
            self.logger.error(f"  [!] Error: {e.stderr.strip()}")
            return None

    def _log_error(self, message):
        """Log an error message surrounded by separator lines."""
        self.logger.error("=" * self.LINE_WIDTH)
        self.logger.error(message)
        self.logger.error("=" * self.LINE_WIDTH)

    def _log_section(self, message):
        """Log a message surrounded by separator lines."""
        self.logger.info("=" * self.LINE_WIDTH)
        self.logger.info(message)
        self.logger.info("=" * self.LINE_WIDTH)

    def _load_config_file(self, config_path):
        """Load and parse a YAML config file.
        
        Args:
            config_path: Path to the config file (can be relative or absolute)
            
        Returns:
            Parsed config data as dict, or None if file doesn't exist
            
        Raises:
            SystemExit: If file doesn't exist or is invalid YAML
        """
        config_path = os.path.abspath(config_path)

        if not os.path.exists(config_path):
            self._log_error(f"  [!] CONFIG ERROR: Config file not found: {config_path}\n  [!] Provide a valid path via --config-dir /path/to/env/local/")
            sys.exit(1)

        try:
            with open(config_path, "r") as f:
                config_data = yaml.safe_load(f)

            if not config_data or not isinstance(config_data, dict):
                self._log_error("  [!] CONFIG ERROR: Config file is empty, invalid YAML, or not a valid dictionary")
                sys.exit(1)

            return config_data

        except yaml.YAMLError as e:
            self._log_error(f"  [!] CONFIG ERROR: Failed to parse config file as YAML: {e}")
            sys.exit(1)
        except Exception as e:
            self._log_error(f"  [!] CONFIG ERROR: Unexpected error reading config file: {e}")
            sys.exit(1)

    def collect_config(self):
        """Load config files from config-dir, extract kubeconfig, and save redacted configs."""
        config_dir = os.path.abspath(self.config_dir)
        self.logger.info(f"[*] Loading config directory: {config_dir}")

        if not os.path.isdir(config_dir):
            self._log_error(f"  [!] CONFIG ERROR: Directory not found: {config_dir}")
            sys.exit(1)

        # Locate kubeconfig.yaml in the config directory
        kubeconfig = os.path.join(config_dir, "kubeconfig.yaml")
        if not os.path.exists(kubeconfig):
            self._log_error(
                f"  [!] CONFIG ERROR: kubeconfig.yaml not found in {config_dir}\n"
                "  [!] The config directory must contain kubeconfig.yaml"
            )
            sys.exit(1)

        self.kubeconfig_path = kubeconfig
        self.logger.info(f"  [+] kubeconfig resolved to: {self.kubeconfig_path}")

        # Load and save redacted copies of each config file
        config_files = ["global_config.yaml", "config.erag.yaml", "config.inference.yaml"]
        for filename in config_files:
            filepath = os.path.join(config_dir, filename)
            if not os.path.exists(filepath):
                self.logger.warning(f"  [!] Config file not found: {filename}")
                continue

            try:
                data = self._load_config_file(filepath)
                if data:
                    redacted = self._redact_sensitive(data)
                    out_file = os.path.join(self.base_path, f"{filename.replace('.yaml', '_redacted.yaml')}")
                    with open(out_file, "w") as f:
                        yaml.dump(redacted, f, default_flow_style=False, sort_keys=False)
                    self.logger.info(f"  [+] Saved {filename} (redacted)")
            except Exception as e:
                self.logger.error(f"  [!] Unexpected error processing {filename}: {e}")

    def setup_workspace(self):
        """Create directory structure for the diagnostic bundle."""
        os.makedirs(self.logs_path, exist_ok=True)
        os.makedirs(self.kubectl_get_path, exist_ok=True)
        os.makedirs(self.kubectl_getyaml_path, exist_ok=True)
        os.makedirs(self.kubectl_describe_path, exist_ok=True)
        self.logger.info(f"[*] Workspace created: {self.base_path}")

    def get_all_namespaces(self):
        """Get list of all namespaces in the cluster."""
        cmd = ["kubectl", "get", "namespaces", "-o", "jsonpath={.items[*].metadata.name}"]
        output = self.run_command(cmd)
        return output.split() if output else []

    def get_nodes(self):
        """Get list of all node names in the cluster."""
        cmd = ["kubectl", "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}"]
        output = self.run_command(cmd)
        return output.split() if output else []

    def get_pods(self, namespace=None):
        """Get list of all pod names, optionally filtered by namespace."""
        if namespace:
            cmd = ["kubectl", "get", "pods", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}"]
        else:
            cmd = ["kubectl", "get", "pods", "--all-namespaces", "-o", "json"]
            result = self.run_command(cmd)
            if result:
                data = json.loads(result)
                return [(item['metadata']['namespace'], item['metadata']['name']) for item in data['items']]
            return []

        output = self.run_command(cmd)
        return [(namespace, pod) for pod in output.split()] if output else []

    def collect_pod_logs(self):
        """Collect current and previous logs for all pods across all namespaces."""
        self.logger.info("[*] Collecting pod logs from all namespaces...")
        pods = self.get_pods()

        for namespace, pod in pods:
            ns_log_path = os.path.join(self.logs_path, namespace)
            os.makedirs(ns_log_path, exist_ok=True)

            # Collecting current logs
            self._save_pod_logs(namespace, pod, ns_log_path, is_previous=False)

            # AC: Restart logs and crash loop backoff (collecting previous logs)
            self._save_pod_logs(namespace, pod, ns_log_path, is_previous=True)

    def _save_pod_logs(self, namespace, pod_name, log_path, is_previous=False):
        suffix = "previous" if is_previous else "current"

        cmd = ["kubectl", "logs", pod_name, "-n", namespace, "--all-containers"]
        if is_previous:
            cmd.append("--previous")

        try:
            # Set up environment with kubeconfig if available
            env = os.environ.copy()
            if self.kubeconfig_path:
                env["KUBECONFIG"] = self.kubeconfig_path

            process = subprocess.run(cmd, capture_output=True, env=env)
            if process.returncode == 0 and len(process.stdout) > 0:
                filename = f"{pod_name}_{suffix}.log"
                full_path = os.path.join(log_path, filename)

                with open(full_path, "wb") as f:
                    f.write(process.stdout)

                log_size = len(process.stdout)
                self.logger.info(f"  [+] Saved {namespace}/{filename} ({log_size / 1024:.2f} KB)")
            elif process.returncode != 0:
                if is_previous:
                    # Expected - pod may have never been restarted
                    pass
                else:
                    self.logger.warning(f"  [!] Failed to collect current logs for {namespace}/{pod_name}: {process.stderr.decode().strip()}")
        except Exception as e:
            self.logger.error(f"  [!] Unexpected error collecting logs for {namespace}/{pod_name}: {e}")

    def collect_kubectl_get(self):
        """Collect 'kubectl get -o wide' output for each resource type into separate files."""
        self.logger.info("[*] Collecting kubectl get (table format) per resource type...")
        all_resources = self.KUBECTL_RESOURCES + self.SENSITIVE_RESOURCES

        for resource in all_resources:
            output = self.run_command(["kubectl", "get", resource, "-A", "-o", "wide"], ignore_errors=True)
            if output:
                resource_file = os.path.join(self.kubectl_get_path, f"{resource}.txt")
                with open(resource_file, "w") as f:
                    f.write(output)
                self.logger.info(f"  [+] Saved kubectl get {resource}")
            else:
                self.logger.warning(f"  [!] No output for kubectl get {resource} - skipping")

    def collect_kubectl_getyaml(self):
        """Collect 'kubectl get -o yaml' output for each resource type into separate files."""
        self.logger.info("[*] Collecting kubectl get -o yaml per resource type...")

        for resource in self.KUBECTL_RESOURCES:
            output = self.run_command(["kubectl", "get", resource, "-A", "-o", "yaml"], ignore_errors=True)
            if output:
                resource_file = os.path.join(self.kubectl_getyaml_path, f"{resource}.txt")
                with open(resource_file, "w") as f:
                    f.write(self._redact_yaml_env(output))
                self.logger.info(f"  [+] Saved kubectl get -o yaml {resource}")
            else:
                self.logger.warning(f"  [!] No output for kubectl get -o yaml {resource} - skipping")

    def collect_kubectl_describe(self):
        """Collect 'kubectl describe' output for each resource type into separate files."""
        self.logger.info("[*] Collecting kubectl describe per resource type...")

        for resource in self.KUBECTL_RESOURCES:
            output = self.run_command(["kubectl", "describe", resource, "-A"], ignore_errors=True)
            if output:
                resource_file = os.path.join(self.kubectl_describe_path, f"{resource}.txt")
                with open(resource_file, "w") as f:
                    f.write(self._redact_kv_lines(output))
                self.logger.info(f"  [+] Saved kubectl describe {resource}")
            else:
                self.logger.warning(f"  [!] No output for kubectl describe {resource} - skipping")

    def _redact_sensitive(self, data):
        """Recursively redact sensitive values in a dict/list structure."""
        if isinstance(data, dict):
            return {
                k: "<REDACTED>" if (
                    self.SENSITIVE_KEYS.search(k)
                    and not self.SENSITIVE_KEYS_EXCLUSIONS.search(k)
                    and v
                ) else self._redact_sensitive(v)
                for k, v in data.items()
            }
        elif isinstance(data, list):
            return [self._redact_sensitive(item) for item in data]
        return data

    def _redact_env_values(self, data):
        """Recursively redact inline Kubernetes env values.

        Env entries are lists of {name: VAR, value: ...} dicts, so the sensitive
        indicator lives in the *value* of the `name` key rather than in a dict key -
        the generic key-based redactor cannot catch it. Only inline `value` is redacted;
        references (valueFrom/secretKeyRef) are left intact so the bundle still shows
        what is referenced.
        """
        if isinstance(data, dict):
            name = data.get("name")
            is_sensitive_env = (
                isinstance(name, str)
                and self.SENSITIVE_KEYS.search(name)
                and not self.SENSITIVE_KEYS_EXCLUSIONS.search(name)
            )
            return {
                k: "<REDACTED>" if (is_sensitive_env and k == "value" and isinstance(v, str) and v)
                else self._redact_env_values(v)
                for k, v in data.items()
            }
        elif isinstance(data, list):
            return [self._redact_env_values(item) for item in data]
        return data

    def _redact_yaml_env(self, output):
        """Redact inline sensitive env values in 'kubectl get -o yaml' output.

        Parses the YAML so env {name, value} pairs can be matched structurally. Falls
        back to line-based redaction (fail-closed) if the output is not valid YAML.
        """
        try:
            data = yaml.safe_load(output)
        except yaml.YAMLError as e:
            self.logger.warning(f"  [!] Could not parse YAML for redaction ({e}); using line-based fallback")
            return self._redact_kv_lines(output)
        if data is None:
            return output
        return yaml.dump(
            self._redact_env_values(data),
            default_flow_style=False, sort_keys=False, allow_unicode=True, width=4096
        )

    def _redact_kv_lines(self, text):
        """Redact 'KEY: value' lines whose key matches SENSITIVE_KEYS (for describe output)."""
        redacted_lines = []
        for line in text.splitlines():
            match = self._KV_LINE.match(line)
            if match and self.SENSITIVE_KEYS.search(match.group(2)) \
                    and not self.SENSITIVE_KEYS_EXCLUSIONS.search(match.group(2)):
                indent, key, sep = match.group(1), match.group(2), match.group(3)
                redacted_lines.append(f"{indent}{key}:{sep}<REDACTED>")
            else:
                redacted_lines.append(line)
        return "\n".join(redacted_lines)

    def collect_helm_info(self):
        """Collect helm release information from cluster secrets."""
        import base64
        import gzip

        self.logger.info("[*] Collecting helm release information...")

        helm_path = os.path.join(self.base_path, "helm")
        os.makedirs(helm_path, exist_ok=True)

        # Get helm release secrets as JSON
        releases_json = self.run_command(
            ["kubectl", "get", "secrets", "-A", "-l", "owner=helm", "-o", "json"], ignore_errors=True
        )
        if not releases_json:
            self.logger.warning("  [!] No helm releases found in cluster")
            return

        try:
            releases_data = json.loads(releases_json)
        except json.JSONDecodeError:
            self.logger.warning("  [!] Failed to parse helm secrets JSON")
            return

        # Decode each release and build summary table
        header = f"{'NAMESPACE':<30}{'NAME':<35}{'REVISION':<10}{'STATUS':<12}{'CHART':<35}{'APP VERSION'}"
        lines = [header]

        for item in releases_data.get("items", []):
            namespace = item.get("metadata", {}).get("namespace", "")
            labels = item.get("metadata", {}).get("labels", {})
            name = labels.get("name", "")
            version = labels.get("version", "")
            status = labels.get("status", "")
            chart = ""
            app_version = ""

            # Decode release payload to extract chart info
            release_b64 = item.get("data", {}).get("release", "")
            if release_b64:
                try:
                    raw = base64.b64decode(release_b64)
                    raw = base64.b64decode(raw)
                    decompressed = gzip.decompress(raw)
                    release_info = json.loads(decompressed)
                    chart_meta = release_info.get("chart", {}).get("metadata", {})
                    chart_name = chart_meta.get("name", "")
                    chart_version = chart_meta.get("version", "")
                    chart = f"{chart_name}-{chart_version}" if chart_name else ""
                    app_version = chart_meta.get("appVersion", "")
                except Exception as e:
                    self.logger.debug(f"  [!] Failed to decode release payload for {namespace}/{name}: {e}")

            lines.append(f"{namespace:<30}{name:<35}{version:<10}{status:<12}{chart:<35}{app_version}")

        status_file = os.path.join(helm_path, "helm_releases.txt")
        with open(status_file, "w") as f:
            f.write("\n".join(lines))
        self.logger.info(f"  [+] Saved helm releases list ({len(lines) - 1} releases)")

    def collect_topology_preview(self):
        """Collect topology preview report from deployment/ansible-logs/."""
        self.logger.info("[*] Collecting topology preview report...")

        tools_dir = os.path.dirname(os.path.abspath(__file__))
        deployment_dir = os.path.normpath(os.path.join(tools_dir, ".."))
        report_path = os.path.join(deployment_dir, "ansible-logs", "topology_preview_report.yaml")

        if not os.path.exists(report_path):
            self.logger.warning(f"  [!] Topology preview report not found at {report_path}, skipping")
            return

        try:
            dest_path = os.path.join(self.base_path, "topology_preview_report.yaml")
            shutil.copy(report_path, dest_path)
            file_size = os.path.getsize(dest_path)
            self.logger.info(f"  [+] Saved topology_preview_report.yaml ({file_size / 1024:.2f} KB)")
        except Exception as e:
            self.logger.error(f"  [!] Failed to collect topology preview report: {e}")

    def collect_install_logs(self):
        """Collect deployment install-*.log files from <config-dir>/logs/ into the bundle."""
        self.logger.info("[*] Collecting deployment install logs...")

        logs_dir = os.path.join(os.path.abspath(self.config_dir), "logs")
        if not os.path.isdir(logs_dir):
            self.logger.warning(f"  [!] Install logs directory not found at {logs_dir}, skipping")
            return

        install_logs = sorted(glob.glob(os.path.join(logs_dir, "install-*.log")))
        if not install_logs:
            self.logger.warning(f"  [!] No install-*.log files found in {logs_dir}, skipping")
            return

        dest_dir = os.path.join(self.base_path, "install_logs")
        os.makedirs(dest_dir, exist_ok=True)
        for log_path in install_logs:
            try:
                dest_path = os.path.join(dest_dir, os.path.basename(log_path))
                shutil.copy(log_path, dest_path)
                file_size = os.path.getsize(dest_path)
                self.logger.info(f"  [+] Saved {os.path.basename(log_path)} ({file_size / 1024:.2f} KB)")
            except Exception as e:
                self.logger.error(f"  [!] Failed to collect {os.path.basename(log_path)}: {e}")

    def create_summary_report(self):
        """Generate SUMMARY.json."""
        self.logger.info("[*] Generating summary report...")

        summary = {
            "collection_info": {
                "timestamp": self.timestamp,
                "collection_date": datetime.datetime.now().isoformat(),
            },
            "namespaces": self.get_all_namespaces(),
            "total_pods": len(self.get_pods()),
            "node_count": len(self.get_nodes()),
        }

        summary_file = os.path.join(self.base_path, "SUMMARY.json")
        with open(summary_file, "w") as f:
            json.dump(summary, f, indent=2)

        self.logger.info("  [+] Summary report created")

        # Copy debug_tool.md into the bundle
        docs_dir = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", "..", "docs"))
        debug_tool_md = os.path.join(docs_dir, "debug_tool.md")
        if os.path.exists(debug_tool_md):
            shutil.copy(debug_tool_md, os.path.join(self.base_path, "debug_tool.md"))
            self.logger.info("  [+] debug_tool.md copied into bundle")
        else:
            self.logger.warning(f"  [!] debug_tool.md not found at {debug_tool_md}, skipping")

    def create_archive(self):
        """Package the debug bundle into a compressed tar.gz archive."""
        self.logger.info("[*] Creating compressed archive...")
        archive_name = f"{self.base_path}.tar.gz"
        with tarfile.open(archive_name, "w:gz") as tar:
            tar.add(self.base_path, arcname=os.path.basename(self.base_path))

        # Calculate size
        size_mb = os.path.getsize(archive_name) / (1024 * 1024)
        self.logger.info(f"[*] Final bundle: {archive_name} ({size_mb:.2f} MB)")
        return archive_name

    def check_prerequisites(self):
        """Check that kubectl is available and properly configured with kubeconfig."""
        self.logger.info("[*] Checking prerequisites...")

        # Check if kubectl is installed
        result = subprocess.run(["kubectl", "version", "--client"], capture_output=True, text=True)
        if result.returncode != 0:
            self._log_error("  [!] PREREQUISITE ERROR: kubectl is not installed or not in PATH.\n  [!] Please install kubectl: https://kubernetes.io/docs/tasks/tools/")
            sys.exit(1)

        # Check if kubeconfig is available and cluster is reachable
        env = os.environ.copy()
        if self.kubeconfig_path:
            env["KUBECONFIG"] = self.kubeconfig_path

        result = subprocess.run(["kubectl", "cluster-info"], capture_output=True, text=True, env=env)
        if result.returncode != 0:
            error_lines = ["  [!] PREREQUISITE ERROR: kubectl is not configured or cluster is unreachable."]
            if self.kubeconfig_path:
                error_lines.append(f"  [!] kubeconfig: {self.kubeconfig_path}")
                error_lines.append("  [!] Please verify the kubeconfig file is valid and accessible")
            else:
                error_lines.append("  [!] Ensure one of the following is set up:")
                error_lines.append("  [!]   - ~/.kube/config exists with valid cluster credentials")
                error_lines.append("  [!]   - KUBECONFIG environment variable points to a valid kubeconfig file")
                error_lines.append("  [!]   - Provide --config with 'kubeconfig' variable defined")
            error_lines.append(f"  [!] kubectl error: {result.stderr.strip()}")
            self._log_error("\n".join(error_lines))
            sys.exit(1)

        self.logger.info("  [+] kubectl is configured and cluster is reachable")

    def run(self):
        """Execute all debug collection steps and produce the final archive."""
        self.setup_logging()

        self._log_section("Intel® AI for Enterprise RAG Debug Tool")

        self.setup_workspace()
        self.collect_config()
        self.check_prerequisites()
        self.collect_topology_preview()
        self.collect_install_logs()
        self.collect_pod_logs()
        self.collect_kubectl_get()
        self.collect_kubectl_getyaml()
        self.collect_kubectl_describe()
        self.collect_helm_info()
        self.create_summary_report()
        archive = self.create_archive()

        self._log_section("Debug collection complete!")
        self.logger.info(f"Archive: {archive}")
        self.logger.info("=" * self.LINE_WIDTH)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Enterprise RAG Debug Tool - Collect comprehensive diagnostic information"
    )
    parser.add_argument(
        "--output-dir",
        default="debug_bundle",
        help="Output directory for debug bundle (default: debug_bundle)"
    )
    parser.add_argument(
        "--config-dir",
        required=True,
        metavar="PATH",
        help="Path to config directory (env/local/) containing global_config.yaml, "
             "config.erag.yaml, config.inference.yaml, and kubeconfig.yaml."
    )

    args = parser.parse_args()

    debugger = EnterpriseRAGDebugger(
        output_dir=args.output_dir,
        config_dir=args.config_dir,
    )
    debugger.run()
