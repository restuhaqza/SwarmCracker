"""The SwarmCracker API v1 Python SDK.

The models in :mod:`swarmcracker.types` are generated from ``api/openapi.yaml``
by ``tools/sdkgen`` and committed; do not edit them by hand. The client in
:mod:`swarmcracker.client` is a thin, standard-library-only transport wrapper.

Python 3.11+; no third-party dependencies.
"""

from .client import (
    ApiError,
    Client,
    EventStream,
    ExecStream,
    Frame,
    LogStream,
)
from .types import (
    Event,
    Service,
    ServiceDocument,
    ServiceList,
    ServiceWrite,
    Task,
    TaskDocument,
    TaskList,
    VersionInfo,
)

__all__ = [
    "ApiError",
    "Client",
    "Event",
    "EventStream",
    "ExecStream",
    "Frame",
    "LogStream",
    "Service",
    "ServiceDocument",
    "ServiceList",
    "ServiceWrite",
    "Task",
    "TaskDocument",
    "TaskList",
    "VersionInfo",
]

__version__ = "0.1.0"
