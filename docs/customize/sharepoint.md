# SharePoint Integration

[← Customize](../README.md#customize)

Intel® Chatbot Toolkit can integrate with Microsoft SharePoint Online to ingest documents directly from SharePoint sites into the knowledge base.

This is how you keep answers current without anyone re-uploading files: documents stay where the organization already publishes them, and scheduled sync brings changes in. Site filtering means each user can be limited to the sites they are entitled to.

## Overview

SharePoint integration works under the following premises:

- **Site-level access model.** The Microsoft Graph API does not expose an endpoint listing every site an application has access to. Sites must be manually added to the tracking list. When added, the system resolves the site through Graph and fails if the app has no access.
- **SharePoint is the source of truth.** Synchronization compares files in tracked sites against PostgreSQL `FileStatus` records carrying a `site_name`, downloading new or updated files for processing and embedding. Files deleted on SharePoint are removed from the knowledge base.
- **File upload to SharePoint.** Users can upload files to tracked sites through the UI or API. Uploaded files land in the root of the site's default document library.
- **Per-user filtering.** When RBAC is enabled, each user's delegated Microsoft token is retrieved through the Keycloak broker and used to check site access, so users only see files from sites they can reach.

## Prerequisites

SharePoint integration requires a working Single Sign-On setup with Microsoft Entra ID (see the SSO half of [Authentication](auth.md)). The same app registration is used for both SSO and SharePoint.

In addition to the SSO prerequisites:

### API permissions

The app registration needs the following Microsoft Graph permissions with admin consent:

| Permission | Type | Purpose |
|------------|------|---------|
| `Sites.Selected` | Application | Read and write items in the selected SharePoint site collections |
| `Sites.Selected` | Delegated | Read and write items in the selected site collections on behalf of the signed-in user |
| `User.Read` | Delegated | Sign in and read the user profile |

Configure them under **Microsoft Entra ID → App registrations → [your app] → API permissions → Add a permission**.

> **Note:** The installer cannot verify Graph permissions or admin consent. Neither the configurator Job nor the `app_edp` validation checks them, so a missing permission surfaces only as a Graph `403` when a site is added or synchronized.

### Site-level access

Each SharePoint site must grant read/write access to the app registration, because `Sites.Selected` grants nothing on its own. Use the Microsoft Graph `sites/{site-id}/permissions` endpoint or the SharePoint admin center. See [Microsoft's documentation](https://learn.microsoft.com/en-us/graph/api/site-post-permissions).

## Configuration

Set the following in `env/<name>/config.erag.yaml`:

```yaml
erag_keycloak_oidc_endpoint: "https://login.microsoftonline.com/<tenant-id>/v2.0/.well-known/openid-configuration"
erag_keycloak_oidc_alias: "enterprise-sso"
erag_keycloak_oidc_client_id: "<application-client-id>"
erag_keycloak_oidc_client_secret: "<client-secret-value>"
erag_keycloak_oidc_tenant_id: "<directory-tenant-id>"
```

| Option | Purpose |
|--------|---------|
| `erag_keycloak_oidc_endpoint` | Entra OpenID Connect metadata document URL. Non-empty enables SSO brokering (required). |
| `erag_keycloak_oidc_alias` | Keycloak identity-provider alias (default `enterprise-sso`). |
| `erag_keycloak_oidc_client_id` | Entra app registration application (client) ID. |
| `erag_keycloak_oidc_client_secret` | Entra app registration client secret value. |
| `erag_keycloak_oidc_tenant_id` | Entra directory (tenant) ID. Non-empty additionally enables SharePoint ingestion. |

There is no `sharepoint_enabled` flag. Both SSO and SharePoint are enabled implicitly by credential presence:

- All variables empty (the default) leaves both features disabled.
- Setting `erag_keycloak_oidc_endpoint` enables SSO and requires `erag_keycloak_oidc_alias`, `erag_keycloak_oidc_client_id`, and `erag_keycloak_oidc_client_secret`.
- Setting `erag_keycloak_oidc_tenant_id` additionally enables SharePoint and requires `erag_keycloak_oidc_endpoint`, `erag_keycloak_oidc_client_id`, and `erag_keycloak_oidc_client_secret`.

A partial configuration is rejected at install time.

Deploy or redeploy:

```bash
./es_auto_installer.sh install erag --env <name>
```

Re-check the configuration against the live cluster:

```bash
./es_auto_installer.sh validate erag --env <name>
```

## Managing SharePoint sites

The API is exposed through APISIX at `https://<base_domain_name>/api/v1/edp/sharepoint/...` in both routing modes. All routes require the `admin` or `maintainer` permission. All requests need a bearer token.

### Adding a site

```http
POST https://<base_domain_name>/api/v1/edp/sharepoint/sites
Content-Type: application/json
Authorization: Bearer <token>

{
  "site_url": "https://contoso.sharepoint.com/sites/my-team-site"
}
```

The system resolves the site URL through Microsoft Graph, obtains its Graph site ID, and creates a tracking record. A site the app registration cannot reach fails with the status code Graph returned (typically `403`). A URL already tracked returns `409`.

### Synchronizing files

- **Preview changes:** `GET https://<base_domain_name>/api/v1/edp/sharepoint/sync` returns the planned `add`, `update`, `delete`, and `no action` entries without applying them.

- **Apply sync:** `POST https://<base_domain_name>/api/v1/edp/sharepoint/sync` downloads new and updated files into the knowledge base and removes files that no longer exist on the site. If another sync is already running, returns `409` with `SharePoint synchronization is already in progress. Please wait and try again.`

Each file is identified by its `site_name` and an `object_name` of the form `{drive_name}/{relative_path}`, for example `Documents/Reports/Q1.pdf`.

### Uploading files

Files uploaded this way are stored in the root of the site's default document library. A synchronization must run before the file appears in the knowledge base.

```http
POST https://<base_domain_name>/api/v1/edp/sharepoint/files?site_id=<graph_site_id>
Content-Type: multipart/form-data
Authorization: Bearer <token>

file: <binary>
```

| Parameter | Location | Description |
|-----------|----------|-------------|
| `site_id` | Query string | Microsoft Graph site ID of the tracked site |
| `file` | Form data | The file to upload |

**Response:**

```json
{
  "message": "File 'report.pdf' uploaded to SharePoint site.",
  "web_url": "https://contoso.sharepoint.com/sites/my-team-site/Shared%20Documents/report.pdf"
}
```

> **Note:** This differs from S3 bucket uploads, which use presigned URLs. SharePoint uploads go through the EDP backend, which forwards them to the Microsoft Graph API using the application credentials.

### Fetching a file URL

Returns the SharePoint web URL of a file so the UI can open it in place. Access control is handled by SharePoint: a user without permission gets a SharePoint `403`.

```http
POST https://<base_domain_name>/api/v1/edp/sharepoint/file-url
Content-Type: application/json
Authorization: Bearer <token>

{
  "site_name": "My Team Site",
  "object_name": "Documents/Reports/Q1.pdf"
}
```

| Field | Description |
|-------|-------------|
| `site_name` | Display name of the tracked site |
| `object_name` | Path of the file within the site, as `{drive_name}/{relative_path}` |

**Response:**

```json
{
  "url": "https://contoso.sharepoint.com/sites/my-team-site/Shared%20Documents/Reports/Q1.pdf"
}
```

### Removing a file

```http
DELETE https://<base_domain_name>/api/v1/edp/sharepoint/files
Content-Type: application/json
Authorization: Bearer <token>

{
  "site_name": "My Team Site",
  "object_name": "Documents/Reports/Q1.pdf"
}
```

Deletion is asynchronous: the request marks the file for deletion and returns `File '<object_name>' deletion initiated.` The Celery task then removes the file's vectors, deletes the file from the SharePoint site through Graph, and removes the database record.

> **Important:** Deleting a single file from a site that is still tracked removes it from the SharePoint site, not only from the knowledge base. Disconnecting a whole site behaves differently (see [Disconnecting a site](#disconnecting-a-site)).

### Disconnecting a site

```http
DELETE https://<base_domain_name>/api/v1/edp/sharepoint/sites/<graph_site_id>
Authorization: Bearer <token>
```

Disconnecting a site:

- Removes the site's tracking record
- Deletes every file that came from that site from the knowledge base (vector store + PostgreSQL records)
- Leaves the SharePoint site and its files intact

The response reports how many files were queued for deletion:

```json
{
  "message": "Site disconnected. 12 file(s) deleted from the knowledge base.",
  "deleted_files": 12
}
```

> **Note:** Disconnect reuses the same deletion path as removing a file, which does delete from SharePoint. Source files survive because the tracking record is deleted first, so each deletion task finds no site record and logs `SP site record not found for '<name>', skipping SP delete.`

## Scheduled synchronization

Synchronization is manual by default. To run it periodically, set the following in `env/<name>/config.erag.yaml`:

```yaml
edp_scheduled_sync_enabled: true
edp_scheduled_sync_period_seconds: "60"
edp_scheduled_sync_sharepoint_period_seconds: "60"
```

| Option | Default | Description |
|--------|---------|-------------|
| `edp_scheduled_sync_enabled` | `false` | Registers the Celery beat periodic tasks. When false, no scheduling environment is rendered. |
| `edp_scheduled_sync_period_seconds` | `"60"` | Interval between general storage sync tasks. |
| `edp_scheduled_sync_sharepoint_period_seconds` | `"60"` | Interval between SharePoint sync tasks. Falls back to `edp_scheduled_sync_period_seconds` when unset. |

The scheduled SharePoint task returns immediately when the integration is not configured. It acquires the sync lock without blocking: if a sync is already running, it logs that the lock is held and skips this tick rather than queueing.

## Role-based access control (RBAC)

Enable RBAC in `env/<name>/config.erag.yaml`:

```yaml
edp_rbac_enabled: true
edp_rbac_validation_type: "ALWAYS"  # NONE | ALWAYS | CACHED | STATIC
```

With RBAC enabled, `GET /api/list_bucket_with_permissions` returns both a `buckets` list and a `sites` list, and the SharePoint side is filtered per user:

- The user's Keycloak access token is exchanged for their stored Microsoft token at `/realms/EnterpriseRAG/broker/<alias>/token`.
- Each tracked site is checked against that delegated token.
- Only accessible sites (and permitted buckets) appear in file listings and search results.

If the broker exchange fails (for example because `Store tokens` is off on the identity provider, or `erag_keycloak_oidc_endpoint` was never set), EDP logs a warning and returns **no** sites for that user. Users then see an empty SharePoint result set rather than unfiltered data.

With RBAC disabled, every tracked site is returned to every caller.

## Related docs

| Topic | Link |
|-------|------|
| Single Sign-On setup (required for SharePoint) | [Authentication](auth.md) (Part 1) |
| EDP storage configuration | [object_store.md](object_store.md) |
