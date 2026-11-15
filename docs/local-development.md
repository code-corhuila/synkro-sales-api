# Local development

Detailed procedures and delivery notes moved from the repository README at
[source 2bcbb25](https://github.com/code-corhuila/synkro-sales-api/blob/2bcbb25330555c9120c16d6a473d7ea9fb1b3bb4/README.md).
Recorded execution results retain their original scope; moving them does not
constitute a new execution or certify a different revision.

Run commands from the repository root unless a step explicitly changes directory.

## Running locally

Apply the Sales DB migrations first. Supply `MONGODB_URI` for `sales_app` and
exactly one of `JWT_PUBLIC_KEY` (PEM, including literal escaped `\n`) or
`JWT_PUBLIC_KEY_FILE` (an accessible public PEM file). Missing or invalid keys
stop startup; there is no development authentication bypass. Never use an
administrator URI or put credentials/private keys in this repository.

```bash
go run ./cmd/sales-api
```

The service listens on `:8080` (override with `HTTP_PORT`). Verify it:

```bash
curl http://localhost:8080/health
```

Public `/health` pings this service's MongoDB replica set: `200` with database
`ok`, or `503` with database `down`. `MONGODB_TIMEOUT` defaults to `5s` (maximum
`10s`), and `MONGODB_MAX_POOL_SIZE` defaults to `10`. The server has explicit
header/read/write/idle limits of 5/10/15/60 seconds and a 20-second shutdown.
