#!/usr/bin/env node
// Print the v1 read path of a SwarmCracker manager with the shipped Node SDK.
//
// Environment:
//
//   SWARMCRACKER_HOST         the manager URL (required)
//   SWARMCRACKER_CA           path to the CA bundle (optional)
//   SWARMCRACKER_TOKEN_FILE   path to a file holding the bearer token (optional)
//   SWARMCRACKER_TASK         a task reference; when set, the example also runs
//                             `true` in it and prints the exit status and stdout
//
// The output is stable `key=value` lines on stdout, one fact per line, so CI can
// diff it. A failed request prints `error=<code>` on stderr and exits 1. The
// token, the CA contents and the request headers are never printed.

import { ApiError, Client } from '../../../sdk/node/src/index.mjs';

async function main() {
  const host = process.env.SWARMCRACKER_HOST;
  if (!host) {
    console.error('error=usage');
    console.error('SWARMCRACKER_HOST is not set');
    return 1;
  }

  const client = new Client({
    host,
    ca: process.env.SWARMCRACKER_CA || undefined,
    tokenFile: process.env.SWARMCRACKER_TOKEN_FILE || undefined,
  });

  try {
    const version = await client.version();
    console.log(`version=${version.serverVersion}`);
    console.log(`capabilities=${[...(version.capabilities ?? [])].sort().join(',')}`);

    const services = await client.services.list({ limit: 2 });
    console.log(`services=${services.items.length}`);
    for (const service of services.items) {
      console.log(`service=${service.name}`);
    }

    const task = process.env.SWARMCRACKER_TASK;
    if (task) {
      const chunks = [];
      const stream = client.tasks.exec(task, ['true']);
      for await (const frame of stream) {
        if (frame.type === 'stdout') {
          chunks.push(Buffer.from(frame.data));
        }
      }
      // A non-zero guest status is still a successful API call: print it and
      // keep going, so the example does not fail on a command.
      console.log(`exec.exit=${stream.exitCode}`);
      console.log(`exec.stdout=${Buffer.concat(chunks).toString('utf8').trim()}`);
    }
  } catch (error) {
    if (error instanceof ApiError) {
      console.error(`error=${error.code}`);
      return 1;
    }
    if (error instanceof TypeError) {
      // A host the client refuses (plain http to a non-loopback address, both
      // a token and a tokenFile, ...) is a usage error, not a server failure.
      console.error('error=usage');
      return 1;
    }
    throw error;
  } finally {
    client.close();
  }

  return 0;
}

process.exitCode = await main();
