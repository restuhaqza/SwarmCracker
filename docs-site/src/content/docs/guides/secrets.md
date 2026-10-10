---
title: Secrets and Configs
description: Inject secrets and configuration files into microVMs and attach them to services.
---

SwarmCracker supports SwarmKit **secrets** and **configs**. A secret is a blob
of sensitive data (up to 500KB) that is injected into a microVM's rootfs and
never exposed by `inspect`. A config is the same mechanism for non-secret
configuration data.

Both are stored on the SwarmKit manager (cluster-wide) and delivered to the node
that runs a task, then written into that task's rootfs at the path the service
requests.

## Creating a secret or config

Create from a file, or `-` to read from stdin:

```bash
swarmcracker secret create db-pass ./password.txt
cat tls-cert.pem | swarmcracker secret create tls-cert -

swarmcracker config create app-yaml ./app.yaml
```

List and inspect them. **`inspect` never prints the value** — SwarmKit redacts
secret payloads from the control API, and SwarmCracker only shows metadata:

```bash
swarmcracker secret ls
swarmcracker secret inspect db-pass

swarmcracker config ls
swarmcracker config inspect app-yaml
```

## Attaching to a service

Use `--secret` / `--config`. The value is a comma-separated spec:

```
[src=]NAME[,target=PATH][,mode=0400][,uid=N][,gid=N]
```

```bash
swarmcracker service create --name api --image myapp \
  --secret src=db-pass,target=/run/secrets/db-pass,mode=0400 \
  --config src=app-yaml,target=/config/app.yaml,mode=0444
```

- `src` (or a bare value) is the secret/config name or ID.
- `target` defaults to `/run/secrets/<name>` for secrets and `/config/<name>`
  for configs.
- `mode` defaults to `0400` for secrets and `0444` for configs.
- `uid`/`gid` are optional (best-effort; not all images preserve them).

Inside the microVM the file exists at `target` with the requested mode:

```bash
# in the guest
cat /run/secrets/db-pass
ls -l /run/secrets/db-pass   # -r-------- 1 root root
```

`service update --secret ...` / `--config ...` replaces the service's grants
(omit them to leave them unchanged).

## Rotation

SwarmKit secrets and configs are **immutable** — only labels can change. To
rotate a value, create a new secret/config and update the service to reference
it:

```bash
swarmcracker secret create db-pass-v2 ./new-password.txt
swarmcracker service update --secret src=db-pass-v2,target=/run/secrets/db-pass api
```

## Removal

A secret or config cannot be removed while a service still references it:

```bash
swarmcracker secret rm db-pass
# secret is in use by service(s) api; remove or update them first
```

Remove or update the referencing services first.

## How injection stays safe

- Secret/config values are **never** written to logs, `service inspect`,
  `task inspect`, or metrics. SwarmKit redacts secret payloads, and the CLI only
  ever prints references (name + target).
- Injection targets a **private, per-task copy** of the image rootfs. The shared
  image cache is never modified, so a secret cannot leak into another task that
  uses the same image.

## Daemon configuration file

The commands above manage SwarmKit secrets/configs. To manage the daemon's own
configuration **file**, use `swarmcracker config file`:

```bash
swarmcracker config file ls
swarmcracker config file validate
swarmcracker config file migrate
```

**See Also:** [Networking](/guides/networking/) | [CLI Reference](/reference/cli/) | [Configuration](/guides/configuration/)
