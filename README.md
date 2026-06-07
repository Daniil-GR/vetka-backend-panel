# Vetka Backend Panel

Vetka Backend Panel is the central Go/PostgreSQL backend for managing Vetka nodes, users, node assignments, sync versions, and subscription output.

`vetka-node-agent` is a separate repository and is intentionally not implemented here. The Backend Panel and PostgreSQL database are the source of truth. Node Agent is only an executor and applied-state cache.

## Architecture

```text
Bot / Admin UI / API
        |
Vetka Backend Panel
        |
PostgreSQL = source of truth
        |
Node Manager
        |
Node Agent API
        |
Protocol Driver on node: Naive or Mieru
```

One node has exactly one protocol: `naive` or `mieru`. User assignments inherit the node protocol, and PostgreSQL enforces that invariant.

## Local Development

Copy `.env.example` to `.env`, adjust secrets, then start PostgreSQL and the backend:

```bash
docker compose up postgres
go run ./cmd/server
```

Or run both services through Compose:

```bash
docker compose up --build
```

The UI listens on `http://localhost:8080` by default. Login uses `ADMIN_USERNAME` and `ADMIN_PASSWORD`. API endpoints use:

```http
Authorization: Bearer <ADMIN_API_TOKEN>
```

## Migrations

Migrations are applied automatically on server startup. You can also run:

```bash
make migrate-up
```

The SQL source for the first migration is in `migrations/001_initial.up.sql`; the application embeds the same schema in Go for simple MVP startup.

## Creating The First Node

Open `/nodes`, fill `name`, `domain`, `api_url`, and `protocol_type`. If `node_id` or `node_secret` is empty, the Backend Panel generates them.

After creation the UI shows install environment values for the separate Node Agent:

```bash
NODE_ID="..."
NODE_SECRET="..."
PROTOCOL_TYPE="naive"
NODE_PORT="2222"
NODE_LISTEN_HOST="0.0.0.0"
BACKEND_PANEL_IP="<backend_ip>"
```

For production, the Node Agent port should be reachable only from the Backend Panel IP.

## Syncing Nodes

The Backend Panel sends the full desired state through:

```http
POST /v1/sync
Authorization: Bearer <NODE_SECRET>
X-Node-Id: <NODE_ID>
```

No per-user create/delete commands are sent to nodes. On sync success the panel updates `last_applied_version`; on failure it records `last_error` and a row in `node_sync_events`.

## Users And Subscriptions

Create users in `/users`. The panel generates `subscription_token` and per-node protocol credentials. A user subscription is available at:

```text
https://sub.vetka.tech/sub/<subscription_token>
```

Disabled or expired users do not receive a subscription response and are excluded from node sync payloads.

Naive and Mieru URI builders live in `internal/subscriptions`. The Naive URI format and Mieru share link are marked with TODOs until final client formats are confirmed.

## API For Future Telegram Bot

Implemented MVP endpoints:

```http
GET  /api/nodes
POST /api/nodes
GET  /api/nodes/{id}
PATCH /api/nodes/{id}
POST /api/nodes/{id}/health
POST /api/nodes/{id}/status
POST /api/nodes/{id}/sync
POST /api/nodes/sync-all

POST /api/users
GET  /api/users/{id}
PATCH /api/users/{id}
POST /api/users/{id}/enable
POST /api/users/{id}/disable
POST /api/users/{id}/assign-node
POST /api/users/{id}/unassign-node
POST /api/users/{id}/sync
GET  /api/users/{id}/subscription
```

`GET /api/nodes` and `GET /api/nodes/{id}` return masked `node_secret`. `POST /api/nodes` returns the raw generated or submitted secret once so the admin can bootstrap a Node Agent.

Telegram Bot and payment logic are not part of this repository.

## Security Notes

`node_secret` is stored in PostgreSQL for the MVP. The UI masks it after creation and API responses should avoid exposing raw secrets outside trusted admin flows.

TODO: add encryption at rest for node secrets using `APP_SECRET`.

Never commit real `.env` files or production secrets.

## Checks

```bash
go fmt ./...
go test ./...
go run ./cmd/server --help
git diff --check
```
