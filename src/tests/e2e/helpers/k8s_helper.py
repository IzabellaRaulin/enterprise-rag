#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import base64
import logging

import kr8s
import kr8s.objects
import yaml

logger = logging.getLogger(__name__)

# Declared explicitly instead of looked up by name. Passing kr8s a string makes it resolve
# plural.group through full API discovery, which enumerates every API group on the cluster —
# so a single unavailable aggregated APIService (a metrics adapter that the mesh has cut off
# is enough) fails the lookup for unrelated kinds, and kr8s surfaces it as
# "UnboundLocalError: cannot access local variable 'plural'" rather than naming the group.
# These checks concern backup and restore, so they now consult velero.io and
# snapshot.storage.k8s.io and nothing else.
_Backup = kr8s.objects.new_class("Backup", "velero.io/v1", asyncio=False, namespaced=True)
_Restore = kr8s.objects.new_class("Restore", "velero.io/v1", asyncio=False, namespaced=True)
_BackupStorageLocation = kr8s.objects.new_class(
    "BackupStorageLocation", "velero.io/v1", asyncio=False, namespaced=True
)
# plural is given because kr8s derives it by appending "s", which yields
# "volumesnapshotclasss" and a 404. The other three pluralise correctly.
_VolumeSnapshotClass = kr8s.objects.new_class(
    "VolumeSnapshotClass", "snapshot.storage.k8s.io/v1", asyncio=False, namespaced=False,
    plural="volumesnapshotclasses",
)


class ResourceNotFound(Exception):
    pass


class K8sHelper:

    def retrieve_admin_password(self, secret_name, namespace):
        """Retrieve the admin password from the keycloak secret"""
        logger.debug(f"Retrieving the admin password from namespace '{namespace}'")
        secrets = kr8s.get("secrets", namespace=namespace)
        for secret in secrets:
            if secret.name == secret_name:
                # Extract and decode the base64-encoded password
                encoded_password = secret.data.get("password") or secret.data.get("admin-password")
                return base64.b64decode(encoded_password).decode().strip()

    def list_namespaces(self):
        """List all namespaces in the Kubernetes cluster"""
        namespaces = kr8s.get("namespaces")
        return [ns.name for ns in namespaces]

    def get_namespace(self, namespace_name):
        """Get a namespace by name"""
        namespaces = kr8s.get("namespaces")
        for ns in namespaces:
            if ns.name == namespace_name:
                return ns
        return None

    def get_backups(self, namespace, label_selector=None):
        """Get Velero Backups in a namespace, optionally filtered by label.

        Bound to velero.io/v1 explicitly: the PostgreSQL operator also serves a
        `backups` resource, and on a cluster running both a bare name resolves to
        that one and returns an empty list — a missing backup rather than an error.
        """
        logger.debug(f"Getting Velero backups in namespace '{namespace}' (labels: {label_selector})")
        return list(kr8s.get(_Backup, namespace=namespace, label_selector=label_selector))

    def get_restores(self, namespace, label_selector=None):
        """Get Velero Restores in a namespace, optionally filtered by label"""
        logger.debug(f"Getting Velero restores in namespace '{namespace}' (labels: {label_selector})")
        return list(kr8s.get(_Restore, namespace=namespace, label_selector=label_selector))

    def get_backup_storage_locations(self, namespace):
        """Get Velero BackupStorageLocations in a namespace"""
        return list(kr8s.get(_BackupStorageLocation, namespace=namespace))

    def get_volume_snapshot_classes(self):
        """Get the cluster's VolumeSnapshotClasses.

        A backup taken without one completes while capturing object metadata and
        no volume contents, so its absence is worth failing on separately.
        """
        return list(kr8s.get(_VolumeSnapshotClass))

    def list_pvcs(self, namespace):
        """List all PersistentVolumeClaims in a namespace"""
        return list(kr8s.get("persistentvolumeclaims", namespace=namespace))

    def list_pods(self, namespace):
        """List all pods in the specified namespace"""
        return list(kr8s.get("pods", namespace=namespace))

    def get_pod_by_label(self, namespace, label_selector):
        """Returns first pod matching a label selector in a namespace"""
        logger.debug(f"Getting pods with label selector '{label_selector}' in namespace '{namespace}'")
        pods = list(kr8s.get("pods", namespace=namespace, label_selector=label_selector))
        if len(pods) == 0:
            raise ResourceNotFound(f"No running pods found with label '{label_selector}' in namespace '{namespace}'.")
        return pods[0]

    def count_pods_by_label(self, namespace, label_selector):
        """Returns the number of pods matching a label selector in a namespace (0 if none)."""
        logger.debug(f"Counting pods with label selector '{label_selector}' in namespace '{namespace}'")
        return len(list(kr8s.get("pods", namespace=namespace, label_selector=label_selector)))

    def get_deployment_manifest_version(self, namespace="default"):
        """Read the solution version from the erag-deployment-manifest ConfigMap."""
        logger.debug("Reading version from erag-deployment-manifest ConfigMap")
        configmaps = kr8s.get("configmaps", namespace=namespace)
        for cm in configmaps:
            if cm.name == "erag-deployment-manifest":
                manifest_raw = cm.data.get("manifest.yaml", "")
                manifest = yaml.safe_load(manifest_raw)
                return manifest["deployment"]["version"]
        raise ResourceNotFound("ConfigMap 'erag-deployment-manifest' not found")

    def delete_pods_by_label(self, namespace, label_selector):
        """Delete all pods matching a label selector in a namespace"""
        logger.debug(f"Deleting pods with label selector '{label_selector}' in namespace '{namespace}'")
        deleted = 0
        for pod in kr8s.get("pods", namespace=namespace, label_selector=label_selector):
            logger.debug(f"Deleting pod '{pod.name}'")
            pod.delete()
            deleted += 1
        if deleted == 0:
            raise ResourceNotFound(f"No pods found with label '{label_selector}' in namespace '{namespace}'.")

    def wait_for_pod_ready(self, namespace, label_selector, timeout=300):
        """Wait until a pod matching label_selector is Ready.
        Already terminating pods are skipped."""
        logger.debug(f"Waiting for pod with label '{label_selector}' to be ready in namespace '{namespace}'")
        for pod in kr8s.get("pods", namespace=namespace, label_selector=label_selector):
            if pod.metadata.get("deletionTimestamp"):
                logger.debug(f"Pod '{pod.name}' is Terminating, skipping")
                continue
            logger.debug(f"Found non-terminating pod '{pod.name}', waiting for condition=Ready")
            pod.wait("condition=Ready", timeout=timeout)
            logger.debug(f"Pod '{pod.name}' is ready")
            return pod
        raise ResourceNotFound(
            f"No pods found with label '{label_selector}' in namespace '{namespace}'"
        )

    def get_pod_logs(self, namespace, label_selector, since_seconds=None):
        """Get logs from all pods matching a label selector"""
        all_logs = []
        for pod in kr8s.get("pods", namespace=namespace, label_selector=label_selector):
            kwargs = {}
            if since_seconds:
                kwargs["since_seconds"] = since_seconds
            log_lines = list(pod.logs(**kwargs))
            all_logs.append({"pod": pod.name, "logs": "\n".join(log_lines)})
        return all_logs

    def exec_in_pod(self, pod, command):
        """Execute a command in a pod's container"""
        logger.debug(f"Executing command '{command}' in pod '{pod.name}'")
        try:
            exec_result = pod.exec(command)
            stdout = exec_result.stdout
            if isinstance(stdout, bytes):
                return stdout.decode().strip()
            else:
                return stdout.strip()
        except Exception as e:
            logger.error(f"Failed to execute command in pod '{pod.name}': {e}")
            raise  # Re-raise the exception to fail the test

    def get_namespace_phase(self, namespace_name):
        """Return the lifecycle phase of a namespace ('Active'/'Terminating'), or None if absent"""
        ns = self.get_namespace(namespace_name)
        if ns is None:
            return None
        return ns.raw.get("status", {}).get("phase")

    def list_helm_releases(self, namespace=None):
        """List Helm releases as (namespace, release_name) tuples, discovered from Secrets of
        type 'helm.sh/release.v1'. Searches all namespaces when namespace is None."""
        ns = namespace if namespace is not None else kr8s.ALL
        releases = []
        for secret in kr8s.get("secrets", namespace=ns):
            if secret.raw.get("type") != "helm.sh/release.v1":
                continue
            # Secret name format: sh.helm.release.v1.<release>.v<N>
            parts = secret.name.split(".")
            release_name = parts[4] if len(parts) >= 6 else secret.name
            releases.append((secret.namespace, release_name))
        return releases

    def list_secrets(self, namespace):
        """List secrets in a namespace"""
        return list(kr8s.get("secrets", namespace=namespace))

    def list_crd_groups(self):
        """Return the set of API groups across all CustomResourceDefinitions in the cluster"""
        groups = set()
        for crd in kr8s.get("customresourcedefinitions"):
            group = crd.raw.get("spec", {}).get("group")
            if group:
                groups.add(group)
        return groups
