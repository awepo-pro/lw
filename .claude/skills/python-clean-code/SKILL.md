---
name: modern-python-style
description: Guidelines and patterns for writing framework-grade, maintainable Python code following conventions from major open-source projects (pytest, requests, httpx, CPython).
---

# Modern Python Community Coding Standards

When designing, refactoring, or reviewing Python code, follow these established industry patterns used by tier-1 Python frameworks (`pytest`, `httpx`, `requests`, `pip`, `sqlalchemy`).

---

## 1. Explicit Parameter Default Resolution

### Rule
Never use `val = arg or DEFAULT_GLOBAL` because empty strings (`""`), zeros (`0`), and empty collections (`[]`) are falsy and will silently trigger unintended fallbacks. Use explicit `is None` checks or sentinel objects.

### Patterns

#### A. Standard Optional Overrides (`is None`)
```python
# ❌ BAD: Falsy string "" or 0 unintentionally uses fallback
def connect(timeout: float | None = None, uri: str | None = None):
    actual_timeout = timeout or DEFAULT_TIMEOUT
    actual_uri = uri or MONGODB_URI

# ✅ GOOD: Explicit identity check
def connect(timeout: float | None = None, uri: str | None = None):
    actual_timeout = DEFAULT_TIMEOUT if timeout is None else timeout
    actual_uri = MONGODB_URI if uri is None else uri

```

#### B. Sentinel Objects (Distinguishing `None` from "Omitted")

When `None` is a valid, intentional argument value from the caller, use a module-level sentinel object:

```python
# CPython / dataclasses / pip style
_MISSING = object()

def fetch_user(user_id: str, cache_ttl: float | None | object = _MISSING):
    if cache_ttl is _MISSING:
        # Parameter was completely omitted by caller
        cache_ttl = DEFAULT_CACHE_TTL

    # cache_ttl can now be explicitly passed as None (meaning "disable cache")

```

---

## 2. Global State & Dependency Injection

### Rule

Functions must not implicitly reach into global environment variables or ambient state during execution. Gather configuration early into explicit data objects and pass them down.

### Patterns

#### A. Encapsulated Service / Context Class

```python
# ❌ BAD: Implicit dependency on external global state
def check_db_health():
    client = MongoClient(os.environ["DB_URI"])
    client.ping()

# ✅ GOOD: Explicit dependency injection
class DatabaseHealthChecker:
    def __init__(self, uri: str, client_factory: Callable[..., Any] = MongoClient):
        if not uri:
            raise ValueError("uri must be provided")
        self.uri = uri
        self.client_factory = client_factory

    def check() -> None:
        with self.client_factory(self.uri) as client:
            client.admin.command("ping")

```

#### B. Core Engine + Stateless Helper

Keep internal logic stateful and object-oriented, exposing thin, stateless helper functions at the top-level API boundary.

```python
# httpx / requests style
class Client:
    def request(self, method: str, url: str) -> Response: ...

def get(url: str, **kwargs) -> Response:
    """Stateless top-level wrapper around ephemeral Client session."""
    with Client() as client:
        return client.request("GET", url, **kwargs)

```

---

## 3. Resource Management & Cleanups

### Rule

Avoid manual `try...finally` resource cleanup blocks. Implement Context Managers (`__enter__`/`__exit__`) or generator-based cleanup fixtures (`yield`).

### Patterns

```python
# ❌ BAD: Imperative cleanup logic scattered across try blocks
client = create_client()
try:
    client.do_something()
finally:
    if client is not None:
        client.close()

# ✅ GOOD: Declarative resource safety via Context Manager
with create_client() as client:
    client.do_something()

```

For generator-based setup/teardown (e.g., `pytest` fixtures or `@contextlib.contextmanager`):

```python
from contextlib import contextmanager

@contextmanager
def managed_resource():
    resource = acquire_resource()
    try:
        yield resource
    finally:
        resource.release()

```

---

## 4. Exception Domain Boundaries & Chaining

### Rule

Never let 3rd-party library exceptions or raw low-level driver errors leak unhandled to callers. Catch specific domain errors and wrap them using explicit exception chaining (`raise CustomError(...) from exc`).

### Patterns

```python
# ❌ BAD: Broad catch and missing exception linkage
try:
    client.ping()
except Exception as e:
    raise RuntimeError("DB failed")

# ✅ GOOD: Specific exception catch with PEP 3134 trace preservation
from pymongo.errors import PyMongoError

try:
    client.ping()
except PyMongoError as exc:
    raise DatabaseUnavailableError(
        f"Failed to reach MongoDB server at {self.uri}"
    ) from exc

```

---

## 5. Modern Syntax & Naming Conventions

### Rule

Use Python 3.11+ built-in generics and current idioms instead of legacy `typing` imports, `%`-formatting, manual string concatenation, and unguarded `.strip()` calls. Mark internal-only functions with a leading underscore.

### Patterns

#### A. Built-in Generics Over `typing` Imports

```python
# ❌ BAD
from typing import Any, Awaitable, Callable, Dict, Optional

def get_config(name: str) -> Optional[Dict[str, Any]]: ...

# ✅ GOOD: built-ins are generic since 3.9+, union syntax since 3.10+
def get_config(name: str) -> dict[str, Any] | None: ...
```

#### B. f-strings Over `%`-formatting

```python
# ❌ BAD
msg = "User %s failed with code %d" % (user_id, code)

# ✅ GOOD
msg = f"User {user_id} failed with code {code}"
```

#### C. `textwrap.dedent` for Multi-line Strings (Prompts, SQL, etc.)

```python
# ❌ BAD: manual concatenation is hard to read and edit
prompt = (
    "You are a helpful assistant.\n"
    "Answer concisely.\n"
    "Cite sources when possible.\n"
)

# ✅ GOOD
from textwrap import dedent

prompt = dedent("""\
    You are a helpful assistant.
    Answer concisely.
    Cite sources when possible.
""")
```

#### D. Guard `.strip()` (and similar str methods) with `str()`

```python
# ❌ BAD: fails if content isn't already a str (e.g. None, int)
text = response["choices"][0]["message"]["content"].strip()

# ✅ GOOD
text = str(response["choices"][0]["message"]["content"]).strip()
```

#### E. Leading Underscore for Non-Public Functions

```python
# ❌ BAD: implies public API
def find_table(doc): ...

# ✅ GOOD: signals internal/helper use only
def _find_table(doc): ...
```
