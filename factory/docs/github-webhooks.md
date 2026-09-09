# Retired GitHub webhook settings

The Routines model removes the dedicated GitHub webhook listener along with
Definitions and Automations. Factory no longer serves
`/api/v1/webhooks/github`.

Existing `config.toml` files may still contain these retired fields:

- `webhook_listen`
- `webhook_tls_cert`
- `webhook_tls_key`
- `github_webhook_secret_file`

Factory accepts and ignores them during upgrade so an old configuration cannot
prevent the database migration or server startup. Remove the fields after the
upgrade. They do not start a listener.

Use a Routine to describe repository work, choose its repositories, and run it
manually or on a schedule.
