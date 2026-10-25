#!/usr/bin/env python3
"""Print the v1 read path of a SwarmCracker manager with the shipped Python SDK.

Environment:

    SWARMCRACKER_HOST         the manager URL (required)
    SWARMCRACKER_CA           path to the CA bundle (optional)
    SWARMCRACKER_TOKEN_FILE   path to a file holding the bearer token (optional)
    SWARMCRACKER_TASK         a task reference; when set, the example also runs
                              `true` in it and prints the exit status and stdout

The output is stable `key=value` lines on stdout, one fact per line, so CI can
diff it. A failed request prints `error=<code>` on stderr and exits 1. The token,
the CA contents and the request headers are never printed.
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", "sdk", "python"))

from swarmcracker import ApiError, Client


def main() -> int:
    host = os.environ.get("SWARMCRACKER_HOST")
    if not host:
        print("error=usage", file=sys.stderr)
        print("SWARMCRACKER_HOST is not set", file=sys.stderr)
        return 1

    try:
        with Client(
            host,
            ca=os.environ.get("SWARMCRACKER_CA") or None,
            token_file=os.environ.get("SWARMCRACKER_TOKEN_FILE") or None,
        ) as client:
            version = client.version()
            print(f"version={version['serverVersion']}")
            print("capabilities=" + ",".join(sorted(version.get("capabilities") or [])))

            services = client.services.list(limit=2)
            print(f"services={len(services['items'])}")
            for service in services["items"]:
                print(f"service={service['name']}")

            task = os.environ.get("SWARMCRACKER_TASK")
            if task:
                stdout = b""
                with client.tasks.exec(task, ["true"]) as stream:
                    for frame in stream:
                        if frame["type"] == "stdout":
                            stdout += frame["data"]
                    exit_code = stream.exit_code
                # A non-zero guest status is still a successful API call: print
                # it and keep going, so the example does not fail on a command.
                print(f"exec.exit={exit_code}")
                print(f"exec.stdout={stdout.decode('utf-8', 'replace').strip()}")
    except ApiError as error:
        print(f"error={error.code}", file=sys.stderr)
        return 1
    except ValueError:
        # A host the client refuses (plain http to a non-loopback address, both
        # a token and a token_file, ...) is a usage error, not a server failure.
        print("error=usage", file=sys.stderr)
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
