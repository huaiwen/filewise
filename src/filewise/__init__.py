"""Filewise's trusted local API; HTTP callers authenticate through filewise.api."""

from .engine import Engine, FilewiseError
from .models import Actor, BuildRequest, Revision, Scope

__all__ = ["Actor", "BuildRequest", "Engine", "FilewiseError", "Revision", "Scope"]
__version__ = "0.1.0"
