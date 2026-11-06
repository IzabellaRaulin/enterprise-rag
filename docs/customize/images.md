# Building Images

[← Customize](../README.md#customize)

Intel® Chatbot Toolkit provides the ability to build Docker images locally instead of using pre-built images from public registries. This is useful for custom modifications, security requirements, or development and testing.

Most deployments never need this. Reach for it when your environment cannot pull from a public registry, when your policy requires images you scanned and signed yourself, or when you changed a microservice and want to run your version. This guide covers pointing the deployment at a registry, building the images, and pushing them.

## Registry configuration

Set in `env/<name>/config.erag.yaml`:

```yaml
registry: "docker.io/intel"    # Default: public registry
# tag: "3.0.0"                # Omit to use the default tag matching the solution version
# tag_override: false         # Set true to accept a tag not containing the solution version
```

| Option | Default | Description |
|--------|---------|-------------|
| `registry` | `docker.io/intel` | Image registry base URL |
| `tag` | version from `deployment/version.yaml` | Image tag. Left unset, uses `default_image_tag` from `version.yaml`, which matches the solution version. |
| `tag_override` | `false` | Set `true` to accept a tag not containing the solution version (e.g. `latest`) |

`tag` is optional. Left unset, the deployment uses `default_image_tag` from `deployment/version.yaml`, which matches the solution version. A tag that does not contain the solution version is refused before anything is deployed unless `tag_override: true` is set.

## Build and push images

The `update_images.sh` script builds microservice images from source and pushes them to a specified registry. The script consists of three main steps: build images, set up the registry, and push the images. To execute all at once:

```bash
./update_images.sh --build --setup-registry --push
```

Alternatively, run each step separately:

### Step 1: Build

Build images for each microservice component using the source code:

```bash
./update_images.sh --build
```

Options:

- Build individual images:

  ```bash
  ./update_images.sh --build embedding-usvc reranking-usvc
  ```

- List all available image names:

  ```bash
  ./update_images.sh --help  # refer to "Components Available" section
  ```

- Increase concurrent tasks:

  ```bash
  ./update_images.sh --build -j 4
  ```

- Set a custom image tag:

  ```bash
  ./update_images.sh --build --tag v1.2.3
  ```

  Defaults to `latest` if not specified.

### Step 2: Setup registry

Configure the registry where the built images will be pushed:

```bash
./update_images.sh --setup-registry
```

By default, the registry is set to `localhost:5000`. Change this using the `--registry` option.

**Single-node clusters:**

The local registry is a developer-only convenience. When the host container runtime is nerdctl (the case after a Kubespray localhost deployment), `--setup-registry` installs a systemd-managed service so the registry runs in a dedicated containerd namespace, persists pushed images under `/var/lib/local-registry`, and automatically restarts after containerd restarts and reboots.

By default (`localhost:5000`) the registry binds loopback only and is not exposed on the network.

**Multi-node clusters:**

Worker nodes must be able to reach the registry, so bind it to a routable address rather than loopback. The registry is unauthenticated, so only do this on a trusted network:

```bash
./update_images.sh --setup-registry --registry <node-ip-or-host>:5000
./update_images.sh --build --push --registry <node-ip-or-host>:5000
```

Then point the deployment at it in `env/<name>/config.erag.yaml`:

```yaml
registry: "<node-ip-or-host>:5000"
tag_override: true
```

The registry stays running until you remove it explicitly (pass the same `--registry` used at setup so the matching containerd trust config is cleaned up):

```bash
./update_images.sh --teardown-registry              # remove the registry, keep pushed image data
./update_images.sh --teardown-registry --purge-data # also delete /var/lib/local-registry
```

### Step 3: Push

Push the built images to the configured registry:

```bash
./update_images.sh --push
```

This uploads all built images to the registry specified during setup.

## Related docs

| Topic | Link |
|-------|------|
| Image registry map and Dockerfiles | `deployment/images.yaml` |
| Toolkit Manager configuration | [`../../docs/customize/configuration.md`](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/customize/configuration.md) |
